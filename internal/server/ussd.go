package server

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"vocat/internal/device"
	"vocat/internal/store"
	"vocat/internal/vowifi"
)

var errInvalidUSSDRequest = errors.New("invalid USSD request")

// startUSSD is shared by HTTP and Telegram. IMS runtimes are keyed by the
// configured ID; only the cellular device manager accepts the discovered ID.
func (s *Server) startUSSD(ctx context.Context, config store.Device, physicalID, code string) (device.USSDResult, error) {
	code = strings.TrimSpace(code)
	if !device.ValidUSSDCode(code) {
		return device.USSDResult{}, fmt.Errorf("%w: invalid USSD service code", errInvalidUSSDRequest)
	}
	if code == "*#06#" || code == "*#06" {
		imei := config.ModemIMEI
		if imei == "" && s.store != nil {
			if runtime, err := s.store.DeviceRuntime(ctx, config.ID); err == nil {
				imei = runtime.IMEI
			}
		}
		if imei != "" {
			return device.USSDResult{Code: code, Text: "IMEI: " + imei, Status: "final"}, nil
		}
	}
	if (code == "*#0000#" || code == "*#0000") && s.store != nil {
		if runtime, err := s.store.DeviceRuntime(ctx, config.ID); err == nil && runtime.Firmware != "" {
			return device.USSDResult{Code: code, Text: "Software Version: " + runtime.Firmware, Status: "final"}, nil
		}
	}
	if !config.VoWiFiEnabled {
		s.logUSSDRoute(config.ID, physicalID, "cellular")
		return s.devices.USSD(ctx, physicalID, code)
	}
	if err := s.requireUSSI(config.ID); err != nil {
		return device.USSDResult{}, err
	}
	s.logUSSDRoute(config.ID, physicalID, "ims")
	result, err := s.vowifi.(imsUSSIController).SendUSSI(ctx, config.ID, vowifi.USSISubmitRequest{Code: code})
	if err != nil {
		// Even ErrUSSINotReady must not switch transport: VoWiFi owns the radio.
		return device.USSDResult{}, err
	}
	mapped := ussdResultFromUSSI(result)
	mapped.Code = code
	if mapped.Continueable {
		if result.SessionID == "" {
			return device.USSDResult{}, errors.New("IMS returned an interactive USSI result without a dialog ID")
		}
		mapped.SessionID = s.openUSSDSessionWithNetwork(config.ID, result.SessionID)
	}
	return mapped, nil
}

func (s *Server) requireUSSI(deviceID string) error {
	if s.vowifi == nil {
		return fmt.Errorf("%w: VoWiFi runtime unavailable; waiting for IMS registration", vowifi.ErrUSSINotReady)
	}
	if _, ok := s.vowifi.(imsUSSIController); !ok {
		return fmt.Errorf("%w: VoWiFi runtime does not support USSI", vowifi.ErrUSSINotReady)
	}
	state, err := s.vowifi.State(deviceID)
	if err != nil {
		return fmt.Errorf("%w: cannot read IMS state: %w", vowifi.ErrUSSINotReady, err)
	}
	if !state.IMSReady {
		return fmt.Errorf("%w: waiting for IMS registration (phase=%s, last_error=%s)", vowifi.ErrUSSINotReady, state.Phase, state.LastError)
	}
	return nil
}

func (s *Server) continueUSSD(ctx context.Context, sessionID, input string) (device.USSDResult, error) {
	sessionID, input = strings.TrimSpace(sessionID), strings.TrimSpace(input)
	deviceID, err := s.ussdSessionDevice(sessionID)
	if err != nil {
		if strings.HasPrefix(sessionID, ussiSessionPrefix) {
			return device.USSDResult{}, err
		}
		return s.devices.ContinueUSSD(ctx, sessionID, input)
	}
	networkID := s.ussdNetworkSession(sessionID)
	if networkID == "" {
		return device.USSDResult{}, vowifi.ErrUSSISessionNotFound
	}
	if input == "" {
		return device.USSDResult{}, fmt.Errorf("%w: USSI continue input is empty", errInvalidUSSDRequest)
	}
	if err := s.requireUSSI(deviceID); err != nil {
		return device.USSDResult{}, err
	}
	result, err := s.vowifi.(imsUSSIController).SendUSSI(ctx, deviceID, vowifi.USSISubmitRequest{SessionID: networkID, Input: input})
	if err != nil {
		if errors.Is(err, vowifi.ErrUSSISessionNotFound) {
			s.dropUSSDSession(sessionID)
		}
		return device.USSDResult{}, err
	}
	if result.SessionID != "" && result.SessionID != networkID {
		return device.USSDResult{}, errors.New("IMS returned a different USSI dialog ID")
	}
	mapped := ussdResultFromUSSI(result)
	mapped.Code = input
	if mapped.Continueable {
		// Keep one token for the lifetime of this exact network dialog.
		mapped.SessionID = sessionID
	} else {
		s.dropUSSDSession(sessionID)
	}
	return mapped, nil
}

func (s *Server) cancelUSSD(ctx context.Context, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	deviceID, err := s.ussdSessionDevice(sessionID)
	if err != nil {
		if strings.HasPrefix(sessionID, ussiSessionPrefix) {
			return err
		}
		return s.devices.CancelUSSD(ctx, sessionID)
	}
	networkID := s.ussdNetworkSession(sessionID)
	if networkID == "" {
		return vowifi.ErrUSSISessionNotFound
	}
	canceler, ok := s.vowifi.(imsUSSICanceler)
	if !ok {
		return fmt.Errorf("%w: VoWiFi runtime cannot cancel USSI", vowifi.ErrUSSINotReady)
	}
	if err := canceler.CancelUSSI(ctx, deviceID, networkID); err != nil {
		if errors.Is(err, vowifi.ErrUSSISessionNotFound) {
			s.dropUSSDSession(sessionID)
		}
		return err
	}
	s.dropUSSDSession(sessionID)
	return nil
}

func (s *Server) logUSSDRoute(configuredID, physicalID, transport string) {
	if s.logger != nil {
		s.logger.Info("USSD request routed", "category", "hardware", "event", "ussd.route",
			"device_id", configuredID, "physical_id", physicalID, "transport", transport)
	}
}
