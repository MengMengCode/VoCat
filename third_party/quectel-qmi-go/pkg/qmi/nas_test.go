package qmi

import (
	"encoding/binary"
	"testing"
)

func nasTLVUint16(tlvType uint8, v uint16) TLV {
	buf := make([]byte, 2)
	binary.LittleEndian.PutUint16(buf, v)
	return TLV{Type: tlvType, Value: buf}
}

func nasTLVUint32(tlvType uint8, v uint32) TLV {
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, v)
	return TLV{Type: tlvType, Value: buf}
}

func nasTLVUint64(tlvType uint8, v uint64) TLV {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, v)
	return TLV{Type: tlvType, Value: buf}
}

func TestBuildTechnologyPreferenceTLVs(t *testing.T) {
	tlvs := buildTechnologyPreferenceTLVs(TechnologyPreference{
		ActivePreference: NASTechPreference3GPP | NASTechPreferenceLTE,
		ActiveDuration:   NASPreferenceDurationPowerCycle,
	})

	if len(tlvs) != 1 {
		t.Fatalf("expected 1 TLV, got %d", len(tlvs))
	}
	if tlvs[0].Type != 0x01 {
		t.Fatalf("unexpected TLV type: 0x%02X", tlvs[0].Type)
	}
	if got := binary.LittleEndian.Uint16(tlvs[0].Value[0:2]); got != (NASTechPreference3GPP | NASTechPreferenceLTE) {
		t.Fatalf("unexpected active preference: 0x%X", got)
	}
	if tlvs[0].Value[2] != NASPreferenceDurationPowerCycle {
		t.Fatalf("unexpected active duration: 0x%02X", tlvs[0].Value[2])
	}
}

func TestParseTechnologyPreferenceResponse(t *testing.T) {
	resp := &Packet{
		TLVs: []TLV{
			successResultTLV(),
			{Type: 0x01, Value: []byte{0x22, 0x00, NASPreferenceDurationPermanent}},
			{Type: 0x10, Value: []byte{0x20, 0x00}},
		},
	}

	info, err := parseTechnologyPreferenceResponse(resp)
	if err != nil {
		t.Fatalf("parseTechnologyPreferenceResponse returned error: %v", err)
	}
	if info.ActivePreference != 0x22 || info.ActiveDuration != NASPreferenceDurationPermanent {
		t.Fatalf("unexpected active preference: %+v", info)
	}
	if !info.HasPersistentPreference || info.PersistentPreference != 0x20 {
		t.Fatalf("unexpected persistent preference: %+v", info)
	}
}

func TestBuildNASInitiateNetworkRegisterTLVsAutomaticOmitsEmptyPLMN(t *testing.T) {
	tlvs := buildNASInitiateNetworkRegisterTLVs(NASInitiateNetworkRegisterRequest{
		Mode: NASNetworkRegisterAutomatic,
	})

	if len(tlvs) != 1 {
		t.Fatalf("expected 1 TLV, got %d", len(tlvs))
	}
	if tlvs[0].Type != 0x01 {
		t.Fatalf("unexpected TLV type: 0x%02X", tlvs[0].Type)
	}
	if got, want := tlvs[0].Value, []byte{0x01}; string(got) != string(want) {
		t.Fatalf("automatic network info TLV = % X, want % X", got, want)
	}
}

func TestBuildNASInitiateNetworkRegisterTLVsManualIncludesPLMN(t *testing.T) {
	tlvs := buildNASInitiateNetworkRegisterTLVs(NASInitiateNetworkRegisterRequest{
		Mode:             NASNetworkRegisterManual,
		MCC:              460,
		MNC:              1,
		RadioAccessTech:  0x08,
		IncludesPCSDigit: true,
	})

	if len(tlvs) != 3 {
		t.Fatalf("expected 3 TLVs, got %d", len(tlvs))
	}
	if tlvs[0].Type != 0x01 || len(tlvs[0].Value) != 1 {
		t.Fatalf("unexpected manual action TLV: %+v", tlvs[0])
	}
	if tlvs[0].Value[0] != 0x02 {
		t.Fatalf("manual network action = %d, want 2", tlvs[0].Value[0])
	}
	if tlvs[1].Type != 0x10 || len(tlvs[1].Value) != 5 {
		t.Fatalf("unexpected manual info TLV: %+v", tlvs[1])
	}
	if got := binary.LittleEndian.Uint16(tlvs[1].Value[0:2]); got != 460 {
		t.Fatalf("manual MCC = %d, want 460", got)
	}
	if got := binary.LittleEndian.Uint16(tlvs[1].Value[2:4]); got != 1 {
		t.Fatalf("manual MNC = %d, want 1", got)
	}
	if tlvs[1].Value[4] != 0x08 {
		t.Fatalf("manual RAT = 0x%02X, want 0x08", tlvs[1].Value[4])
	}
	if tlvs[2].Type != 0x12 || tlvs[2].Value[0] != 0x01 {
		t.Fatalf("unexpected PCS digit TLV: %+v", tlvs[2])
	}
}

