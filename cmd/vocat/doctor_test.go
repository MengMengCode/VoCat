package main

import "testing"

func TestDoctorHelpIsSuccessful(t *testing.T) {
	if err := runDoctor([]string{"--help"}); err != nil {
		t.Fatalf("runDoctor(--help) error = %v", err)
	}
}
