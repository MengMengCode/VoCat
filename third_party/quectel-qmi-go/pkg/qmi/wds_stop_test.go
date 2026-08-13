package qmi

import (
	"encoding/binary"
	"testing"
)

func TestBuildStopNetworkInterfaceTLVsSupportsAnyHandleAndDisableAutoconnect(t *testing.T) {
	tlvs := buildStopNetworkInterfaceTLVs(StopNetworkInterfaceOptions{
		Handle:             AnyPacketDataHandle,
		DisableAutoconnect: true,
	})

	handle := FindTLV(tlvs, 0x01)
	if handle == nil || len(handle.Value) != 4 {
		t.Fatalf("handle TLV = %+v, want 4-byte TLV", handle)
	}
	if got := binary.LittleEndian.Uint32(handle.Value); got != AnyPacketDataHandle {
		t.Fatalf("handle = 0x%08x, want 0x%08x", got, AnyPacketDataHandle)
	}

	disable := FindTLV(tlvs, 0x10)
	if disable == nil || len(disable.Value) != 1 || disable.Value[0] != 1 {
		t.Fatalf("disable autoconnect TLV = %+v, want boolean true", disable)
	}
}

func TestBuildStopNetworkInterfaceTLVsKeepsDefaultStopRequestMinimal(t *testing.T) {
	tlvs := buildStopNetworkInterfaceTLVs(StopNetworkInterfaceOptions{Handle: 0x11223344})
	if len(tlvs) != 1 {
		t.Fatalf("TLV count = %d, want 1", len(tlvs))
	}
	handle := FindTLV(tlvs, 0x01)
	if handle == nil || binary.LittleEndian.Uint32(handle.Value) != 0x11223344 {
		t.Fatalf("handle TLV = %+v, want 0x11223344", handle)
	}
	if disable := FindTLV(tlvs, 0x10); disable != nil {
		t.Fatalf("disable autoconnect TLV present in minimal stop request: %+v", disable)
	}
}