func TestBuildNASInitiateNetworkRegisterTLVsChangeDurationUsesLibqmiTLV(t *testing.T) {
	tlvs := buildNASInitiateNetworkRegisterTLVs(NASInitiateNetworkRegisterRequest{
		Mode:              NASNetworkRegisterAutomatic,
		ChangeDuration:    0x01,
		HasChangeDuration: true,
	})

	if len(tlvs) != 2 {
		t.Fatalf("expected 2 TLVs, got %d", len(tlvs))
	}
	if tlvs[1].Type != 0x11 || tlvs[1].Value[0] != 0x01 {
		t.Fatalf("unexpected change duration TLV: %+v", tlvs[1])
	}
}

func TestNASForceNetworkSearchMessageIDMatchesLibqmi(t *testing.T) {
	if NASForceNetworkSearch != 0x0067 {
		t.Fatalf("NASForceNetworkSearch = 0x%04X, want 0x0067", NASForceNetworkSearch)
	}
}

func TestGetLTEDuplexModeFromBandInfo(t *testing.T) {
	tests := []struct {
		name string
		info *RFBandInfo
		want string
	}{
		{
			name: "lte band 8 is fdd",
			info: &RFBandInfo{Bands: []RFBandInfoEntry{{RadioInterface: 0x08, ActiveBandClass: 8}}},
			want: "FDD",
		},
		{
			name: "lte band 41 is tdd",
			info: &RFBandInfo{Bands: []RFBandInfoEntry{{RadioInterface: 0x08, ActiveBandClass: 41}}},
			want: "TDD",
		},
		{
			name: "non lte band is ignored",
			info: &RFBandInfo{Bands: []RFBandInfoEntry{{RadioInterface: 0x04, ActiveBandClass: 8}}},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetLTEDuplexModeFromBandInfo(tt.info); got != tt.want {
				t.Fatalf("GetLTEDuplexModeFromBandInfo()=%q want=%q", got, tt.want)
			}
		})
	}
}

func TestGetLTEDuplexModeFromCellLocation(t *testing.T) {
	tests := []struct {
		name string
		info *CellLocationInfo
		want string
	}{
		{
			name: "band 8 earfcn is fdd",
			info: &CellLocationInfo{LTE: &LTECellLocationInfo{EARFCN: 3740}},
			want: "FDD",
		},
		{
			name: "band 41 earfcn is tdd",
			info: &CellLocationInfo{LTE: &LTECellLocationInfo{EARFCN: 39150}},
			want: "TDD",
		},
		{
			name: "unknown earfcn stays empty",
			info: &CellLocationInfo{LTE: &LTECellLocationInfo{EARFCN: 65535}},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetLTEDuplexModeFromCellLocation(tt.info); got != tt.want {
				t.Fatalf("GetLTEDuplexModeFromCellLocation()=%q want=%q", got, tt.want)
			}
		})
	}
}

