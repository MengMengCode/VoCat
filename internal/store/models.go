package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

const SecretMask = "********"

type Device struct {
	ID                          string
	Name                        string
	DeviceType                  string
	Interface                   string
	ControlDevice               string
	ATPort                      string
	USBPath                     string
	AudioDevice                 string
	ModemIMEI                   string
	APN                         string
	IMSAPN                      string
	IMSPrivateIdentity          string
	IMSPublicIdentity           string
	IMSSMSCenter                string
	IMSTransport                string
	IMSAllowIMSIDerivedIdentity bool
	VoWiFiEAPMethod             string
	VoWiFiAllowSHA1             bool
	VoWiFiUseMODP1024           bool
	ProxyPort                   int
	BaudRate                    int
	DataBits                    int
	StopBits                    int
	Parity                      string
	DeviceBackend               string
	ESIMTransport               string
	QMIUseProxy                 bool
	QMIProxyPath                string
	QMIProxyExecutable          string
	NetworkEnabled              bool
	SMSEnabled                  bool
	VoWiFiEnabled               bool
	Extra                       json.RawMessage
	CreatedAt                   time.Time
	UpdatedAt                   time.Time
}

type DeviceRuntime struct {
	DeviceID          string
	Running           bool
	Healthy           bool
	ControlOnline     bool
	PhysicalPresent   bool
	WorkerRunning     bool
	DataConnected     bool
	RadioRegistered   bool
	NetworkConnected  bool
	FlightMode        bool
	LifecyclePhase    string
	LifecycleReason   string
	PublicIP          string
	PrivateIP         string
	Operator          string
	NativeMCC         string
	NativeMNC         string
	NativeSPN         string
	NetworkMode       string
	NetworkDuplex     string
	RadioBand         string
	RadioChannel      int
	SignalDBM         int
	SignalRSRP        *int
	SignalRSRQ        *int
	SignalSINR        *int
	IMEI              string
	ICCID             string
	IMSI              string
	Firmware          string
	RegStatus         int
	RegStatusText     string
	PSAttached        *bool
	SIMInserted       *bool
	OperatingMode     *int
	PhoneNumber       string
	PhoneNumberSource string
	Traffic           json.RawMessage
	Extra             json.RawMessage
	UpdatedAt         time.Time
}

type VoWiFiRuntime struct {
	DeviceID          string
	Phase             string
	DataplaneMode     string
	ICCID             string
	IMSI              string
	SIMReady          bool
	AccessReady       bool
	TunnelReady       bool
	IMSReady          bool
	SMSReady          bool
	RegStatus         int
	RegStatusText     string
	NetworkMode       string
	LocalPhone        string
	PhoneNumberSource string
	LastErrorClass    string
	LastError         string
	LastReason        string
	Tunnel            json.RawMessage
	IMSCore           json.RawMessage
	SMSIP             json.RawMessage
	Extra             json.RawMessage
	UpdatedAt         time.Time
}

