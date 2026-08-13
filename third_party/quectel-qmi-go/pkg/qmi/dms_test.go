package qmi

import (
	"encoding/binary"
	"testing"
)

func dmsTLVUint64(tlvType uint8, v uint64) TLV {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, v)
	return TLV{Type: tlvType, Value: buf}
}

func TestParseBandCapabilitiesResponse(t *testing.T) {
	extendedLTE := []byte{0x02, 0x00, 0x29, 0x00, 0x4E, 0x00}
	nr5g := []byte{0x01, 0x00, 0x01, 0x01}
	resp := &Packet{
		TLVs: []TLV{
			successResultTLV(),
			dmsTLVUint64(0x01, 0x1234),
			dmsTLVUint64(0x10, 0x5678),
			{Type: 0x12, Value: extendedLTE},
			{Type: 0x13, Value: nr5g},
		},
	}

	info, err := parseBandCapabilitiesResponse(resp)
	if err != nil {
		t.Fatalf("parseBandCapabilitiesResponse returned error: %v", err)
	}
	if !info.HasBandCapability || info.BandCapability != 0x1234 {
		t.Fatalf("unexpected band capability: %+v", info)
	}
	if !info.HasLTEBandCapability || info.LTEBandCapability != 0x5678 {
		t.Fatalf("unexpected LTE band capability: %+v", info)
	}
	if len(info.ExtendedLTEBandCapability) != 2 || info.ExtendedLTEBandCapability[1] != 78 {
		t.Fatalf("unexpected extended LTE bands: %+v", info.ExtendedLTEBandCapability)
	}
	if len(info.NR5GBandCapability) != 1 || info.NR5GBandCapability[0] != 257 {
		t.Fatalf("unexpected NR5G bands: %+v", info.NR5GBandCapability)
	}
}

func TestParseDeviceCapabilitiesResponse(t *testing.T) {
	infoValue := []byte{
		0x20, 0x03, 0x00, 0x00,
		0x40, 0x06, 0x00, 0x00,
		DMSDataServiceCapabilityPS,
		DMSSIMCapabilitySupported,
		0x03,
		DMSRadioInterfaceGSM,
		DMSRadioInterfaceLTE,
		DMSRadioInterfaceNR5G,
	}
	resp := &Packet{
		TLVs: []TLV{
			successResultTLV(),
			{Type: 0x01, Value: infoValue},
		},
	}

	info, err := parseDeviceCapabilitiesResponse(resp)
	if err != nil {
		t.Fatalf("parseDeviceCapabilitiesResponse returned error: %v", err)
	}
	if info.MaxTxChannelRate != 800 || info.MaxRxChannelRate != 1600 {
		t.Fatalf("unexpected channel rates: %+v", info)
	}
	if info.DataServiceCapability != DMSDataServiceCapabilityPS || info.SIMCapability != DMSSIMCapabilitySupported {
		t.Fatalf("unexpected capabilities: %+v", info)
	}
	if len(info.RadioInterfaces) != 3 || info.RadioInterfaces[2] != DMSRadioInterfaceNR5G {
		t.Fatalf("unexpected radio interfaces: %+v", info.RadioInterfaces)
	}
}

func TestParsePowerStateResponse(t *testing.T) {
	resp := &Packet{
		TLVs: []TLV{
			successResultTLV(),
			{Type: 0x01, Value: []byte{DMSPowerStateExternalSource | DMSPowerStateBatteryCharging, 85}},
		},
	}

	info, err := parsePowerStateResponse(resp)
	if err != nil {
		t.Fatalf("parsePowerStateResponse returned error: %v", err)
	}
	if info.BatteryLevel != 85 || !info.ExternalSource || !info.BatteryCharging || info.BatteryConnected || info.PowerFaultDetected {
		t.Fatalf("unexpected power state: %+v", info)
	}
}

func TestParseTimeResponse(t *testing.T) {
	deviceTime := []byte{
		0x78, 0x56, 0x34, 0x12, 0xEF, 0xCD,
		0x02, 0x00,
	}
	systemTime := make([]byte, 8)
	userTime := make([]byte, 8)
	binary.LittleEndian.PutUint64(systemTime, 123456789)
	binary.LittleEndian.PutUint64(userTime, 987654321)
	resp := &Packet{
		TLVs: []TLV{
			successResultTLV(),
			{Type: 0x01, Value: deviceTime},
			{Type: 0x10, Value: systemTime},
			{Type: 0x11, Value: userTime},
		},
	}

	info, err := parseTimeResponse(resp)
	if err != nil {
		t.Fatalf("parseTimeResponse returned error: %v", err)
	}
	if info.DeviceTimeCount != 0xCDEF12345678 {
		t.Fatalf("unexpected device time count: 0x%X", info.DeviceTimeCount)
	}
	if info.TimeSource != DMSTimeSourceHDRNetwork {
		t.Fatalf("unexpected time source: %d", info.TimeSource)
	}
	if !info.HasSystemTime || info.SystemTime != 123456789 || !info.HasUserTime || info.UserTime != 987654321 {
		t.Fatalf("unexpected system/user time: %+v", info)
	}
}

