package qmi

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"
)

func newUIMUnitTestClient() *Client {
	return &Client{
		eventCh:            make(chan Event, 1),
		indicationInCh:     make(chan Event, 1),
		writeCh:            make(chan writeRequest, 16),
		closeCh:            make(chan struct{}),
		transactions:       make(map[uint32]*transactionEntry),
		recentTransactions: make(map[uint32]recentTransaction),
		opts:               DefaultClientOptions(),
	}
}

func serveUIMUnitTestRequests(t *testing.T, c *Client, handler func(*Packet) *Packet) func() {
	t.Helper()
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for {
			select {
			case <-done:
				return
			case wr := <-c.writeCh:
				req, err := UnmarshalPacket(wr.data)
				if err != nil {
					t.Errorf("failed to unmarshal request: %v", err)
					wr.result <- err
					continue
				}
				wr.result <- nil
				resp := handler(req)
				if resp == nil {
					continue
				}
				key := uint32(req.ServiceType)<<16 | uint32(req.TransactionID)
				c.mu.Lock()
				entry := c.transactions[key]
				c.mu.Unlock()
				if entry == nil {
					t.Errorf("response channel not found for key=0x%08x", key)
					continue
				}
				resp.ServiceType = req.ServiceType
				resp.ClientID = req.ClientID
				resp.TransactionID = req.TransactionID
				resp.MessageID = req.MessageID
				entry.ch <- resp
			}
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}

func requestFilePath(req *Packet) []byte {
	tlv := FindTLV(req.TLVs, 0x02)
	if tlv == nil || len(tlv.Value) < 3 {
		return nil
	}
	pathLen := int(tlv.Value[2])
	if len(tlv.Value) < 3+pathLen {
		return nil
	}
	return append([]byte(nil), tlv.Value[3:3+pathLen]...)
}

func requestRecordNumber(req *Packet) uint16 {
	tlv := FindTLV(req.TLVs, 0x03)
	if tlv == nil || len(tlv.Value) < 2 {
		return 0
	}
	return binary.LittleEndian.Uint16(tlv.Value[0:2])
}

func cardStatusPacketWithUSIMAID(aid []byte) *Packet {
	return cardStatusPacketWithApps(cardStatusTestApp{appType: UIMAppTypeUSIM, aid: aid})
}

func cardStatusPacketWithISIMAID(aid []byte) *Packet {
	return cardStatusPacketWithApps(cardStatusTestApp{appType: UIMAppTypeISIM, aid: aid})
}

type cardStatusTestApp struct {
	appType uint8
	aid     []byte
}

func cardStatusPacketWithApps(apps ...cardStatusTestApp) *Packet {
	capHint := 15
	for _, app := range apps {
		capHint += 14 + len(app.aid)
	}
	value := make([]byte, 15, capHint)
	value[8] = 1                        // number of slots
	value[9] = 0x01                     // card present
	value[10] = byte(PINStatusDisabled) // UPIN state
	value[14] = byte(len(apps))         // number of applications
	for _, app := range apps {
		value = append(value,
			app.appType, // app type
			0x01,        // app state
			0x00,        // personalization state
			0x00,        // personalization feature
			0x00,        // personalization retries
			0x00,        // personalization unblock retries
			byte(len(app.aid)),
		)
		value = append(value, app.aid...)
		value = append(value,
			0x00,                    // UPIN not used
			byte(PINStatusDisabled), // PIN1 state
			0x03,                    // PIN1 retries
			0x0A,                    // PUK1 retries
			byte(PINStatusDisabled), // PIN2 state
			0x03,                    // PIN2 retries
			0x0A,                    // PUK2 retries
		)
	}
	return &Packet{TLVs: []TLV{
		successResultTLV(),
		{Type: 0x10, Value: value},
	}}
}

func sameBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestUIMServiceGetUSIMAIDUsesFullCardStatusAID(t *testing.T) {
	client := newUIMUnitTestClient()
	aid := []byte{0xA0, 0x00, 0x00, 0x00, 0x87, 0x10, 0x02, 0xFF, 0x49, 0xFF, 0x01, 0x89}
	stop := serveUIMUnitTestRequests(t, client, func(req *Packet) *Packet {
		if req.MessageID != UIMGetCardStatus {
			t.Fatalf("unexpected message id 0x%04x", req.MessageID)
		}
		return cardStatusPacketWithUSIMAID(aid)
	})
	defer stop()
	uim := &UIMService{client: client, clientID: 1}

	got, err := uim.GetUSIMAID(context.Background())
	if err != nil {
		t.Fatalf("GetUSIMAID() error = %v", err)
	}
	if !sameBytes(got, aid) {
		t.Fatalf("GetUSIMAID() = %X, want %X", got, aid)
	}
}

func TestUIMServiceGetISIMAIDUsesFullCardStatusAID(t *testing.T) {
	client := newUIMUnitTestClient()
	usimAID := []byte{0xA0, 0x00, 0x00, 0x00, 0x87, 0x10, 0x02, 0xFF, 0x49, 0xFF, 0x01, 0x89}
	aid := []byte{0xA0, 0x00, 0x00, 0x00, 0x87, 0x10, 0x04, 0xFF, 0xFF, 0xFF, 0xFF, 0x89, 0x03, 0x02, 0x00, 0x00}
	stop := serveUIMUnitTestRequests(t, client, func(req *Packet) *Packet {
		if req.MessageID != UIMGetCardStatus {
			t.Fatalf("unexpected message id 0x%04x", req.MessageID)
		}
		return cardStatusPacketWithApps(
			cardStatusTestApp{appType: UIMAppTypeUSIM, aid: usimAID},
			cardStatusTestApp{appType: UIMAppTypeISIM, aid: aid},
		)
	})
	defer stop()
	uim := &UIMService{client: client, clientID: 1}

	got, err := uim.GetISIMAID(context.Background())
	if err != nil {
		t.Fatalf("GetISIMAID() error = %v", err)
	}
	if !sameBytes(got, aid) {
		t.Fatalf("GetISIMAID() = %X, want %X", got, aid)
	}
}

func qmiErrorPacket(code uint16) *Packet {
	value := make([]byte, 4)
	binary.LittleEndian.PutUint16(value[0:2], 1)
	binary.LittleEndian.PutUint16(value[2:4], code)
	return &Packet{TLVs: []TLV{{Type: 0x02, Value: value}}}
}

func fileAttrsPacket(recordSize, recordCount uint16) *Packet {
	attr := make([]byte, 26)
	binary.LittleEndian.PutUint16(attr[0:2], recordSize*recordCount)
	binary.LittleEndian.PutUint16(attr[2:4], 0x6FC5)
	attr[4] = UIMFileTypeLinearFixed
	binary.LittleEndian.PutUint16(attr[5:7], recordSize)
	binary.LittleEndian.PutUint16(attr[7:9], recordCount)
	return &Packet{TLVs: []TLV{
		successResultTLV(),
		{Type: 0x10, Value: []byte{0x90, 0x00}},
		{Type: 0x11, Value: attr},
	}}
}

func readRecordPacket(data []byte) *Packet {
	value := make([]byte, 2+len(data))
	binary.LittleEndian.PutUint16(value[0:2], uint16(len(data)))
	copy(value[2:], data)
	return &Packet{TLVs: []TLV{
		successResultTLV(),
		{Type: 0x10, Value: []byte{0x90, 0x00}},
		{Type: 0x11, Value: value},
	}}
}

func readTransparentPacket(data []byte) *Packet {
	value := make([]byte, 2+len(data))
	binary.LittleEndian.PutUint16(value[0:2], uint16(len(data)))
	copy(value[2:], data)
	return &Packet{TLVs: []TLV{
		successResultTLV(),
		{Type: 0x11, Value: value},
	}}
}

func TestParseGetSlotStatusResponse(t *testing.T) {
	statusValue := []byte{
		0x02,
		0x02, 0x00, 0x00, 0x00,
		0x01, 0x00, 0x00, 0x00,
		0x01,
		0x13,
		'8', '9', '8', '6', '0', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9', '0', '1', '2', '3',
		0x01, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
		0x00,
		0x00,
	}
	extInfoValue := []byte{
		0x02,
		0x02, 0x00, 0x00, 0x00,
		0x02,
		0x03,
		0x3B, 0x9F, 0x95,
		0x01,
		0x00, 0x00, 0x00, 0x00,
		0x00,
		0x00,
		0x00,
	}
	eidValue := []byte{
		0x02,
		0x04, 0x89, 0x10, 0x00, 0x01,
		0x00,
	}

	resp := &Packet{
		TLVs: []TLV{
			successResultTLV(),
			{Type: 0x10, Value: statusValue},
			{Type: 0x11, Value: extInfoValue},
			{Type: 0x12, Value: eidValue},
		},
	}

	info, err := parseGetSlotStatusResponse(resp)
	if err != nil {
		t.Fatalf("parseGetSlotStatusResponse returned error: %v", err)
	}
	if len(info.Slots) != 2 {
		t.Fatalf("expected 2 slots, got %d", len(info.Slots))
	}
	if info.Slots[0].PhysicalCardStatus != UIMPhysicalCardStatePresent || info.Slots[0].PhysicalSlotStatus != UIMSlotStateActive {
		t.Fatalf("unexpected first slot state: %+v", info.Slots[0])
	}
	if info.Slots[0].ICCID != "8986001234567890123" {
		t.Fatalf("unexpected first slot ICCID: %+v", info.Slots[0])
	}
	if !info.Slots[0].HasExtendedInfo || info.Slots[0].CardProtocol != UIMCardProtocolUICC || !info.Slots[0].IsEUICC {
		t.Fatalf("unexpected first slot extended info: %+v", info.Slots[0])
	}
	if !info.Slots[0].HasEID || len(info.Slots[0].EID) != 4 {
		t.Fatalf("unexpected first slot EID: %+v", info.Slots[0])
	}
	if info.Slots[1].PhysicalCardStatus != UIMPhysicalCardStateAbsent || info.Slots[1].PhysicalSlotStatus != UIMSlotStateInactive {
		t.Fatalf("unexpected second slot state: %+v", info.Slots[1])
	}
}

func TestParseReadRecordResponse(t *testing.T) {
	readValue := []byte{0x03, 0x00, 0xDE, 0xAD, 0xBE}
	additionalValue := []byte{0x02, 0x00, 0xFA, 0xCE}
	token := make([]byte, 4)
	binary.LittleEndian.PutUint32(token, 0x10203040)
	resp := &Packet{
		TLVs: []TLV{
			successResultTLV(),
			{Type: 0x10, Value: []byte{0x90, 0x00}},
			{Type: 0x11, Value: readValue},
			{Type: 0x12, Value: additionalValue},
			{Type: 0x13, Value: token},
		},
	}

	info, err := parseReadRecordResponse(resp)
	if err != nil {
		t.Fatalf("parseReadRecordResponse returned error: %v", err)
	}
	if !info.HasCardResult || info.CardResult.SW1 != 0x90 || info.CardResult.SW2 != 0x00 {
		t.Fatalf("unexpected card result: %+v", info)
	}
	if len(info.Data) != 3 || info.Data[2] != 0xBE {
		t.Fatalf("unexpected read data: %+v", info.Data)
	}
	if len(info.AdditionalData) != 2 || info.AdditionalData[1] != 0xCE {
		t.Fatalf("unexpected additional data: %+v", info.AdditionalData)
	}
	if !info.HasResponseInIndicationToken || info.ResponseInIndicationToken != 0x10203040 {
		t.Fatalf("unexpected token: %+v", info)
	}
}

func TestParseGetFileAttributesResponse(t *testing.T) {
	attrValue := make([]byte, 29)
	binary.LittleEndian.PutUint16(attrValue[0:2], 64)
	binary.LittleEndian.PutUint16(attrValue[2:4], 0x6F3A)
	attrValue[4] = UIMFileTypeLinearFixed
	binary.LittleEndian.PutUint16(attrValue[5:7], 16)
	binary.LittleEndian.PutUint16(attrValue[7:9], 4)
	attrValue[9] = 1
	binary.LittleEndian.PutUint16(attrValue[10:12], 0x1001)
	attrValue[12] = 2
	binary.LittleEndian.PutUint16(attrValue[13:15], 0x1002)
	attrValue[15] = 3
	binary.LittleEndian.PutUint16(attrValue[16:18], 0x1003)
	attrValue[18] = 4
	binary.LittleEndian.PutUint16(attrValue[19:21], 0x1004)
	attrValue[21] = 5
	binary.LittleEndian.PutUint16(attrValue[22:24], 0x1005)
	binary.LittleEndian.PutUint16(attrValue[24:26], 3)
	copy(attrValue[26:29], []byte{0x62, 0x10, 0x82})

	token := make([]byte, 4)
	binary.LittleEndian.PutUint32(token, 0x0A0B0C0D)
	resp := &Packet{
		TLVs: []TLV{
			successResultTLV(),
			{Type: 0x10, Value: []byte{0x90, 0x00}},
			{Type: 0x11, Value: attrValue},
			{Type: 0x12, Value: token},
		},
	}

	info, err := parseGetFileAttributesResponse(resp)
	if err != nil {
		t.Fatalf("parseGetFileAttributesResponse returned error: %v", err)
	}
	if info.FileID != 0x6F3A || info.FileType != UIMFileTypeLinearFixed || info.RecordCount != 4 {
		t.Fatalf("unexpected file attributes: %+v", info)
	}
	if info.ReadSecurity.Attributes != 0x1001 || info.ActivateSecurity.Attributes != 0x1005 {
		t.Fatalf("unexpected security attributes: %+v", info)
	}
	if len(info.RawData) != 3 || info.RawData[2] != 0x82 {
		t.Fatalf("unexpected raw data: %+v", info.RawData)
	}
	if !info.HasResponseInIndicationToken || info.ResponseInIndicationToken != 0x0A0B0C0D {
		t.Fatalf("unexpected token: %+v", info)
	}
}

func TestReadPNNRecordsSelectsOnePathForWholeEFAndStopsAtEmptyRecord(t *testing.T) {
	c := newUIMUnitTestClient()
	u := &UIMService{client: c, clientID: 1}
	defer serveUIMUnitTestRequests(t, c, func(req *Packet) *Packet {
		path := requestFilePath(req)
		switch req.MessageID {
		case UIMGetFileAttrs:
			if sameBytes(path, []byte{0x00, 0x3F, 0xFF, 0x7F}) {
				return qmiErrorPacket(QMIErrInvalidArg)
			}
			if sameBytes(path, []byte{0x20, 0x7F}) {
				return fileAttrsPacket(6, 2)
			}
			t.Errorf("unexpected file attrs path % X", path)
		case UIMReadRecord:
			if !sameBytes(path, []byte{0x20, 0x7F}) {
				t.Errorf("read record used fallback path % X after EF path selection", path)
				return qmiErrorPacket(QMIErrInvalidArg)
			}
			switch requestRecordNumber(req) {
			case 1:
				return readRecordPacket([]byte{0x43, 0x04, 'T', 'e', 's', 't'})
			case 2:
				return readRecordPacket([]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF})
			default:
				t.Errorf("read continued past empty record: record=%d", requestRecordNumber(req))
			}
		default:
			t.Errorf("unexpected message 0x%04x", req.MessageID)
		}
		return qmiErrorPacket(QMIErrInvalidArg)
	})()

	records, err := u.readPNNRecords(context.Background(), 0x6FC5)
	if err != nil {
		t.Fatalf("readPNNRecords returned error: %v", err)
	}
	if len(records) != 1 || records[0].Record != 1 {
		t.Fatalf("expected exactly the first PNN record, got %+v", records)
	}
}

