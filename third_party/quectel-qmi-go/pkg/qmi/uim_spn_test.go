package qmi

import "testing"

func TestDecodeEFSPNASCII(t *testing.T) {
	data := []byte{0x00, 'C', 'M', 'C', 'C', 0xFF, 0xFF, 0xFF}
	got, err := decodeEFSPN(data)
	if err != nil {
		t.Fatalf("decodeEFSPN() error = %v", err)
	}
	if got != "CMCC" {
		t.Fatalf("decodeEFSPN() = %q, want %q", got, "CMCC")
	}
}

func TestDecodeEFSPNUCS2(t *testing.T) {
	data := []byte{0x00, 0x80, 0x4E, 0x2D, 0x56, 0xFD, 0x79, 0xFB, 0x52, 0xA8, 0xFF, 0xFF}
	got, err := decodeEFSPN(data)
	if err != nil {
		t.Fatalf("decodeEFSPN() error = %v", err)
	}
	if got != "中国移动" {
		t.Fatalf("decodeEFSPN() = %q, want %q", got, "中国移动")
	}
}

func TestDecodeEFSPNCompressedUCS281(t *testing.T) {
	data := []byte{0x00, 0x81, 0x03, 0x9C, 0xAD, 0x01, 0x02, 0xFF}
	got, err := decodeEFSPN(data)
	if err != nil {
		t.Fatalf("decodeEFSPN() error = %v", err)
	}
	if got != "中£$" {
		t.Fatalf("decodeEFSPN() = %q, want %q", got, "中£$")
	}
}

func TestDecodeEFSPNCompressedUCS282(t *testing.T) {
	data := []byte{0x00, 0x82, 0x03, 0x4E, 0x00, 0xAD, 0x01, 0x02, 0xFF}
	got, err := decodeEFSPN(data)
	if err != nil {
		t.Fatalf("decodeEFSPN() error = %v", err)
	}
	if got != "中£$" {
		t.Fatalf("decodeEFSPN() = %q, want %q", got, "中£$")
	}
}

func TestDecodeEFSPNEmptyPadding(t *testing.T) {
	data := []byte{0x00, 0xFF, 0xFF, 0x00}
	if got, err := decodeEFSPN(data); err == nil {
		t.Fatalf("decodeEFSPN() = %q, want error", got)
	}
}
