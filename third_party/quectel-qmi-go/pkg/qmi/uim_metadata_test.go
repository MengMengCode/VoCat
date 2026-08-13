package qmi

import "testing"

func TestNativeMCCMNCFromOPLRecordsUsesFirstExactPLMN(t *testing.T) {
	mcc, mnc, ok := nativeMCCMNCFromOPLRecords([]OPLRecord{
		{Record: 1, PLMN: "51566"},
		{Record: 2, PLMN: "20404"},
	})
	if !ok {
		t.Fatal("nativeMCCMNCFromOPLRecords() ok=false")
	}
	if mcc != "515" || mnc != "66" {
		t.Fatalf("mcc/mnc = %s/%s, want 515/66", mcc, mnc)
	}
}

func TestNativeMCCMNCFromOPLRecordsSkipsWildcardPLMN(t *testing.T) {
	mcc, mnc, ok := nativeMCCMNCFromOPLRecords([]OPLRecord{
		{Record: 1, PLMN: "515xx"},
		{Record: 2, PLMN: "310260"},
	})
	if !ok {
		t.Fatal("nativeMCCMNCFromOPLRecords() ok=false")
	}
	if mcc != "310" || mnc != "260" {
		t.Fatalf("mcc/mnc = %s/%s, want 310/260", mcc, mnc)
	}
}