func TestParseRFBandInfoResponse(t *testing.T) {
	extended := []byte{
		0x02,
		0x04, 0x2C, 0x00, 0x64, 0x00, 0x00, 0x00,
		0x08, 0x4D, 0x00, 0x44, 0x01, 0x00, 0x00,
	}
	bandwidths := []byte{
		0x02,
		0x04, 0x14, 0x00, 0x00, 0x00,
		0x08, 0x64, 0x00, 0x00, 0x00,
	}
	resp := &Packet{
		TLVs: []TLV{
			successResultTLV(),
			{Type: 0x11, Value: extended},
			{Type: 0x12, Value: bandwidths},
		},
	}

	info, err := parseRFBandInfoResponse(resp)
	if err != nil {
		t.Fatalf("parseRFBandInfoResponse returned error: %v", err)
	}
	if len(info.Bands) != 2 {
		t.Fatalf("expected 2 band entries, got %d", len(info.Bands))
	}
	if info.Bands[0].RadioInterface != 0x04 || info.Bands[0].ActiveBandClass != 0x002C || info.Bands[0].ActiveChannel != 100 {
		t.Fatalf("unexpected first band entry: %+v", info.Bands[0])
	}
	if len(info.Bandwidths) != 2 || info.Bandwidths[1].Bandwidth != 100 {
		t.Fatalf("unexpected bandwidth entries: %+v", info.Bandwidths)
	}
}

func TestBuildSystemSelectionPreferenceTLVs(t *testing.T) {
	extLTE := [4]uint64{1, 2, 3, 4}
	pref := SystemSelectionPreference{
		ModePreference:                NASRatModePreferenceLTE | NASRatModePreferenceNR5G,
		HasModePreference:             true,
		LTEBandPreference:             0x1234,
		HasLTEBandPreference:          true,
		NetworkSelectionPreference:    NASNetworkSelectionManual,
		HasNetworkSelectionPreference: true,
		ManualNetworkSelection: ManualNetworkSelection{
			MCC:              460,
			MNC:              1,
			IncludesPCSDigit: true,
		},
		HasManualNetworkSelection:    true,
		ChangeDuration:               NASChangeDurationPermanent,
		HasChangeDuration:            true,
		ServiceDomainPreference:      NASServiceDomainPreferenceCSPS,
		HasServiceDomainPreference:   true,
		ExtendedLTEBandPreference:    extLTE,
		HasExtendedLTEBandPreference: true,
	}

	tlvs, err := buildSystemSelectionPreferenceTLVs(pref)
	if err != nil {
		t.Fatalf("buildSystemSelectionPreferenceTLVs returned error: %v", err)
	}
	if len(tlvs) != 7 {
		t.Fatalf("expected 7 TLVs, got %d", len(tlvs))
	}
	if tlvs[0].Type != 0x11 || binary.LittleEndian.Uint16(tlvs[0].Value) != (NASRatModePreferenceLTE|NASRatModePreferenceNR5G) {
		t.Fatalf("unexpected mode preference TLV: %+v", tlvs[0])
	}
	if tlvs[2].Type != 0x16 || len(tlvs[2].Value) != 5 {
		t.Fatalf("unexpected network selection TLV: %+v", tlvs[2])
	}
	if tlvs[2].Value[0] != NASNetworkSelectionManual {
		t.Fatalf("expected manual selection mode, got %d", tlvs[2].Value[0])
	}
	if got := binary.LittleEndian.Uint16(tlvs[2].Value[1:3]); got != 460 {
		t.Fatalf("unexpected MCC: %d", got)
	}
	if got := binary.LittleEndian.Uint16(tlvs[2].Value[3:5]); got != 1 {
		t.Fatalf("unexpected MNC: %d", got)
	}
	if tlvs[5].Type != 0x1A || tlvs[5].Value[0] != 0x01 {
		t.Fatalf("unexpected PCS digit TLV: %+v", tlvs[5])
	}
	if tlvs[6].Type != 0x24 || len(tlvs[6].Value) != 32 {
		t.Fatalf("unexpected extended LTE band TLV: %+v", tlvs[6])
	}
}

func TestBuildSystemSelectionPreferenceTLVsAutomaticNetworkSelectionUsesLibqmiSequence(t *testing.T) {
	tlvs, err := buildSystemSelectionPreferenceTLVs(SystemSelectionPreference{
		NetworkSelectionPreference:    NASNetworkSelectionAutomatic,
		HasNetworkSelectionPreference: true,
	})
	if err != nil {
		t.Fatalf("buildSystemSelectionPreferenceTLVs returned error: %v", err)
	}
	if len(tlvs) != 1 {
		t.Fatalf("expected 1 TLV, got %d", len(tlvs))
	}
	if tlvs[0].Type != 0x16 {
		t.Fatalf("unexpected TLV type: 0x%02X", tlvs[0].Type)
	}
	if got, want := tlvs[0].Value, []byte{NASNetworkSelectionAutomatic, 0x00, 0x00, 0x00, 0x00}; string(got) != string(want) {
		t.Fatalf("automatic network selection TLV = % X, want % X", got, want)
	}
}

