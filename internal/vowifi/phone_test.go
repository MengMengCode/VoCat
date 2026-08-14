package vowifi

import "testing"

func TestExtractAssociatedMSISDN(t *testing.T) {
	tests := []struct {
		name       string
		evidence   IMSEvidence
		wantNumber string
		wantSource string
		wantOK     bool
	}{
		{
			name: "explicit associated identity with IMS domain",
			evidence: IMSEvidence{
				AssociatedMSISDN: "+447700900123@ims.mnc015.mcc234.3gppnetwork.org",
			},
			wantNumber: "+447700900123",
			wantSource: PhoneSourceAssociatedMSISDN,
			wantOK:     true,
		},
		{
			name: "tel P-Associated-URI",
			evidence: IMSEvidence{
				PAssociatedURI: []string{"<tel:+44 7700-900123>"},
			},
			wantNumber: "+447700900123",
			wantSource: PhoneSourcePAssociatedURI,
			wantOK:     true,
		},
		{
			name: "SIP E164 P-Associated-URI",
			evidence: IMSEvidence{
				PAssociatedURI: []string{
					"sip:234150000000000@ims.mnc015.mcc234.3gppnetwork.org",
					"<sip:+447700900123@ims.mnc015.mcc234.3gppnetwork.org;user=phone>",
				},
			},
			wantNumber: "+447700900123",
			wantSource: PhoneSourcePAssociatedURI,
			wantOK:     true,
		},
		{
			name: "percent encoded plus",
			evidence: IMSEvidence{
				PAssociatedURI: []string{"tel:%2B447700900123"},
			},
			wantNumber: "+447700900123",
			wantSource: PhoneSourcePAssociatedURI,
			wantOK:     true,
		},
		{
			name: "reject IMSI-shaped SIP IMPU",
			evidence: IMSEvidence{
				PAssociatedURI: []string{
					"sip:234150000000000@ims.mnc015.mcc234.3gppnetwork.org",
				},
			},
			wantOK: false,
		},
		{
			name: "reject arbitrary untyped P-Associated value",
			evidence: IMSEvidence{
				PAssociatedURI: []string{"+447700900123"},
			},
			wantOK: false,
		},
		{
			name: "reject command characters",
			evidence: IMSEvidence{
				AssociatedMSISDN: "+447700900123;AT+CFUN=0",
			},
			wantOK: false,
		},
		{
			name: "reject overlong E164",
			evidence: IMSEvidence{
				AssociatedMSISDN: "+1234567890123456",
			},
			wantOK: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			number, source, ok := ExtractAssociatedMSISDN(test.evidence)
			if number != test.wantNumber || source != test.wantSource || ok != test.wantOK {
				t.Fatalf(
					"ExtractAssociatedMSISDN() = (%q, %q, %t), want (%q, %q, %t)",
					number,
					source,
					ok,
					test.wantNumber,
					test.wantSource,
					test.wantOK,
				)
			}
		})
	}
}

func TestDeriveEPDGUsesExplicitPLMNAndNeverIMSIHeuristics(t *testing.T) {
	tests := []struct {
		name     string
		identity SIMIdentity
		want     string
		wantErr  bool
	}{
		{
			name: "two digit MNC is padded",
			identity: SIMIdentity{
				ICCID:   "one",
				IMSI:    "untrusted-for-plmn",
				HomeMCC: "234",
				HomeMNC: "15",
			},
			want: "epdg.epc.mnc015.mcc234.pub.3gppnetwork.org",
		},
		{
			name: "three digit MNC is preserved",
			identity: SIMIdentity{
				ICCID:   "one",
				HomeMCC: "310",
				HomeMNC: "260",
			},
			want: "epdg.epc.mnc260.mcc310.pub.3gppnetwork.org",
		},
		{
			name: "AT&T 310280 uses carrier endpoint",
			identity: SIMIdentity{
				ICCID:   "89012804332291663965",
				IMSI:    "310280229187733",
				HomeMCC: "310",
				HomeMNC: "280",
			},
			want: "epdg.epc.att.net",
		},
		{
			name: "explicit endpoint",
			identity: SIMIdentity{
				ICCID:   "one",
				HomeMCC: "234",
				HomeMNC: "15",
				EPDG:    "EPDG.EXAMPLE.NET",
			},
			want: "epdg.example.net",
		},
		{
			name: "missing explicit MNC is not guessed from IMSI",
			identity: SIMIdentity{
				ICCID:   "one",
				IMSI:    "234150000000000",
				HomeMCC: "234",
			},
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			epdg, err := DeriveEPDG(test.identity)
			if (err != nil) != test.wantErr {
				t.Fatalf("DeriveEPDG() error = %v, wantErr %t", err, test.wantErr)
			}
			if epdg != test.want {
				t.Fatalf("DeriveEPDG() = %q, want %q", epdg, test.want)
			}
		})
	}
}
