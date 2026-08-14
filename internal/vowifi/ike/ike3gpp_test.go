package ike

import (
	"bytes"
	"testing"
)

func TestDeviceIdentityNotifyMatchesAndroidEncoding(t *testing.T) {
	item, err := deviceIdentityNotify("123456789012345")
	if err != nil {
		t.Fatal(err)
	}
	kind, data, err := parseNotify(item)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0, 9, 1, 0x21, 0x43, 0x65, 0x87, 0x09, 0x21, 0x43, 0xf5}
	if kind != notifyDeviceIdentity || !bytes.Equal(data, want) {
		t.Fatalf("DEVICE_IDENTITY = %d/%x, want %d/%x", kind, data, notifyDeviceIdentity, want)
	}
}

func TestDeviceIdentityNotifyAcceptsIMEISV(t *testing.T) {
	item, err := deviceIdentityNotify("1234567890123456")
	if err != nil {
		t.Fatal(err)
	}
	_, data, _ := parseNotify(item)
	if data[2] != 2 || data[10] != 0x65 {
		t.Fatalf("IMEISV data = %x", data)
	}
}

func TestDeviceIdentityRequested(t *testing.T) {
	requested, err := deviceIdentityRequested([]payload{makeNotify(notifyDeviceIdentity, nil)})
	if err != nil || !requested {
		t.Fatalf("deviceIdentityRequested() = %v, %v", requested, err)
	}
}