func TestParseSystemSelectionPreferenceResponse(t *testing.T) {
	extended := make([]byte, 32)
	for i, v := range []uint64{10, 20, 30, 40} {
		binary.LittleEndian.PutUint64(extended[i*8:(i+1)*8], v)
	}
	resp := &Packet{
		TLVs: []TLV{
			successResultTLV(),
			{Type: 0x10, Value: []byte{0x01}},
			nasTLVUint16(0x11, NASRatModePreferenceLTE|NASRatModePreferenceNR5G),
			nasTLVUint64(0x15, 0x1234),
			{Type: 0x16, Value: []byte{NASNetworkSelectionManual}},
			nasTLVUint32(0x18, NASServiceDomainPreferenceCSPS),
			{Type: 0x1B, Value: []byte{0xCC, 0x01, 0x01, 0x00, 0x01}},
			{Type: 0x1C, Value: []byte{0x03, 0x04, 0x08, 0x0C}},
			nasTLVUint32(0x1F, 0x77),
			nasTLVUint32(0x20, 0x55),
			nasTLVUint16(0x22, 0x99),
			{Type: 0x23, Value: extended},
		},
	}

	info, err := parseSystemSelectionPreferenceResponse(resp)
	if err != nil {
		t.Fatalf("parseSystemSelectionPreferenceResponse returned error: %v", err)
	}
	if !info.HasEmergencyMode || !info.EmergencyMode {
		t.Fatalf("unexpected emergency mode: %+v", info)
	}
	if !info.HasModePreference || info.ModePreference != (NASRatModePreferenceLTE|NASRatModePreferenceNR5G) {
		t.Fatalf("unexpected mode preference: %+v", info)
	}
	if !info.HasLTEBandPreference || info.LTEBandPreference != 0x1234 {
		t.Fatalf("unexpected LTE band preference: %+v", info)
	}
	if !info.HasManualNetworkSelection || info.ManualNetworkSelection.MCC != 460 || info.ManualNetworkSelection.MNC != 1 || !info.ManualNetworkSelection.IncludesPCSDigit {
		t.Fatalf("unexpected manual network selection: %+v", info.ManualNetworkSelection)
	}
	if len(info.AcquisitionOrderPreference) != 3 || info.AcquisitionOrderPreference[2] != 0x0C {
		t.Fatalf("unexpected acquisition order preference: %+v", info.AcquisitionOrderPreference)
	}
	if !info.HasExtendedLTEBandPreference || info.ExtendedLTEBandPreference[3] != 40 {
		t.Fatalf("unexpected extended LTE band preference: %+v", info.ExtendedLTEBandPreference)
	}
}

func TestDecodeBCDPLMN(t *testing.T) {
	mcc, mnc := decodeBCDPLMN([]byte{0x13, 0x00, 0x62})
	if mcc != "310" || mnc != "260" {
		t.Fatalf("unexpected 3-digit MNC decode: %s/%s", mcc, mnc)
	}

	mcc, mnc = decodeBCDPLMN([]byte{0x64, 0xF0, 0x10})
	if mcc != "460" || mnc != "01" {
		t.Fatalf("unexpected 2-digit MNC decode: %s/%s", mcc, mnc)
	}
}

