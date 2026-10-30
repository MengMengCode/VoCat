package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"vocat/internal/developer"
	"vocat/internal/device"
	"vocat/internal/store"
	"vocat/internal/vowifi"
)

// registerTools exposes the tool surface. The set is deliberately
// read-heavy: one write tool per domain, no raw AT, no eSIM download or
// delete, no SIM PIN, no network selection — mirroring the Telegram bot's
// boundary for automated consumers. A ReadOnly server registers only the
// read tools.
func (s *Server) registerTools() {
	mcp.AddTool(s.sdk, &mcp.Tool{
		Name:        "list_devices",
		Description: "List configured modem devices with live online status, operator and signal.",
	}, s.listDevices)
	mcp.AddTool(s.sdk, &mcp.Tool{
		Name:        "device_overview",
		Description: "Read one device's live radio status: SIM, signal quality, operator, band and registration.",
	}, s.deviceOverview)
	mcp.AddTool(s.sdk, &mcp.Tool{
		Name:        "sms_list_threads",
		Description: "List SMS conversation threads stored for a device, newest first.",
	}, s.smsListThreads)
	mcp.AddTool(s.sdk, &mcp.Tool{
		Name:        "sms_read_thread",
		Description: "Read the messages of one SMS conversation in chronological order.",
	}, s.smsReadThread)
	mcp.AddTool(s.sdk, &mcp.Tool{
		Name:        "esim_list_profiles",
		Description: "List the eSIM profiles installed on a device's eUICC with enable state.",
	}, s.esimListProfiles)
	mcp.AddTool(s.sdk, &mcp.Tool{
		Name:        "vowifi_status",
		Description: "Read the WiFi Calling (IKEv2/ePDG/IMS) runtime state for a device.",
	}, s.vowifiStatus)
	if s.options.ReadOnly {
		return
	}
	mcp.AddTool(s.sdk, &mcp.Tool{
		Name:        "sms_send",
		Description: "Send an SMS over the cellular AT path. Barred destinations are rejected and the global hourly send limit applies. Messages sent here are not yet written to the web conversation history.",
	}, s.smsSend)
	if s.options.SwitchESIM != nil {
		mcp.AddTool(s.sdk, &mcp.Tool{
			Name:        "esim_switch_profile",
			Description: "Enable a different eSIM profile by ICCID, disabling the currently active one and restoring the target card's saved policy.",
		}, s.esimSwitchProfile)
	}
	mcp.AddTool(s.sdk, &mcp.Tool{
		Name:        "vowifi_reconnect",
		Description: "Request a WiFi Calling reconnect. The tunnel re-establishes asynchronously; poll vowifi_status for the result.",
	}, s.vowifiReconnect)
}

// --- devices ---

type deviceIDInput struct {
	DeviceID string `json:"device_id" jsonschema:"configured Vocat device ID"`
}

type deviceSummary struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Type          string `json:"device_type,omitempty"`
	Online        bool   `json:"online"`
	IMEI          string `json:"imei,omitempty"`
	Model         string `json:"model,omitempty"`
	OperatorName  string `json:"operator_name,omitempty"`
	SignalPercent *int   `json:"signal_percent,omitempty"`
	PhoneNumber   string `json:"phone_number,omitempty"`
	FlightMode    bool   `json:"flight_mode,omitempty"`
}

type deviceListOutput struct {
	Devices []deviceSummary `json:"devices"`
}

func (s *Server) listDevices(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, deviceListOutput, error) {
	configured, err := s.options.Store.ListDevices(ctx)
	if err != nil {
		return nil, deviceListOutput{}, fmt.Errorf("list configured devices: %w", err)
	}
	output := deviceListOutput{Devices: make([]deviceSummary, 0, len(configured))}
	for _, config := range configured {
		summary := deviceSummary{
			ID:   config.ID,
			Name: config.Name,
			Type: config.DeviceType,
		}
		if physicalID, resolveErr := s.resolvePhysical(ctx, config.ID); resolveErr == nil {
			if entry, getErr := s.options.Devices.Get(physicalID); getErr == nil {
				summary.Online = true
				snapshot := entry.Snapshot
				summary.IMEI = snapshotIMEI(snapshot)
				if snapshot != nil {
					summary.Model = snapshot.Model
					summary.OperatorName = snapshot.OperatorName
					summary.SignalPercent = snapshot.SignalPercent
					summary.PhoneNumber = snapshot.Phone.Number
					summary.FlightMode = snapshot.FlightMode
				}
			}
		}
		output.Devices = append(output.Devices, summary)
	}
	return nil, output, nil
}