func TestReadPNNRecordsReturnsContextCancellation(t *testing.T) {
	c := newUIMUnitTestClient()
	u := &UIMService{client: c, clientID: 1}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer serveUIMUnitTestRequests(t, c, func(req *Packet) *Packet {
		switch req.MessageID {
		case UIMGetFileAttrs:
			return fileAttrsPacket(6, 32)
		case UIMReadRecord:
			if requestRecordNumber(req) != 1 {
				t.Errorf("read continued after context cancellation: record=%d", requestRecordNumber(req))
				return qmiErrorPacket(QMIErrInvalidArg)
			}
			cancel()
			return readRecordPacket([]byte{0x43, 0x04, 'T', 'e', 's', 't'})
		default:
			t.Errorf("unexpected message 0x%04x", req.MessageID)
		}
		return qmiErrorPacket(QMIErrInvalidArg)
	})()

	_, err := u.readPNNRecords(ctx, 0x6FC5)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestGetSIMServiceTableDoesNotRepeatSuccessfulEmptyRead(t *testing.T) {
	c := newUIMUnitTestClient()
	u := &UIMService{client: c, clientID: 1}
	transparentReads := 0
	defer serveUIMUnitTestRequests(t, c, func(req *Packet) *Packet {
		if req.MessageID != UIMReadTransparent {
			t.Errorf("unexpected message 0x%04x", req.MessageID)
			return qmiErrorPacket(QMIErrInvalidArg)
		}
		transparentReads++
		if transparentReads > 1 {
			t.Errorf("GetSIMServiceTable repeated a successful empty transparent read")
		}
		return readTransparentPacket(nil)
	})()

	table, err := u.GetSIMServiceTable(context.Background())
	if err != nil {
		t.Fatalf("GetSIMServiceTable returned error: %v", err)
	}
	if table != nil {
		t.Fatalf("expected nil service table for empty EF, got %+v", table)
	}
	if transparentReads != 1 {
		t.Fatalf("expected one transparent read, got %d", transparentReads)
	}
}

func TestDecodeUIMDigits(t *testing.T) {
	if got := decodeUIMDigits([]byte("898600")); got != "898600" {
		t.Fatalf("unexpected ASCII digit decode: %s", got)
	}
	if got := decodeUIMDigits([]byte{0x98, 0x10, 0x32}); got != "890123" {
		t.Fatalf("unexpected BCD digit decode: %s", got)
	}
}

func TestParseSupportedMessagesTLV(t *testing.T) {
	resp := &Packet{
		TLVs: []TLV{
			successResultTLV(),
			{Type: 0x10, Value: []byte{0x03, 0x00, 0x2F, 0x30, 0x47}},
		},
	}

	msgs, err := parseSupportedMessagesTLV(resp)
	if err != nil {
		t.Fatalf("parseSupportedMessagesTLV returned error: %v", err)
	}
	if len(msgs) != 3 || msgs[0] != 0x2F || msgs[2] != 0x47 {
		t.Fatalf("unexpected supported messages: %v", msgs)
	}
}

func TestBuildChangeProvisioningSessionTLVs(t *testing.T) {
	slot := uint8(2)
	tlvs := buildChangeProvisioningSessionTLVs(UIMChangeProvisioningSessionRequest{
		SessionType:           UIMSessionTypePrimaryGWProvisioning,
		Activate:              true,
		Slot:                  &slot,
		ApplicationIdentifier: []byte{0xA0, 0x00},
	})

	if len(tlvs) != 2 {
		t.Fatalf("expected 2 TLVs, got %d", len(tlvs))
	}
	if tlvs[0].Type != 0x01 || len(tlvs[0].Value) != 2 || tlvs[0].Value[0] != UIMSessionTypePrimaryGWProvisioning || tlvs[0].Value[1] != 1 {
		t.Fatalf("unexpected session change TLV: %+v", tlvs[0])
	}
	if tlvs[1].Type != 0x10 || len(tlvs[1].Value) != 4 || tlvs[1].Value[0] != 2 || tlvs[1].Value[1] != 2 {
		t.Fatalf("unexpected app info TLV header: %+v", tlvs[1])
	}
	if tlvs[1].Value[2] != 0xA0 || tlvs[1].Value[3] != 0x00 {
		t.Fatalf("unexpected app info TLV payload: %+v", tlvs[1])
	}
}

func TestBuildRefreshRegisterInfoTLV(t *testing.T) {
	tlv, err := buildRefreshRegisterInfoTLV(UIMRefreshRegisterRequest{
		RegisterFlag: true,
		VoteForInit:  true,
		Files: []UIMRefreshFile{
			{FileID: 0x6F07, Path: []uint8{0x00, 0x3F}},
		},
	})
	if err != nil {
		t.Fatalf("buildRefreshRegisterInfoTLV returned error: %v", err)
	}
	if tlv.Type != 0x02 || len(tlv.Value) != 9 {
		t.Fatalf("unexpected refresh register info TLV: %+v", tlv)
	}
	if tlv.Value[0] != 1 || tlv.Value[1] != 1 || binary.LittleEndian.Uint16(tlv.Value[2:4]) != 1 {
		t.Fatalf("unexpected refresh register flags/count: %v", tlv.Value[:4])
	}
	if binary.LittleEndian.Uint16(tlv.Value[4:6]) != 0x6F07 || tlv.Value[6] != 2 || tlv.Value[7] != 0x00 || tlv.Value[8] != 0x3F {
		t.Fatalf("unexpected refresh register file payload: %v", tlv.Value[4:])
	}
}

func TestParseUIMRefreshIndication(t *testing.T) {
	packet := &Packet{
		TLVs: []TLV{
			{
				Type: 0x10,
				Value: []byte{
					0x01, 0x02, UIMSessionTypePrimaryGWProvisioning,
					0x02, 0xA0, 0x00,
					0x01, 0x00,
					0x07, 0x6F, 0x02, 0x00, 0x3F,
				},
			},
		},
	}

	info, err := ParseUIMRefreshIndication(packet)
	if err != nil {
		t.Fatalf("ParseUIMRefreshIndication returned error: %v", err)
	}
	if info.Stage != 0x01 || info.Mode != 0x02 || info.SessionType != UIMSessionTypePrimaryGWProvisioning {
		t.Fatalf("unexpected refresh indication header: %+v", info)
	}
	if len(info.ApplicationIdentifier) != 2 || info.ApplicationIdentifier[0] != 0xA0 {
		t.Fatalf("unexpected refresh indication aid: %+v", info.ApplicationIdentifier)
	}
	if len(info.Files) != 1 || info.Files[0].FileID != 0x6F07 || len(info.Files[0].Path) != 2 || info.Files[0].Path[1] != 0x3F {
		t.Fatalf("unexpected refresh indication files: %+v", info.Files)
	}
}

func TestParseUIMSlotStatusIndication(t *testing.T) {
	packet := &Packet{
		TLVs: []TLV{
			{
				Type:  0x10,
				Value: []byte{0x00},
			},
		},
	}

	info, err := ParseUIMSlotStatusIndication(packet)
	if err != nil {
		t.Fatalf("ParseUIMSlotStatusIndication returned error: %v", err)
	}
	if info == nil || len(info.Slots) != 0 {
		t.Fatalf("unexpected slot status indication parse result: %+v", info)
	}
}

func TestUIMRegisterEventsReturnsAcceptedMask(t *testing.T) {
	c := &Client{
		eventCh:        make(chan Event, 1),
		indicationInCh: make(chan Event, 1),
		writeCh:        make(chan writeRequest, 1),
		closeCh:        make(chan struct{}),
		transactions:   make(map[uint32]*transactionEntry),
		opts:           DefaultClientOptions(),
	}
	u := &UIMService{client: c, clientID: 7}

	go func() {
		wr := <-c.writeCh
		wr.result <- nil
		key := uint32(ServiceUIM)<<16 | 1
		c.mu.Lock()
		entry := c.transactions[key]
		c.mu.Unlock()
		if entry == nil {
			t.Errorf("response channel not found for key=%d", key)
			return
		}
		entry.ch <- &Packet{TLVs: []TLV{
			successResultTLV(),
			{Type: 0x10, Value: []byte{0x05, 0x00, 0x00, 0x00}},
		}}
	}()

	mask, err := u.RegisterEvents(context.Background(), UIMEventRegistrationCardStatus|UIMEventRegistrationPhysicalSlotStatus)
	if err != nil {
		t.Fatalf("RegisterEvents returned error: %v", err)
	}
	if mask != 0x00000005 {
		t.Fatalf("unexpected accepted mask: got=0x%08x", mask)
	}
}

func TestUIMRegisterEventsFallsBackToRequestedMask(t *testing.T) {
	c := &Client{
		eventCh:        make(chan Event, 1),
		indicationInCh: make(chan Event, 1),
		writeCh:        make(chan writeRequest, 1),
		closeCh:        make(chan struct{}),
		transactions:   make(map[uint32]*transactionEntry),
		opts:           DefaultClientOptions(),
	}
	u := &UIMService{client: c, clientID: 8}
	requested := UIMEventRegistrationCardStatus | UIMEventRegistrationExtendedCardStatus

	go func() {
		wr := <-c.writeCh
		wr.result <- nil
		key := uint32(ServiceUIM)<<16 | 1
		c.mu.Lock()
		entry := c.transactions[key]
		c.mu.Unlock()
		if entry == nil {
			t.Errorf("response channel not found for key=%d", key)
			return
		}
		entry.ch <- &Packet{TLVs: []TLV{successResultTLV()}}
	}()

	mask, err := u.RegisterEvents(context.Background(), requested)
	if err != nil {
		t.Fatalf("RegisterEvents returned error: %v", err)
	}
	if mask != requested {
		t.Fatalf("expected requested mask fallback=0x%08x, got=0x%08x", requested, mask)
	}
}

func TestUIMRegisterEventsMapsNotSupported(t *testing.T) {
	c := &Client{
		eventCh:        make(chan Event, 1),
		indicationInCh: make(chan Event, 1),
		writeCh:        make(chan writeRequest, 1),
		closeCh:        make(chan struct{}),
		transactions:   make(map[uint32]*transactionEntry),
		opts:           DefaultClientOptions(),
	}
	u := &UIMService{client: c, clientID: 9}

	go func() {
		wr := <-c.writeCh
		wr.result <- nil
		key := uint32(ServiceUIM)<<16 | 1
		c.mu.Lock()
		entry := c.transactions[key]
		c.mu.Unlock()
		if entry == nil {
			t.Errorf("response channel not found for key=%d", key)
			return
		}
		entry.ch <- &Packet{TLVs: []TLV{{Type: 0x02, Value: []byte{0x01, 0x00, 0x5E, 0x00}}}}
	}()

	_, err := u.RegisterEvents(context.Background(), UIMEventRegistrationCardStatus)
	if err == nil {
		t.Fatal("expected not supported error")
	}
	if _, ok := err.(*NotSupportedError); !ok {
		t.Fatalf("expected NotSupportedError, got %T: %v", err, err)
	}
}
