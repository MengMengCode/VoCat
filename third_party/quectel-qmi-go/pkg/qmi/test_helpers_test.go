package qmi

func successResultTLV() TLV {
	return TLV{Type: 0x02, Value: []byte{0x00, 0x00, 0x00, 0x00}}
}

func qmiErrorResultTLV(code uint16) TLV {
	return TLV{Type: 0x02, Value: []byte{0x01, 0x00, byte(code), byte(code >> 8)}}
}