type deviceOverviewOutput struct {
	ID                 string   `json:"id"`
	Responsive         bool     `json:"responsive"`
	Manufacturer       string   `json:"manufacturer,omitempty"`
	Model              string   `json:"model,omitempty"`
	Firmware           string   `json:"firmware,omitempty"`
	SIMStatus          string   `json:"sim_status,omitempty"`
	SIMReady           bool     `json:"sim_ready"`
	SignalPercent      *int     `json:"signal_percent,omitempty"`
	RSRP               *int     `json:"rsrp_dbm,omitempty"`
	RSRQ               *int     `json:"rsrq_db,omitempty"`
	SINR               *int     `json:"sinr_db,omitempty"`
	AccessTech         string   `json:"access_tech,omitempty"`
	Band               string   `json:"band,omitempty"`
	OperatorName       string   `json:"operator_name,omitempty"`
	OperatorCode       string   `json:"operator_code,omitempty"`
	RegistrationStatus int      `json:"registration_status"`
	PSAttached         bool     `json:"ps_attached"`
	IMEI               string   `json:"imei,omitempty"`
	ICCID              string   `json:"iccid,omitempty"`
	FlightMode         bool     `json:"flight_mode"`
	PhoneNumber        string   `json:"phone_number,omitempty"`
	Warnings           []string `json:"warnings,omitempty"`
	UpdatedAt          string   `json:"updated_at,omitempty"`
}

func (s *Server) deviceOverview(ctx context.Context, _ *mcp.CallToolRequest, input deviceIDInput) (*mcp.CallToolResult, deviceOverviewOutput, error) {
	physicalID, err := s.resolvePhysical(ctx, input.DeviceID)
	if err != nil {
		return nil, deviceOverviewOutput{}, err
	}
	entry, err := s.options.Devices.Get(physicalID)
	if err != nil {
		return nil, deviceOverviewOutput{}, fmt.Errorf("read device %q: %w", input.DeviceID, err)
	}
	output := deviceOverviewOutput{ID: input.DeviceID}
	if snapshot := entry.Snapshot; snapshot != nil {
		output.Responsive = snapshot.Responsive
		output.Manufacturer = snapshot.Manufacturer
		output.Model = snapshot.Model
		output.Firmware = snapshot.Firmware
		output.SIMStatus = snapshot.SIMStatus
		output.SIMReady = snapshot.SIMReady
		output.SignalPercent = snapshot.SignalPercent
		output.RSRP = snapshot.RSRP
		output.RSRQ = snapshot.RSRQ
		output.SINR = snapshot.SINR
		output.AccessTech = snapshot.AccessTech
		output.Band = snapshot.Band
		output.OperatorName = snapshot.OperatorName
		output.OperatorCode = snapshot.OperatorCode
		output.RegistrationStatus = snapshot.RegistrationStatus
		output.PSAttached = snapshot.PSAttached
		output.IMEI = snapshot.IMEI
		output.ICCID = snapshot.ICCID
		output.FlightMode = snapshot.FlightMode
		output.PhoneNumber = snapshot.Phone.Number
		output.Warnings = snapshot.Warnings
		if !snapshot.UpdatedAt.IsZero() {
			output.UpdatedAt = snapshot.UpdatedAt.Format(time.RFC3339)
		}
	}
	// IMSI is deliberately absent: tool output must not carry subscriber
	// identifiers beyond what an operator needs.
	return nil, output, nil
}

// --- sms ---

type smsThreadSummary struct {
	Peer          string `json:"peer"`
	DisplayName   string `json:"display_name,omitempty"`
	LastMessage   string `json:"last_message,omitempty"`
	LastTimestamp string `json:"last_timestamp,omitempty"`
	UnreadCount   int    `json:"unread_count"`
	MessageCount  int    `json:"message_count"`
}

type smsThreadListOutput struct {
	Threads []smsThreadSummary `json:"threads"`
}