func TestParsePRLVersionResponse(t *testing.T) {
	resp := &Packet{
		TLVs: []TLV{
			successResultTLV(),
			{Type: 0x01, Value: []byte{0x34, 0x12}},
			{Type: 0x10, Value: []byte{0x01}},
		},
	}

	info, err := parsePRLVersionResponse(resp)
	if err != nil {
		t.Fatalf("parsePRLVersionResponse returned error: %v", err)
	}
	if info.Version != 0x1234 || !info.HasPRLOnlyPreference || !info.PRLOnlyPreference {
		t.Fatalf("unexpected PRL version info: %+v", info)
	}
}

func TestParseActivationStateResponse(t *testing.T) {
	resp := &Packet{
		TLVs: []TLV{
			successResultTLV(),
			{Type: 0x01, Value: []byte{0x01, 0x00}},
		},
	}

	state, err := parseActivationStateResponse(resp)
	if err != nil {
		t.Fatalf("parseActivationStateResponse returned error: %v", err)
	}
	if state != ActivationStateActivated {
		t.Fatalf("unexpected activation state: %v", state)
	}
	if state.String() != "activated" {
		t.Fatalf("unexpected activation state string: %s", state.String())
	}
}

func TestParseUserLockStateResponse(t *testing.T) {
	resp := &Packet{
		TLVs: []TLV{
			successResultTLV(),
			{Type: 0x01, Value: []byte{0x01}},
		},
	}

	enabled, err := parseUserLockStateResponse(resp)
	if err != nil {
		t.Fatalf("parseUserLockStateResponse returned error: %v", err)
	}
	if !enabled {
		t.Fatal("expected user lock to be enabled")
	}
}

func TestParseMACAddressResponse(t *testing.T) {
	resp := &Packet{
		TLVs: []TLV{
			successResultTLV(),
			{Type: 0x10, Value: []byte{0x06, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF}},
		},
	}

	info, err := parseMACAddressResponse(resp, DMSMACTypeWLAN)
	if err != nil {
		t.Fatalf("parseMACAddressResponse returned error: %v", err)
	}
	if info.Type != DMSMACTypeWLAN {
		t.Fatalf("unexpected MAC type: %+v", info)
	}
	if len(info.Address) != 6 || info.Address[0] != 0xAA || info.Address[5] != 0xFF {
		t.Fatalf("unexpected MAC bytes: %+v", info.Address)
	}
	if info.AddressString != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("unexpected MAC string: %s", info.AddressString)
	}
}

func TestParsePrefixedBytesResponse(t *testing.T) {
	resp := &Packet{
		TLVs: []TLV{
			successResultTLV(),
			{Type: 0x01, Value: []byte{0x03, 0x00, 0xDE, 0xAD, 0xBE}},
		},
	}

	data, err := parsePrefixedBytesResponse(resp, 0x01, 2, "read user data")
	if err != nil {
		t.Fatalf("parsePrefixedBytesResponse returned error: %v", err)
	}
	if len(data) != 3 || data[2] != 0xBE {
		t.Fatalf("unexpected prefixed bytes: %+v", data)
	}
}

func TestParsePrefixedBytesResponseTruncated(t *testing.T) {
	resp := &Packet{
		TLVs: []TLV{
			successResultTLV(),
			{Type: 0x10, Value: []byte{0x06, 0xAA, 0xBB}},
		},
	}

	if _, err := parsePrefixedBytesResponse(resp, 0x10, 1, "get MAC address"); err == nil {
		t.Fatal("expected truncated prefixed-bytes error, got nil")
	}
}

func TestParseUint48LE(t *testing.T) {
	if got := parseUint48LE([]byte{0x78, 0x56, 0x34, 0x12, 0xEF, 0xCD}); got != 0xCDEF12345678 {
		t.Fatalf("unexpected uint48 decode: 0x%X", got)
	}
}

func TestParseUint16ArrayTruncated(t *testing.T) {
	if _, err := parseUint16Array([]byte{0x02, 0x00, 0x01}); err == nil {
		t.Fatal("expected truncated uint16 array error, got nil")
	}
}