// PhoneAssociation is a number explicitly published by IMS for one SIM. It is
// keyed by ICCID so a verified number survives service restarts and follows the
// SIM without ever being inferred from IMSI.
type PhoneAssociation struct {
	ICCID     string
	DeviceID  string
	Number    string
	Source    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type SMSMessage struct {
	ID            int64
	MessageID     string
	DeviceID      string
	ModemIMEI     string
	IMSI          string
	Peer          string
	Direction     string
	Body          string
	Timestamp     time.Time
	Status        string
	Source        string
	PartsTotal    int
	DeliveryState string
	Read          bool
	Extra         json.RawMessage
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type SMSFilter struct {
	DeviceID  string
	ModemIMEI string
	IMSI      string
	IMSIExact bool
	Peer      string
	Since     time.Time
	Until     time.Time
	BeforeID  int64
	Limit     int
}

// SMSDeliveryReport is network evidence for one submitted SMS part. The
// message reference is the TP-MR returned in SMS-STATUS-REPORT.
type SMSDeliveryReport struct {
	ReportID          string
	DeviceID          string
	ModemIMEI         string
	IMSI              string
	Peer              string
	Source            string
	MessageReference  int
	StatusCode        int
	DeliveryState     string
	ServiceCenterTime *time.Time
	DischargeTime     *time.Time
	ReceivedAt        time.Time
}

type SMSContact struct {
	DeviceID      string
	DeviceName    string
	ModemIMEI     string
	IMSI          string
	LocalPhone    string
	Peer          string
	DisplayName   string
	LastMessage   string
	LastTimestamp time.Time
	Direction     string
	LastSMSID     int64
	UnreadCount   int
	MessageCount  int
}

type LocalProxyConfig struct {
	ID          string
	Name        string
	Mode        string
	DeviceID    string
	ListenAddr  string
	ListenPort  int
	Enabled     bool
	AuthEnabled bool
	Username    string
	Password    string
	Extra       json.RawMessage
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (value LocalProxyConfig) Redacted() LocalProxyConfig {
	if value.Password != "" {
		value.Password = SecretMask
	}
	return value
}

func (value LocalProxyConfig) Public() LocalProxyConfig {
	value.Password = ""
	return value
}

func (value LocalProxyConfig) SensitiveValues() []string {
	if value.Password == "" || value.Password == SecretMask {
		return nil
	}
	return []string{value.Password}
}

type UpstreamProxy struct {
	ID        string
	Name      string
	Addr      string
	Username  string
	Password  string
	Enabled   bool
	Extra     json.RawMessage
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (value UpstreamProxy) Redacted() UpstreamProxy {
	if value.Password != "" {
		value.Password = SecretMask
	}
	return value
}

func (value UpstreamProxy) Public() UpstreamProxy {
	value.Password = ""
	return value
}

func (value UpstreamProxy) SensitiveValues() []string {
	if value.Password == "" || value.Password == SecretMask {
		return nil
	}
	return []string{value.Password}
}

type CountryRule struct {
	CountryCode     string
	CountryName     string
	UpstreamProxyID string
	Enabled         bool
	Extra           json.RawMessage
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// DeviceProxyBinding selects the SOCKS5 upstream used by one device's whole
// VoWiFi runtime. The IKE/IPsec transport uses this route and IMS/SMS then
// travel inside that tunnel.
type DeviceProxyBinding struct {
	DeviceID        string
	UpstreamProxyID string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type NotificationSetting struct {
	Channel         string
	Enabled         bool
	Config          json.RawMessage
	SensitiveFields []string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (value NotificationSetting) Redacted() NotificationSetting {
	value.Config = redactJSONFields(value.Config, value.SensitiveFields, SecretMask)
	return value
}

func (value NotificationSetting) Public() NotificationSetting {
	value.Config = redactJSONFields(value.Config, value.SensitiveFields, "")
	return value
}

func (value NotificationSetting) SensitiveValues() []string {
	document, err := decodeJSONObject(value.Config)
	if err != nil {
		return nil
	}
	values := make([]string, 0, len(value.SensitiveFields))
	for _, field := range value.SensitiveFields {
		if secret, ok := getJSONPath(document, field).(string); ok &&
			secret != "" && secret != SecretMask {
			values = append(values, secret)
		}
	}
	return values
}

type AppSetting struct {
	Key       string
	Value     json.RawMessage
	Sensitive bool
	UpdatedAt time.Time
}

func (value AppSetting) Redacted() AppSetting {
	if value.Sensitive {
		value.Value = json.RawMessage(strconv.Quote(SecretMask))
	}
	return value
}

func (value AppSetting) Public() AppSetting {
	if value.Sensitive {
		value.Value = json.RawMessage(`null`)
	}
	return value
}

func (value AppSetting) SensitiveValues() []string {
	if !value.Sensitive {
		return nil
	}
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(value.Value))
	decoder.UseNumber()
	if decoder.Decode(&decoded) != nil {
		return nil
	}
	var values []string
	collectJSONStringValues(decoded, &values)
	return uniqueNonemptyStrings(values)
}

type SensitiveValuesProvider interface {
	SensitiveValues() []string
}

func RedactText(text string, providers ...SensitiveValuesProvider) string {
	var secrets []string
	for _, provider := range providers {
		if !nilSensitiveProvider(provider) {
			secrets = append(secrets, provider.SensitiveValues()...)
		}
	}
	sort.Slice(secrets, func(i, j int) bool {
		return len(secrets[i]) > len(secrets[j])
	})
	for _, secret := range secrets {
		if secret != "" && secret != SecretMask {
			text = strings.ReplaceAll(text, secret, SecretMask)
		}
	}
	return text
}

func nilSensitiveProvider(provider SensitiveValuesProvider) bool {
	if provider == nil {
		return true
	}
	value := reflect.ValueOf(provider)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
		reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func collectJSONStringValues(value any, result *[]string) {
	switch typed := value.(type) {
	case string:
		if typed != "" && typed != SecretMask {
			*result = append(*result, typed)
		}
	case []any:
		for _, item := range typed {
			collectJSONStringValues(item, result)
		}
	case map[string]any:
		for _, item := range typed {
			collectJSONStringValues(item, result)
		}
	}
}

type AuditEvent struct {
	ID         int64
	Actor      string
	Action     string
	EntityType string
	EntityID   string
	Outcome    string
	RemoteAddr string
	Details    json.RawMessage
	CreatedAt  time.Time
}

type AuditFilter struct {
	Actor      string
	Action     string
	EntityType string
	EntityID   string
	Since      time.Time
	Until      time.Time
	BeforeID   int64
	Limit      int
}

type LogEvent struct {
	ID      int64
	Time    time.Time
	Level   string
	Message string
	Caller  string
	Fields  json.RawMessage
}

type LogFilter struct {
	Level    string
	Since    time.Time
	Until    time.Time
	BeforeID int64
	Limit    int
}

type CardPolicy struct {
	ICCID           string
	NetworkEnabled  bool
	VoWiFiEnabled   bool
	AirplaneEnabled bool
	APN             string
	IPVersion       string
	Source          string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type TrafficBucket struct {
	DeviceID    string
	Bucket      string
	PeriodStart time.Time
	RXBytes     int64
	TXBytes     int64
}

func (value TrafficBucket) TotalBytes() int64 {
	return value.RXBytes + value.TXBytes
}

type TrafficFilter struct {
	DeviceID string
	Bucket   string
	Since    time.Time
	Until    time.Time
	Limit    int
}

func normalizeJSONObject(value json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(value)) == 0 {
		return json.RawMessage(`{}`), nil
	}
	document, err := decodeJSONObject(value)
	if err != nil {
		return nil, err
	}
	normalized, err := json.Marshal(document)
	if err != nil {
		return nil, err
	}
	return normalized, nil
}

func normalizeJSONValue(value json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(value)) == 0 {
		return json.RawMessage(`null`), nil
	}
	if !json.Valid(value) {
		return nil, errors.New("invalid JSON value")
	}
	return append(json.RawMessage(nil), value...), nil
}

func decodeJSONObject(value json.RawMessage) (map[string]any, error) {
	var document map[string]any
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("JSON value must contain exactly one object")
		}
		return nil, err
	}
	if document == nil {
		return nil, errors.New("JSON value must be an object")
	}
	return document, nil
}

func redactJSONFields(value json.RawMessage, fields []string, replacement string) json.RawMessage {
	document, err := decodeJSONObject(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	for _, field := range fields {
		if getJSONPath(document, field) != nil {
			setJSONPath(document, field, replacement)
		}
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}

func mergeJSONSecrets(
	incoming json.RawMessage,
	existing json.RawMessage,
	fields []string,
) (json.RawMessage, error) {
	next, err := decodeJSONObject(incoming)
	if err != nil {
		return nil, err
	}
	current, err := decodeJSONObject(existing)
	if err != nil {
		current = map[string]any{}
	}
	for _, field := range fields {
		value := getJSONPath(next, field)
		text, stringValue := value.(string)
		if value == nil || (stringValue && (text == "" || text == SecretMask)) {
			if previous := getJSONPath(current, field); previous != nil {
				setJSONPath(next, field, previous)
			}
		}
	}
	return json.Marshal(next)
}

func getJSONPath(document map[string]any, path string) any {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	parts := strings.Split(path, ".")
	var current any = document
	for _, part := range parts {
		object, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current, ok = object[part]
		if !ok {
			return nil
		}
	}
	return current
}

func setJSONPath(document map[string]any, path string, value any) {
	if strings.TrimSpace(path) == "" {
		return
	}
	parts := strings.Split(path, ".")
	current := document
	for _, part := range parts[:len(parts)-1] {
		next, ok := current[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			current[part] = next
		}
		current = next
	}
	current[parts[len(parts)-1]] = value
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nullableBool(value *bool) any {
	if value == nil {
		return nil
	}
	return boolInt(*value)
}

func nullableInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}