func (s *Server) smsListThreads(ctx context.Context, _ *mcp.CallToolRequest, input deviceIDInput) (*mcp.CallToolResult, smsThreadListOutput, error) {
	filter, err := s.smsStoreFilter(ctx, input.DeviceID)
	if err != nil {
		return nil, smsThreadListOutput{}, err
	}
	contacts, err := s.options.Store.ListSMSContacts(ctx, filter)
	if err != nil {
		return nil, smsThreadListOutput{}, fmt.Errorf("list SMS threads: %w", err)
	}
	output := smsThreadListOutput{Threads: make([]smsThreadSummary, 0, len(contacts))}
	for _, contact := range contacts {
		thread := smsThreadSummary{
			Peer:         contact.Peer,
			DisplayName:  contact.DisplayName,
			LastMessage:  contact.LastMessage,
			UnreadCount:  contact.UnreadCount,
			MessageCount: contact.MessageCount,
		}
		if !contact.LastTimestamp.IsZero() {
			thread.LastTimestamp = contact.LastTimestamp.Format(time.RFC3339)
		}
		output.Threads = append(output.Threads, thread)
	}
	return nil, output, nil
}

type smsReadThreadInput struct {
	DeviceID string `json:"device_id" jsonschema:"configured Vocat device ID"`
	Peer     string `json:"peer" jsonschema:"counterpart phone number of the thread"`
	Limit    int    `json:"limit,omitempty" jsonschema:"maximum number of messages to return (default 50)"`
}

type smsMessageView struct {
	Timestamp string `json:"timestamp"`
	Direction string `json:"direction"`
	Body      string `json:"body"`
	Status    string `json:"status,omitempty"`
}

type smsThreadOutput struct {
	Peer     string           `json:"peer"`
	Messages []smsMessageView `json:"messages"`
}

func (s *Server) smsReadThread(ctx context.Context, _ *mcp.CallToolRequest, input smsReadThreadInput) (*mcp.CallToolResult, smsThreadOutput, error) {
	input.Peer = strings.TrimSpace(input.Peer)
	if input.Peer == "" {
		return nil, smsThreadOutput{}, errors.New("peer is required")
	}
	if input.Limit <= 0 || input.Limit > 500 {
		input.Limit = 50
	}
	filter, err := s.smsStoreFilter(ctx, input.DeviceID)
	if err != nil {
		return nil, smsThreadOutput{}, err
	}
	filter.Peer = input.Peer
	filter.Limit = input.Limit
	messages, err := s.options.Store.ListSMSMessages(ctx, filter)
	if err != nil {
		return nil, smsThreadOutput{}, fmt.Errorf("read SMS thread: %w", err)
	}
	output := smsThreadOutput{Peer: input.Peer, Messages: make([]smsMessageView, 0, len(messages))}
	// The store returns newest first; agents read conversations oldest first.
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		view := smsMessageView{
			Direction: message.Direction,
			Body:      message.Body,
			Status:    message.Status,
		}
		if !message.Timestamp.IsZero() {
			view.Timestamp = message.Timestamp.Format(time.RFC3339)
		}
		output.Messages = append(output.Messages, view)
	}
	return nil, output, nil
}

type smsSendInput struct {
	DeviceID string `json:"device_id" jsonschema:"configured Vocat device ID used for sending"`
	Phone    string `json:"phone" jsonschema:"recipient phone number in international format"`
	Message  string `json:"message" jsonschema:"message text to send"`
}

type smsSendOutput struct {
	Status           string `json:"status"`
	PartsAttempted   int    `json:"parts_attempted"`
	PartsAccepted    int    `json:"parts_accepted"`
	AllPartsAccepted bool   `json:"all_parts_accepted"`
	SubmissionStatus string `json:"submission_status,omitempty"`
	Warning          string `json:"warning,omitempty"`
}

