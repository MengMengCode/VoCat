package device

import (
	"errors"
	"time"

	"vocat/internal/modem"
)

var (
	ErrNotStarted              = errors.New("device manager is not started")
	ErrNotFound                = errors.New("device not found")
	ErrNoATPort                = errors.New("device has no usable AT port")
	ErrUnsupportedCapability   = errors.New("device does not support this capability")
	ErrSMSPromptUnsupported    = errors.New("device AT client does not support SMS prompt mode")
	ErrSMSInvalidRecipient     = errors.New("invalid SMS recipient")
	ErrSMSEmpty                = errors.New("SMS text is empty")
	ErrSMSTooLong              = errors.New("SMS exceeds one-message encoding limit")
	ErrSMSReferenceMissing     = errors.New("modem completed SMS command without a message reference")
	ErrSMSInvalidMessageIndex  = errors.New("invalid SMS message index")
	ErrSMSSubscriberIdentity   = errors.New("live SMS subscriber identity is unavailable")
	ErrSMSTransportUnavailable = errors.New("SMS transport is unavailable")
	ErrDataBackendUnavailable  = errors.New("cellular data backend is unavailable")
	ErrInvalidNetworkAPN       = errors.New("invalid cellular APN")
	ErrRegionBlocked           = errors.New("sim card home region is not served")
	ErrUSSDSessionNotFound     = errors.New("ussd session not found or already closed")
)

type NetworkRequest struct {
	Enabled        bool   `json:"enabled"`
	APN            string `json:"apn"`
	IPVersion      string `json:"ipVersion"`
	Username       string `json:"username,omitempty"`
	Password       string `json:"password,omitempty"`
	Authentication string `json:"authentication,omitempty"`
	Backend        string `json:"backend,omitempty"`
}

type NetworkResult struct {
	Enabled       bool   `json:"enabled"`
	Backend       string `json:"backend"`
	Interface     string `json:"interface,omitempty"`
	ControlDevice string `json:"controlDevice,omitempty"`
	APN           string `json:"apn,omitempty"`
	IPVersion     string `json:"ipVersion,omitempty"`
	Detail        string `json:"detail,omitempty"`
}

type USBNetMode struct {
	Mode int    `json:"mode"`
	Name string `json:"name"`
}

type OperatorSelection struct {
	Mode             int    `json:"mode"`
	Format           int    `json:"format"`
	Operator         string `json:"operator"`
	AccessTechnology string `json:"accessTechnology,omitempty"`
}

type Device struct {
	ID          string          `json:"id"`
	Candidate   modem.Candidate `json:"candidate"`
	Snapshot    *Snapshot       `json:"snapshot,omitempty"`
	LastError   string          `json:"lastError,omitempty"`
	Discovered  bool            `json:"discovered"`
	Recovering  bool            `json:"recovering,omitempty"`
	LastUpdated time.Time       `json:"lastUpdated,omitempty"`
}

type PhoneNumber struct {
	Number string `json:"number"`
	Source string `json:"source"`
	Status string `json:"status"`
}

const (
	PhoneSourceCNUM         = "at_cnum"
	PhoneSourceOwnNumber    = "sim_own_number"
	PhoneSourceEFMSISDN     = "usim_ef_msisdn"
	PhoneSourceQMIDMSMSISDN = "qmi_dms_msisdn"
)