func TestParseCellLocationInfoResponse(t *testing.T) {
	lteTLV := []byte{
		0x01,
		0x13, 0x00, 0x62,
		0x64, 0x00,
		0x78, 0x56, 0x34, 0x12,
		0xA4, 0x01,
		0x21, 0x00,
		0x07, 0x08, 0x09, 0x0A,
	}
	nrTLV := make([]byte, 22)
	rsrq := int16(-11)
	rsrp := int16(-95)
	snr := int16(25)
	copy(nrTLV[0:3], []byte{0x13, 0x00, 0x62})
	copy(nrTLV[3:6], []byte{0x00, 0x01, 0x02})
	binary.LittleEndian.PutUint64(nrTLV[6:14], 0x1122334455667788)
	binary.LittleEndian.PutUint16(nrTLV[14:16], 321)
	binary.LittleEndian.PutUint16(nrTLV[16:18], uint16(rsrq))
	binary.LittleEndian.PutUint16(nrTLV[18:20], uint16(rsrp))
	binary.LittleEndian.PutUint16(nrTLV[20:22], uint16(snr))

	resp := &Packet{
		TLVs: []TLV{
			successResultTLV(),
			{Type: 0x13, Value: lteTLV},
			nasTLVUint32(0x1E, 42),
			nasTLVUint32(0x2E, 635334),
			{Type: 0x2F, Value: nrTLV},
		},
	}

	info, err := parseCellLocationInfoResponse(resp)
	if err != nil {
		t.Fatalf("parseCellLocationInfoResponse returned error: %v", err)
	}
	if info.LTE == nil || info.LTE.MCC != "310" || info.LTE.MNC != "260" || info.LTE.TAC != 100 || info.LTE.GlobalCellID != 0x12345678 {
		t.Fatalf("unexpected LTE cell info: %+v", info.LTE)
	}
	if !info.LTE.HasTimingAdvance || info.LTE.TimingAdvance != 42 {
		t.Fatalf("unexpected LTE timing advance: %+v", info.LTE)
	}
	if info.NR5G == nil || !info.NR5G.HasARFCN || info.NR5G.ARFCN != 635334 {
		t.Fatalf("unexpected NR ARFCN: %+v", info.NR5G)
	}
	if info.NR5G.TAC != 258 || info.NR5G.GlobalCellID != 0x1122334455667788 || info.NR5G.PhysicalCellID != 321 {
		t.Fatalf("unexpected NR cell info: %+v", info.NR5G)
	}
}

func TestParseServingSystemIndication(t *testing.T) {
	packet := &Packet{
		TLVs: []TLV{
			{Type: 0x01, Value: []byte{byte(RegStateRegistered), 0x00, 0x01, 0x00, 0x01, 0x04}},
			{Type: 0x10, Value: []byte{0x01}},
			{Type: 0x12, Value: []byte{0xCC, 0x01, 0x01, 0x00}},
		},
	}

	info, err := ParseServingSystemIndication(packet)
	if err != nil {
		t.Fatalf("ParseServingSystemIndication returned error: %v", err)
	}
	if info.RegistrationState != RegStateRegistered || !info.PSAttached || info.RadioInterface != 0x04 {
		t.Fatalf("unexpected serving system indication payload: %+v", info)
	}
	if info.MCC != 460 || info.MNC != 1 {
		t.Fatalf("unexpected PLMN decode: %+v", info)
	}
}

func TestParseNetworkTimeResponse(t *testing.T) {
	resp := &Packet{
		TLVs: []TLV{
			successResultTLV(),
			{Type: 0x10, Value: []byte{0xE8, 0x07, 0x04, 0x07, 0x0A, 0x1E, 0x2D, 0x02, 0x08, 0x01, 0x04}},
			{Type: 0x11, Value: []byte{0xE8, 0x07, 0x04, 0x07, 0x0B, 0x1F, 0x2E, 0x02, 0x08, 0x00, 0x08}},
		},
	}

	info, err := parseNetworkTimeResponse(resp)
	if err != nil {
		t.Fatalf("parseNetworkTimeResponse returned error: %v", err)
	}
	if !info.HasThreeGPP2 || info.ThreeGPP2.Year != 2024 || info.ThreeGPP2.Hour != 10 || info.ThreeGPP2.RadioInterface != 0x04 {
		t.Fatalf("unexpected 3GPP2 time: %+v", info.ThreeGPP2)
	}
	if !info.HasThreeGPP || info.ThreeGPP.Minute != 31 || info.ThreeGPP.DaylightSavingsAdjustment != 0 || info.ThreeGPP.RadioInterface != 0x08 {
		t.Fatalf("unexpected 3GPP time: %+v", info.ThreeGPP)
	}
}

