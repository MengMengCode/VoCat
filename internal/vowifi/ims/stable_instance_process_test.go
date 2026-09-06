package ims

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"testing"
	"vocat/internal/vowifi"
)

func TestStableInstanceAcrossProcesses(t *testing.T) {
	if os.Getenv("VOCAT_STABLE_INSTANCE_TEST_CHILD") == "1" {
		s := newStableInstanceTestSession(t, vowifi.IMSRequest{DeviceID: "usb-fixture", Identity: vowifi.SIMIdentity{IMEI: "353024112557010", IMSI: "001010123456789", HomeMCC: "001", HomeMNC: "01"}})
		fmt.Println("INSTANCE=" + s.instanceID)
		return
	}
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	var ids []string
	for range 2 {
		cmd := exec.Command(exe, "-test.run=^TestStableInstanceAcrossProcesses$")
		cmd.Env = append(os.Environ(), "VOCAT_STABLE_INSTANCE_TEST_CHILD=1")
		out, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("subprocess failed: %v %s", e, out)
		}
		m := regexp.MustCompile(`INSTANCE=(urn:uuid:[0-9a-f-]+)`).FindStringSubmatch(string(out))
		if len(m) != 2 {
			t.Fatalf("no instance: %s", out)
		}
		ids = append(ids, m[1])
	}
	if ids[0] != ids[1] {
		t.Fatalf("process restart changed instance: %v", ids)
	}
	// Independently calculated with Python uuid.uuid5(uuid.NAMESPACE_URL,...).
	if ids[0] != "urn:uuid:eddf02e1-07af-5305-bad0-a7ce44dd536d" {
		t.Fatalf("UUIDv5 reference mismatch: %s", ids[0])
	}
}
