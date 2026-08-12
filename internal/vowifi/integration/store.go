// Package integration connects the protocol runtime to vocat's persistent
// configuration and modem inventory. It contains no IKE, IMS, or SIM protocol
// implementation.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"vocat/internal/device"
	"vocat/internal/store"
	"vocat/internal/vowifi"
)

type ProxyResolver struct {
	Store *store.Store
}

func (resolver ProxyResolver) Resolve(
	ctx context.Context,
	request vowifi.ProxyRequest,
) (vowifi.ProxyRoute, error) {
	if resolver.Store == nil {
		return vowifi.ProxyRoute{}, errors.New("vowifi proxy resolver: store is nil")
	}
	deviceID := strings.TrimSpace(request.DeviceID)
	iccid := strings.TrimSpace(request.ICCID)
	if deviceID == "" || iccid == "" {
		return vowifi.ProxyRoute{Mode: vowifi.ProxyModeDirect}, nil
	}
	binding, err := resolver.Store.DeviceProxyBinding(ctx, iccid)
	if errors.Is(err, store.ErrNotFound) {
		return vowifi.ProxyRoute{Mode: vowifi.ProxyModeDirect}, nil
	}
	if err != nil {
		return vowifi.ProxyRoute{}, fmt.Errorf("resolve proxy binding for ICCID %s: %w", iccid, err)
	}
	upstream, err := resolver.Store.UpstreamProxy(ctx, binding.UpstreamProxyID)
	if err != nil {
		return vowifi.ProxyRoute{}, fmt.Errorf(
			"load upstream proxy %q for device %s: %w",
			binding.UpstreamProxyID,
			deviceID,
			err,
		)
	}
	if !upstream.Enabled {
		return vowifi.ProxyRoute{}, fmt.Errorf(
			"upstream proxy %q for device %s is disabled",
			upstream.ID,
			deviceID,
		)
	}
	return vowifi.ProxyRoute{
		Mode:     vowifi.ProxyModeSOCKS5,
		ID:       upstream.ID,
		Address:  upstream.Addr,
		Username: upstream.Username,
		Password: upstream.Password,
	}, nil
}

type PhoneStore struct {
	Store    *store.Store
	DeviceID string
}

func (phones PhoneStore) SaveAssociatedNumber(
	ctx context.Context,
	record vowifi.PhoneRecord,
) error {
	if phones.Store == nil {
		return errors.New("vowifi phone store: store is nil")
	}
	switch record.Source {
	case vowifi.PhoneSourceAssociatedMSISDN, vowifi.PhoneSourcePAssociatedURI:
	default:
		return fmt.Errorf("vowifi phone store: untrusted source %q", record.Source)
	}
	return phones.Store.UpsertPhoneAssociation(ctx, store.PhoneAssociation{
		ICCID:     record.ICCID,
		DeviceID:  strings.TrimSpace(phones.DeviceID),
		Number:    record.Number,
		Source:    record.Source,
		UpdatedAt: record.UpdatedAt,
	})
}

type DeviceReader interface {
	Get(string) (device.Device, error)
}

type StateProjector struct {
	Store   *store.Store
	Devices DeviceReader
}