type Snapshot struct {
	DeviceID           string      `json:"deviceId"`
	Port               string      `json:"port"`
	Responsive         bool        `json:"responsive"`
	Manufacturer       string      `json:"manufacturer"`
	Model              string      `json:"model"`
	Firmware           string      `json:"firmware"`
	SIMStatus          string      `json:"simStatus"`
	SIMReady           bool        `json:"simReady"`
	SIMChanged         bool        `json:"simChanged,omitempty"`
	SignalRaw          *int        `json:"signalRaw,omitempty"`
	SignalPercent      *int        `json:"signalPercent,omitempty"`
	RSSIDBm            *int        `json:"rssiDbm,omitempty"`
	RSRP               *int        `json:"rsrp,omitempty"`
	RSRQ               *int        `json:"rsrq,omitempty"`
	SINR               *int        `json:"sinr,omitempty"`
	AccessTech         string      `json:"accessTech"`
	Band               string      `json:"band"`
	Channel            string      `json:"channel"`
	OperatorName       string      `json:"operatorName"`
	OperatorCode       string      `json:"operatorCode"`
	RegistrationStatus int         `json:"registrationStatus"`
	RegistrationSource string      `json:"registrationSource"`
	PSAttached         bool        `json:"psAttached"`
	IMEI               string      `json:"imei"`
	ICCID              string      `json:"iccid"`
	IMSI               string      `json:"imsi"`
	SPN                string      `json:"spn,omitempty"`
	OperatingMode      int         `json:"operatingMode"`
	ModeKnown          bool        `json:"modeKnown"`
	FlightMode         bool        `json:"flightMode"`
	RadioOff           bool        `json:"radioOff"`
	Phone              PhoneNumber `json:"phone"`
	Warnings           []string    `json:"warnings,omitempty"`
	UpdatedAt          time.Time   `json:"updatedAt"`
}

type USSDResult struct {
	Code string `json:"code"`
	Text string `json:"text"`
	Raw  string `json:"raw"`
	DCS  *int   `json:"dcs,omitempty"`
	// Status describes the dialog state: "final", "awaiting_input",
	// "terminated", or "failed".
	Status string `json:"status,omitempty"`
	// SessionID identifies an open dialog when Status is "awaiting_input".
	SessionID string `json:"sessionId,omitempty"`
	// Continueable reports whether the network expects more input on SessionID.
	Continueable bool `json:"continueable,omitempty"`
}

type FlightResult struct {
	PreviousMode int  `json:"previousMode"`
	CurrentMode  int  `json:"currentMode"`
	Changed      bool `json:"changed"`
	FlightMode   bool `json:"flightMode"`
	RadioOff     bool `json:"radioOff"`
}

type SMSEncoding string

const (
	// Cellular SMS transports name VoCAT's host-side modem backend.
	SMSTransportCellularAT  = "cellular_at"
	SMSTransportCellularQMI = "cellular_qmi"
)

const (
	SMSEncodingGSM7Text SMSEncoding = "gsm7_text"
	SMSEncodingGSM7PDU  SMSEncoding = "gsm7_pdu"
	SMSEncodingUCS2PDU  SMSEncoding = "ucs2_pdu"
	SMSEncoding8BitPDU  SMSEncoding = "8bit_pdu"
	SMSEncodingUnknown  SMSEncoding = "unknown"
)

type SMSStorageStatus string

const (
	SMSStatusReceivedUnread SMSStorageStatus = "received_unread"
	SMSStatusReceivedRead   SMSStorageStatus = "received_read"
	SMSStatusStoredUnsent   SMSStorageStatus = "stored_unsent"
	SMSStatusStoredSent     SMSStorageStatus = "stored_sent"
	SMSStatusUnknown        SMSStorageStatus = "unknown"
)

type SMSDirection string

const (
	SMSDirectionReceived     SMSDirection = "received"
	SMSDirectionSubmitted    SMSDirection = "submitted"
	SMSDirectionStatusReport SMSDirection = "status_report"
	SMSDirectionUnknown      SMSDirection = "unknown"
)

type SMSConcatInfo struct {
	Reference int `json:"reference"`
	Total     int `json:"total"`
	Sequence  int `json:"sequence"`
}

// SMSSubmitTPDU is one modem-independent SMS-SUBMIT transfer unit. TPDU does
// not include the SMSC-length octet used by AT+CMGS PDU mode, so it can be
// embedded directly in an RP-DATA message for SMS over IMS.
type SMSSubmitTPDU struct {
	To              string
	Encoding        SMSEncoding
	TPDU            []byte
	Part            int
	Total           int
	ConcatReference *int
}