func TestParseNetworkTimeIndicationSplitTLVs(t *testing.T) {
	packet := &Packet{
		TLVs: []TLV{
			{Type: 0x01, Value: []byte{0xEA, 0x07, 0x05, 0x15, 0x14, 0x0D, 0x22, 0x04}},
			{Type: 0x10, Value: []byte{0x20}},
			{Type: 0x11, Value: []byte{0x01}},
			{Type: 0x12, Value: []byte{0x08}},
		},
	}

	info, err := ParseNetworkTimeIndication(packet)
	if err != nil {
		t.Fatalf("ParseNetworkTimeIndication returned error: %v", err)
	}
	if !info.HasThreeGPP {
		t.Fatal("expected 3GPP network time")
	}
	got := info.ThreeGPP
	if got.Year != 2026 || got.Month != 5 || got.Day != 21 || got.Hour != 20 || got.Minute != 13 || got.Second != 34 || got.DayOfWeek != 4 {
		t.Fatalf("unexpected universal time: %+v", got)
	}
	if got.TimezoneOffsetQuarters != 32 || got.DaylightSavingsAdjustment != 1 || got.RadioInterface != 0x08 {
		t.Fatalf("unexpected optional time fields: %+v", got)
	}
}

func TestBuildNASRegisterIndicationsTLVs(t *testing.T) {
	tlvs := buildNASRegisterIndicationsTLVs(NASIndicationRegistration{
		ServingSystemChanged:   true,
		SystemInfo:             true,
		NetworkTime:            true,
		SignalInfo:             true,
		OperatorName:           true,
		NetworkReject:          true,
		IncrementalNetworkScan: true,
	})
	if len(tlvs) != 7 {
		t.Fatalf("expected 7 TLVs, got %d", len(tlvs))
	}
	if tlvs[0].Type != 0x10 || tlvs[1].Type != 0x13 || tlvs[2].Type != 0x17 {
		t.Fatalf("unexpected register indication TLV layout: %+v", tlvs)
	}
}

func TestParseOperatorNameIndication(t *testing.T) {
	packet := &Packet{
		TLVs: []TLV{
			{Type: 0x10, Value: []byte{0x00, 'C', 'a', 'r', 'r', 'i', 'e', 'r'}},
			{Type: 0x13, Value: []byte("Carrier LTE")},
		},
	}
	info, err := ParseOperatorNameIndication(packet)
	if err != nil {
		t.Fatalf("ParseOperatorNameIndication returned error: %v", err)
	}
	if info.ServiceProviderName != "Carrier" || info.OperatorStringName != "Carrier LTE" {
		t.Fatalf("unexpected operator name info: %+v", info)
	}
}

func TestParseNetworkRejectIndication(t *testing.T) {
	packet := &Packet{
		TLVs: []TLV{
			{Type: 0x10, Value: []byte{0x08, 0x15, 0x00, 0x00, 0x00}},
			{Type: 0x11, Value: []byte("46001")},
		},
	}
	info, err := ParseNetworkRejectIndication(packet)
	if err != nil {
		t.Fatalf("ParseNetworkRejectIndication returned error: %v", err)
	}
	if info.RadioInterface != 0x08 || info.RejectCause != 21 || info.PLMN != "46001" {
		t.Fatalf("unexpected network reject payload: %+v", info)
	}
}

func TestParseIncrementalNetworkScanIndication(t *testing.T) {
	packet := &Packet{
		TLVs: []TLV{
			{Type: 0x10, Value: []byte{0x01, 0x00, 0xCC, 0x01, 0x01, 0x00, 0x02, 0x03, 'C', 'M', 'C'}},
			{Type: 0x11, Value: []byte{0x01, 0x00, 0xCC, 0x01, 0x01, 0x00, 0x08}},
			{Type: 0x12, Value: []byte{0x01}},
		},
	}
	info, err := ParseIncrementalNetworkScanIndication(packet)
	if err != nil {
		t.Fatalf("ParseIncrementalNetworkScanIndication returned error: %v", err)
	}
	if !info.ScanComplete || len(info.Results) != 1 {
		t.Fatalf("unexpected incremental scan payload: %+v", info)
	}
	if got := info.Results[0]; got.MCC != "460" || got.MNC != "001" || len(got.RATs) != 1 || got.RATs[0] != 0x08 {
		t.Fatalf("unexpected incremental scan entry: %+v", got)
	}
}