func (s *Server) smsSend(ctx context.Context, _ *mcp.CallToolRequest, input smsSendInput) (*mcp.CallToolResult, smsSendOutput, error) {
	input.Phone = strings.TrimSpace(input.Phone)
	input.Message = strings.TrimSpace(input.Message)
	if input.DeviceID == "" || input.Phone == "" || input.Message == "" {
		return nil, smsSendOutput{}, errors.New("device_id, phone and message are required")
	}
	if blocked, reason := device.BlockedSMSDestination(input.Phone); blocked {
		return nil, smsSendOutput{}, errors.New(reason)
	}
	// Validate before consuming a send slot: a malformed agent message must
	// not burn quota. Same guardrail order as the HTTP path.
	if _, err := device.PrepareSMSSubmitTPDUs(input.Phone, input.Message); err != nil {
		return nil, smsSendOutput{}, fmt.Errorf("message rejected: %w", err)
	}
	if _, err := s.options.Store.Device(ctx, input.DeviceID); err != nil {
		return nil, smsSendOutput{}, fmt.Errorf("unknown device %q", input.DeviceID)
	}
	// Resolve the physical modem before reserving a send slot, matching the
	// HTTP path: an offline device must not burn quota.
	physicalID, err := s.resolvePhysical(ctx, input.DeviceID)
	if err != nil {
		return nil, smsSendOutput{}, err
	}
	limit := developer.SMSHourlyLimit(ctx, s.options.Store)
	reservation, err := s.options.Store.ReserveSMSSend(ctx, input.DeviceID, limit, time.Now().UTC())
	if err != nil {
		return nil, smsSendOutput{}, fmt.Errorf("reserve send slot: %w", err)
	}
	if !reservation.Allowed {
		return nil, smsSendOutput{}, fmt.Errorf(
			"global hourly SMS limit reached (%d used of %d); retry after %s",
			reservation.Used, reservation.Limit, reservation.ResetAt.Format(time.RFC3339),
		)
	}
	result, sendErr := s.options.Devices.SendSMS(ctx, physicalID, input.Phone, input.Message)
	if sendErr != nil && result.PartsAttempted == 0 {
		return nil, smsSendOutput{}, fmt.Errorf("send failed: %w", sendErr)
	}
	status := "submitted"
	warning := ""
	// Partial submissions leave parts in flight; a retry of the whole
	// message would duplicate them, so the agent must be told explicitly.
	if sendErr != nil || !result.AllPartsAccepted {
		status = "partial_or_unconfirmed"
		warning = "some parts may not have been accepted; do not resend the whole message"
		s.options.Logger.Warn("mcp sms send not fully confirmed",
			"device_id", input.DeviceID,
			"parts_attempted", result.PartsAttempted,
			"parts_accepted", result.PartsAccepted,
			"error", sendErr)
	}
	s.audit(ctx, "mcp.sms.send", input.DeviceID)
	return nil, smsSendOutput{
		Status:           status,
		PartsAttempted:   result.PartsAttempted,
		PartsAccepted:    result.PartsAccepted,
		AllPartsAccepted: result.AllPartsAccepted,
		SubmissionStatus: result.SubmissionStatus,
		Warning:          warning,
	}, nil
}

// smsStoreFilter resolves a configured device to the stable hardware key the
// conversation history is stored under, preferring the live modem IMEI so a
// renamed device keeps its history — the same rule as the web API's filter.
func (s *Server) smsStoreFilter(ctx context.Context, deviceID string) (store.SMSFilter, error) {
	config, err := s.options.Store.Device(ctx, deviceID)
	if err != nil {
		return store.SMSFilter{}, fmt.Errorf("unknown device %q", deviceID)
	}
	imei := strings.TrimSpace(config.ModemIMEI)
	if physicalID, resolveErr := s.resolvePhysical(ctx, deviceID); resolveErr == nil {
		if entry, getErr := s.options.Devices.Get(physicalID); getErr == nil {
			imei = firstNonEmpty(snapshotIMEI(entry.Snapshot), imei)
		}
	}
	if imei != "" {
		return store.SMSFilter{ModemIMEI: imei}, nil
	}
	return store.SMSFilter{DeviceID: deviceID}, nil
}

// --- esim ---

type esimProfileView struct {
	ICCID           string `json:"iccid"`
	AID             string `json:"aid,omitempty"`
	ServiceProvider string `json:"service_provider,omitempty"`
	Nickname        string `json:"nickname,omitempty"`
	Enabled         bool   `json:"enabled"`
}

type esimListOutput struct {
	EID      string            `json:"eid,omitempty"`
	Profiles []esimProfileView `json:"profiles"`
}

func (s *Server) esimListProfiles(ctx context.Context, _ *mcp.CallToolRequest, input deviceIDInput) (*mcp.CallToolResult, esimListOutput, error) {
	physicalID, err := s.resolvePhysical(ctx, input.DeviceID)
	if err != nil {
		return nil, esimListOutput{}, err
	}
	info, err := s.options.Devices.ESIMListProfiles(ctx, physicalID)
	if err != nil {
		return nil, esimListOutput{}, fmt.Errorf("list eSIM profiles: %w", err)
	}
	output := esimListOutput{
		EID:      info.EID,
		Profiles: make([]esimProfileView, 0, len(info.Profiles)),
	}
	for _, profile := range info.Profiles {
		output.Profiles = append(output.Profiles, esimProfileView{
			ICCID:           profile.ICCID,
			AID:             profile.AID,
			ServiceProvider: profile.ServiceProvider,
			Nickname:        profile.Nickname,
			Enabled:         profile.State == 1,
		})
	}
	return nil, output, nil
}

