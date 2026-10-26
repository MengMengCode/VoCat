package main

import (
	"testing"

	"vocat/internal/modem"
)

func TestDoctorHelpIsSuccessful(t *testing.T) {
	if err := runDoctor([]string{"--help"}); err != nil {
		t.Fatalf("runDoctor(--help) error = %v", err)
	}
}

func TestIsOpenStickNativeQMICandidate(t *testing.T) {
	if !isOpenStickNativeQMICandidate(modem.Candidate{ID: "mhi-wwan0", QMIControl: "/dev/wwan0qmi0"}) {
		t.Fatal("MHI OpenStick QMI candidate was not recognized")
	}
	for _, candidate := range []modem.Candidate{
		{ID: "mhi-wwan0", QMIControl: "/dev/cdc-wdm0"},
		{ID: "wwan0", QMIControl: "/dev/wwan0qmi0"},
		{ID: "mhi-wwan0", QMIControl: ""},
	} {
		if isOpenStickNativeQMICandidate(candidate) {
			t.Fatalf("unexpected OpenStick native QMI match: %#v", candidate)
		}
	}
}