type SMSMessage struct {
	Index                  int              `json:"index"`
	Storage                string           `json:"storage,omitempty"`
	Transport              string           `json:"transport,omitempty"`
	StorageStatus          SMSStorageStatus `json:"storageStatus"`
	Direction              SMSDirection     `json:"direction"`
	From                   string           `json:"from,omitempty"`
	To                     string           `json:"to,omitempty"`
	ServiceCenter          string           `json:"serviceCenter,omitempty"`
	Text                   string           `json:"text"`
	Encoding               SMSEncoding      `json:"encoding"`
	ServiceCenterTimestamp *time.Time       `json:"serviceCenterTimestamp,omitempty"`
	DischargeTimestamp     *time.Time       `json:"dischargeTimestamp,omitempty"`
	MessageReference       *int             `json:"messageReference,omitempty"`
	StatusCode             *int             `json:"statusCode,omitempty"`
	DeliveryStatus         string           `json:"deliveryStatus,omitempty"`
	Concat                 *SMSConcatInfo   `json:"concat,omitempty"`
	ProtocolID             int              `json:"protocolId"`
	DataCodingScheme       int              `json:"dataCodingScheme"`
	ModemLength            int              `json:"modemLength"`
	RawPDU                 string           `json:"rawPdu"`
	RawUserData            string           `json:"rawUserData,omitempty"`
	DecodeError            string           `json:"decodeError,omitempty"`
}

type SMSPartResult struct {
	Part             int       `json:"part"`
	Total            int       `json:"total"`
	MessageReference int       `json:"messageReference"`
	ReferenceKnown   bool      `json:"referenceKnown"`
	AcceptedByModem  bool      `json:"acceptedByModem"`
	SubmissionStatus string    `json:"submissionStatus"`
	ModemFinal       string    `json:"modemFinal"`
	ModemEvidence    []string  `json:"modemEvidence"`
	SubmittedAt      time.Time `json:"submittedAt"`
}

// SMSSubscriberIdentity identifies the SIM that was active for an atomic
// cellular SMS submission. Both values are read directly from the modem while
// the device operation lock is held.
type SMSSubscriberIdentity struct {
	ICCID string `json:"iccid"`
	IMSI  string `json:"imsi"`
}

// SMSSubscriberScan binds one modem-storage snapshot to the SIM identity that
// was active for the entire scan. Storages records every successfully listed
// and fully read area, including empty ones, so callers can establish a safe
// storage baseline.
type SMSSubscriberScan struct {
	Messages  []SMSMessage          `json:"messages"`
	Identity  SMSSubscriberIdentity `json:"identity"`
	Storages  []string              `json:"storages"`
	Transport string                `json:"transport,omitempty"`
}

// SMSSendResult proves only that the modem accepted the submit request. A
// +CMGS message reference is not a network delivery receipt.
type SMSSendResult struct {
	To                string          `json:"to"`
	Transport         string          `json:"transport,omitempty"`
	Encoding          SMSEncoding     `json:"encoding"`
	MessageReference  int             `json:"messageReference"`
	ReferenceKnown    bool            `json:"referenceKnown"`
	AcceptedByModem   bool            `json:"acceptedByModem"`
	DeliveryConfirmed bool            `json:"deliveryConfirmed"`
	SubmissionStatus  string          `json:"submissionStatus"`
	DeliveryStatus    string          `json:"deliveryStatus"`
	ModemFinal        string          `json:"modemFinal"`
	ModemEvidence     []string        `json:"modemEvidence"`
	SubmittedAt       time.Time       `json:"submittedAt"`
	PartsTotal        int             `json:"partsTotal"`
	PartsAttempted    int             `json:"partsAttempted"`
	PartsAccepted     int             `json:"partsAccepted"`
	AllPartsAccepted  bool            `json:"allPartsAccepted"`
	ConcatReference   *int            `json:"concatReference,omitempty"`
	PartResults       []SMSPartResult `json:"partResults"`
}