type esimSwitchInput struct {
	DeviceID string `json:"device_id" jsonschema:"configured Vocat device ID"`
	ICCID    string `json:"iccid" jsonschema:"ICCID of the profile to enable"`
	AID      string `json:"aid,omitempty" jsonschema:"profile AID; resolved automatically from the ICCID when omitted"`
}

type esimSwitchOutput struct {
	SwitchedTo string `json:"switched_to_iccid"`
}

func (s *Server) esimSwitchProfile(ctx context.Context, _ *mcp.CallToolRequest, input esimSwitchInput) (*mcp.CallToolResult, esimSwitchOutput, error) {
	if s.options.ReadOnly || s.options.SwitchESIM == nil {
		return nil, esimSwitchOutput{}, errors.New("eSIM profile switching is not available")
	}
	input.DeviceID = strings.TrimSpace(input.DeviceID)
	input.ICCID = strings.TrimSpace(input.ICCID)
	input.AID = strings.TrimSpace(input.AID)
	if input.DeviceID == "" || input.ICCID == "" {
		return nil, esimSwitchOutput{}, errors.New("device_id and iccid are required")
	}
	// AID discovery may require the same SIM/QMI lease as the active VoWiFi
	// session. Leave it, physical resolution and switching inside the shared
	// operation so runtime maintenance happens before any card access.
	if err := s.options.SwitchESIM(ctx, input.DeviceID, input.ICCID, input.AID); err != nil {
		return nil, esimSwitchOutput{}, fmt.Errorf("switch profile: %w", err)
	}
	s.audit(ctx, "mcp.esim.switch", input.DeviceID)
	return nil, esimSwitchOutput{SwitchedTo: input.ICCID}, nil
}

// --- vowifi ---

type vowifiStatusOutput struct {
	Enabled         bool   `json:"enabled"`
	Active          bool   `json:"active"`
	Phase           string `json:"phase,omitempty"`
	TunnelReady     bool   `json:"tunnel_ready"`
	IMSReady        bool   `json:"ims_ready"`
	SMSReady        bool   `json:"sms_ready"`
	EPDG            string `json:"epdg,omitempty"`
	CarrierProfile  string `json:"carrier_profile,omitempty"`
	IMSRegistration string `json:"ims_registration,omitempty"`
	PhoneNumber     string `json:"phone_number,omitempty"`
	LastErrorClass  string `json:"last_error_class,omitempty"`
}

func vowifiStatusFromState(state vowifi.State) vowifiStatusOutput {
	return vowifiStatusOutput{
		Enabled:         state.Enabled,
		Active:          state.Active,
		Phase:           string(state.Phase),
		TunnelReady:     state.TunnelReady,
		IMSReady:        state.IMSReady,
		SMSReady:        state.SMSReady,
		EPDG:            state.EPDG,
		CarrierProfile:  state.CarrierProfile,
		IMSRegistration: state.IMSRegistration,
		PhoneNumber:     state.PhoneNumber,
		LastErrorClass:  state.LastErrorClass,
		// IMSI is deliberately absent from tool output.
	}
}

func (s *Server) vowifiStatus(_ context.Context, _ *mcp.CallToolRequest, input deviceIDInput) (*mcp.CallToolResult, vowifiStatusOutput, error) {
	if s.options.VoWiFi == nil {
		return nil, vowifiStatusOutput{}, errors.New("WiFi Calling runtime is not available")
	}
	state, err := s.options.VoWiFi.State(input.DeviceID)
	if err != nil {
		return nil, vowifiStatusOutput{}, fmt.Errorf("read WiFi Calling state: %w", err)
	}
	return nil, vowifiStatusFromState(state), nil
}

func (s *Server) vowifiReconnect(_ context.Context, _ *mcp.CallToolRequest, input deviceIDInput) (*mcp.CallToolResult, vowifiStatusOutput, error) {
	if s.options.VoWiFi == nil {
		return nil, vowifiStatusOutput{}, errors.New("WiFi Calling runtime is not available")
	}
	state, err := s.options.VoWiFi.RequestReconnect(input.DeviceID)
	if err != nil {
		return nil, vowifiStatusOutput{}, fmt.Errorf("request reconnect: %w", err)
	}
	s.audit(context.Background(), "mcp.vowifi.reconnect", input.DeviceID)
	return nil, vowifiStatusFromState(state), nil
}

func snapshotIMEI(snapshot *device.Snapshot) string {
	if snapshot == nil {
		return ""
	}
	return strings.TrimSpace(snapshot.IMEI)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
