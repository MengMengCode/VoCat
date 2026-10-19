package device

import (
	"encoding/hex"
	"fmt"
	"testing"
	"unicode/utf16"
)

func TestDecodeSMSPDUAlphanumericCRPadding(t *testing.T) {
	// Synthetic reproduction from #177; the OTP is test data.
	const pdu = "00040DD0C2B43BEC1E971B00006201917123130013D9775D0E1ABFC965507A0E8AC966B49A0D"
	m, err := decodeSMSPDU(pdu)
	if err != nil || m.From != "Binance" || m.DataCodingScheme != 0 || m.Text != "Your code is 123456" {
		t.Fatalf("sender=%q DCS=0x%02X text=%q error=%v", m.From, m.DataCodingScheme, m.Text, err)
	}
}
func TestDecodeSMSPDUAlphanumericPaddingMatrix(t *testing.T) {
	for _, sender := range []string{"Binance", "AUTHMSG", "OKX", "Google", "VoCat"} {
		for _, nonzero := range []bool{false, true} {
			for _, ucs2 := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/nonzero=%t/ucs2=%t", sender, nonzero, ucs2), func(t *testing.T) {
					septets, _ := encodeGSM7(sender)
					address := packSeptets(septets, 0)
					if nonzero {
						for bit := len(septets) * 7; bit < len(address)*8; bit++ {
							address[bit/8] |= 1 << uint(bit%8)
						}
					}
					pdu := []byte{0, 4, byte((len(septets)*7 + 3) / 4), 0xd0}
					pdu = append(pdu, address...)
					dcs := byte(0)
					body := "Your code is 123456"
					bodySeptets, _ := encodeGSM7(body)
					payload := packSeptets(bodySeptets, 0)
					udl := len(bodySeptets)
					if ucs2 {
						dcs = 8
						body = "Test 123456 \u9a8c\u8bc1\u7801"
						payload = nil
						for _, unit := range utf16.Encode([]rune(body)) {
							payload = append(payload, byte(unit>>8), byte(unit))
						}
						udl = len(payload)
					}
					pdu = append(pdu, 0, dcs, 0x62, 0x01, 0x91, 0x71, 0x23, 0x13, 0, byte(udl))
					pdu = append(pdu, payload...)
					m, err := decodeSMSPDU(hex.EncodeToString(pdu))
					if err != nil || m.From != sender || m.Text != body || m.DataCodingScheme != int(dcs) {
						t.Fatalf("sender=%q DCS=%02X text=%q error=%v", m.From, m.DataCodingScheme, m.Text, err)
					}
				})
			}
		}
	}
}
