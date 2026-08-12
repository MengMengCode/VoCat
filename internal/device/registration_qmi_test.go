package device

import "testing"

func TestParseQMIRegistrationRegisteredRoaming(t *testing.T) {
	output := `
Registration state: 'registered'
CS: 'attached'
PS: 'attached'
Roaming status: 'on'
Current PLMN:
    MCC: '460'
    MNC: '1'
    Description: 'UNICOM'
Full operator code info:
    MCC: '460'
    MNC: '1'
    MNC with PCS digit: 'no'
`
	result, found := parseQMIRegistration(output)
	if !found || result.Status != 5 || !result.PSAttached || result.PLMN != "46001" || result.Name != "UNICOM" {
		t.Fatalf("registration = %#v, found=%v", result, found)
	}
}

func TestParseQMIRegistrationSearching(t *testing.T) {
	result, found := parseQMIRegistration("Registration state: 'not-registered-searching'\nPS: 'detached'")
	if !found || result.Status != 2 || result.PSAttached {
		t.Fatalf("registration = %#v, found=%v", result, found)
	}
}

func TestParseQMIRegistrationLimitedRoamingService(t *testing.T) {
	output := `
Registration state: 'not-registered-searching'
PS: 'detached'
Roaming status: 'on'
Detailed status:
    Status: 'limited'
Current PLMN:
    MCC: '460'
    MNC: '1'
    Description: 'UNICOM'
`
	result, found := parseQMIRegistration(output)
	if !found || result.Status != 2 || !result.LimitedService || result.PSAttached || result.PLMN != "46001" {
		t.Fatalf("registration = %#v, found=%v", result, found)
	}
}
