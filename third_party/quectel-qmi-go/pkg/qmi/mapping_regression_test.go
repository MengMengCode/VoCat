package qmi

import "testing"

func TestUIMMessageIDMappingsMatchSpec(t *testing.T) {
	if UIMReset != 0x0000 {
		t.Fatalf("UIMReset = 0x%04X, want 0x0000", UIMReset)
	}
	if UIMGetSupportedMessages != 0x001E {
		t.Fatalf("UIMGetSupportedMessages = 0x%04X, want 0x001E", UIMGetSupportedMessages)
	}
	if UIMSetPINProtection != 0x0025 {
		t.Fatalf("UIMSetPINProtection = 0x%04X, want 0x0025", UIMSetPINProtection)
	}
	if UIMVerifyPIN != 0x0026 {
		t.Fatalf("UIMVerifyPIN = 0x%04X, want 0x0026", UIMVerifyPIN)
	}
	if UIMUnblockPIN != 0x0027 {
		t.Fatalf("UIMUnblockPIN = 0x%04X, want 0x0027", UIMUnblockPIN)
	}
	if UIMChangePIN != 0x0028 {
		t.Fatalf("UIMChangePIN = 0x%04X, want 0x0028", UIMChangePIN)
	}
	if UIMRefreshRegister != 0x002A {
		t.Fatalf("UIMRefreshRegister = 0x%04X, want 0x002A", UIMRefreshRegister)
	}
	if UIMRefreshComplete != 0x002C {
		t.Fatalf("UIMRefreshComplete = 0x%04X, want 0x002C", UIMRefreshComplete)
	}
	if UIMPowerOffSIM != 0x0030 {
		t.Fatalf("UIMPowerOffSIM = 0x%04X, want 0x0030", UIMPowerOffSIM)
	}
	if UIMPowerOnSIM != 0x0031 {
		t.Fatalf("UIMPowerOnSIM = 0x%04X, want 0x0031", UIMPowerOnSIM)
	}
	if UIMChangeProvisioningSession != 0x0038 {
		t.Fatalf("UIMChangeProvisioningSession = 0x%04X, want 0x0038", UIMChangeProvisioningSession)
	}
	if UIMRefreshRegisterAll != 0x0044 {
		t.Fatalf("UIMRefreshRegisterAll = 0x%04X, want 0x0044", UIMRefreshRegisterAll)
	}
	if UIMSwitchSlot != 0x0046 {
		t.Fatalf("UIMSwitchSlot = 0x%04X, want 0x0046", UIMSwitchSlot)
	}
	if UIMGetSlotStatus != 0x0047 {
		t.Fatalf("UIMGetSlotStatus = 0x%04X, want 0x0047", UIMGetSlotStatus)
	}
}

func TestDMSUIMMessageIDMappingsMatchSpec(t *testing.T) {
	if DMSUIMSetPINProtection != 0x0027 {
		t.Fatalf("DMSUIMSetPINProtection = 0x%04X, want 0x0027", DMSUIMSetPINProtection)
	}
	if DMSUIMVerifyPIN != 0x0028 {
		t.Fatalf("DMSUIMVerifyPIN = 0x%04X, want 0x0028", DMSUIMVerifyPIN)
	}
	if DMSUIMUnblockPIN != 0x0029 {
		t.Fatalf("DMSUIMUnblockPIN = 0x%04X, want 0x0029", DMSUIMUnblockPIN)
	}
	if DMSUIMChangePIN != 0x002A {
		t.Fatalf("DMSUIMChangePIN = 0x%04X, want 0x002A", DMSUIMChangePIN)
	}
}

func TestWDSProfileMessageIDMappingsMatchSpec(t *testing.T) {
	if WDSCreateProfile != 0x0027 {
		t.Fatalf("WDSCreateProfile = 0x%04X, want 0x0027", WDSCreateProfile)
	}
	if WDSModifyProfileSettings != 0x0028 {
		t.Fatalf("WDSModifyProfileSettings = 0x%04X, want 0x0028", WDSModifyProfileSettings)
	}
}