func (projector StateProjector) Save(
	ctx context.Context,
	state vowifi.State,
) error {
	if projector.Store == nil {
		return errors.New("vowifi state projector: store is nil")
	}
	runtime := store.VoWiFiRuntime{
		DeviceID:          state.DeviceID,
		ICCID:             strings.TrimSpace(state.ICCID),
		IMSI:              strings.TrimSpace(state.IMSI),
		Phase:             string(state.Phase),
		DataplaneMode:     dataplaneMode(state),
		SIMReady:          state.SIMReady,
		AccessReady:       state.AccessReady,
		TunnelReady:       state.TunnelReady,
		IMSReady:          state.IMSReady,
		SMSReady:          state.SMSReady,
		RegStatus:         boolInt(state.IMSReady),
		RegStatusText:     registrationText(state),
		NetworkMode:       "Wi-Fi",
		LocalPhone:        state.PhoneNumber,
		PhoneNumberSource: state.PhoneNumberSource,
		LastErrorClass:    state.LastErrorClass,
		LastError:         state.LastError,
		LastReason:        state.LastReason,
		UpdatedAt:         state.UpdatedAt,
	}
	if projector.Devices != nil {
		if entry, err := projector.Devices.Get(state.DeviceID); err == nil && entry.Snapshot != nil {
			// An active VoWiFi session belongs to the identity captured when it
			// was established. Do not relabel its phone number with a newly
			// selected eSIM profile while teardown is still in progress.
			if runtime.ICCID == "" {
				runtime.ICCID = strings.TrimSpace(entry.Snapshot.ICCID)
			}
			if runtime.IMSI == "" {
				runtime.IMSI = strings.TrimSpace(entry.Snapshot.IMSI)
			}
		}
	}
	if runtime.LocalPhone == "" && runtime.ICCID != "" {
		if association, err := projector.Store.PhoneAssociation(ctx, runtime.ICCID); err == nil {
			runtime.LocalPhone = association.Number
			runtime.PhoneNumberSource = association.Source
		} else if !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("restore associated phone number: %w", err)
		}
	}

	var err error
	runtime.Tunnel, err = marshalObject(map[string]any{
		"established":    state.TunnelReady,
		"name":           state.TunnelName,
		"dataplane_mode": state.DataplaneMode,
		"epdg":           state.EPDG,
		"proxy_mode":     state.ProxyMode,
		"proxy_id":       state.ProxyID,
		"security_audit": state.Security,
	})
	if err != nil {
		return err
	}
	runtime.IMSCore, err = marshalObject(map[string]any{
		"registered":         state.IMSReady,
		"registration_state": state.IMSRegistration,
		"associated_number":  runtime.LocalPhone,
		"number_source":      runtime.PhoneNumberSource,
	})
	if err != nil {
		return err
	}
	runtime.SMSIP, err = marshalObject(map[string]any{
		"ready": state.SMSReady,
	})
	if err != nil {
		return err
	}
	runtime.Extra, err = marshalObject(map[string]any{
		"enabled":              state.Enabled,
		"active":               state.Active,
		"pure_airplane_policy": state.PureAirplanePolicy,
		"home_mcc":             state.HomeMCC,
		"home_mnc":             state.HomeMNC,
		"carrier_plmn":         state.CarrierPLMN,
		"carrier_preset_id":    state.CarrierPresetID,
		"carrier_source":       state.CarrierSource,
		"epdg_source":          state.EPDGSource,
		"ims_identity_source":  state.IMSIdentitySource,
		"warnings":             state.Warnings,
		"cleanup_errors":       state.CleanupErrors,
		"attempt":              state.Attempt,
		"sequence":             state.Sequence,
		"started_at":           state.StartedAt,
	})
	if err != nil {
		return err
	}
	return projector.Store.UpsertVoWiFiRuntime(ctx, runtime)
}

func dataplaneMode(state vowifi.State) string {
	if value := strings.TrimSpace(state.DataplaneMode); value == "userspace" || value == "xfrm" {
		return value
	}
	if state.TunnelReady || state.Phase == vowifi.PhaseTunnelReady ||
		state.Phase == vowifi.PhaseIMSReady || state.Phase == vowifi.PhaseSMSReady {
		return "ipsec"
	}
	return ""
}

func registrationText(state vowifi.State) string {
	if value := strings.TrimSpace(state.IMSRegistration); value != "" {
		return value
	}
	if state.IMSReady {
		return "registered"
	}
	if state.Phase == vowifi.PhaseFailed &&
		strings.HasPrefix(state.LastErrorClass, "ims") {
		return "registration failed"
	}
	return "not registered"
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func marshalObject(value map[string]any) (json.RawMessage, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal VoWiFi runtime projection: %w", err)
	}
	return encoded, nil
}
