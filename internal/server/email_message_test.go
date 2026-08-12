package server

import (
	"bytes"
	"encoding/base64"
	"net/mail"
	"strings"
	"testing"
)

func TestWritePlainTextMailEncodesUntrustedContent(t *testing.T) {
	from, err := parseMailAddress("VoCat Alerts <alerts@example.com>")
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := parseMailAddress("Admin <admin@example.com>")
	if err != nil {
		t.Fatal(err)
	}
	body := "message\r\nBcc: injected@example.com\r\n<script>alert(1)</script>"
	var output bytes.Buffer
	if err := writePlainTextMail(&output, from, []*mail.Address{recipient}, "new SMS", body); err != nil {
		t.Fatal(err)
	}
	message := output.String()
	if strings.Contains(message, body) || strings.Contains(message, "\r\nBcc: injected@example.com") {
		t.Fatalf("unencoded body reached message: %q", message)
	}
	if !strings.Contains(message, "Content-Transfer-Encoding: base64") {
		t.Fatalf("base64 transfer encoding missing: %q", message)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(body))
	if !strings.Contains(strings.ReplaceAll(message, "\r\n", ""), encoded) {
		t.Fatalf("encoded body missing: %q", message)
	}
}

func TestWritePlainTextMailRejectsInjectedSubject(t *testing.T) {
	from := &mail.Address{Address: "alerts@example.com"}
	recipients := []*mail.Address{{Address: "admin@example.com"}}
	if err := writePlainTextMail(&bytes.Buffer{}, from, recipients, "hello\r\nBcc: x@example.com", "body"); err == nil {
		t.Fatal("injected subject was accepted")
	}
}

func TestWritePlainTextMailRejectsDirectlyConstructedInjectedAddresses(t *testing.T) {
	tests := []struct {
		name       string
		from       *mail.Address
		recipients []*mail.Address
	}{
		{
			name:       "sender address",
			from:       &mail.Address{Address: "alerts@example.com\r\nBcc: injected@example.com"},
			recipients: []*mail.Address{{Address: "admin@example.com"}},
		},
		{
			name:       "sender display name",
			from:       &mail.Address{Name: "Alerts\r\nBcc: injected@example.com", Address: "alerts@example.com"},
			recipients: []*mail.Address{{Address: "admin@example.com"}},
		},
		{
			name:       "recipient address",
			from:       &mail.Address{Address: "alerts@example.com"},
			recipients: []*mail.Address{{Address: "admin@example.com\nCc: injected@example.com"}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := writePlainTextMail(&bytes.Buffer{}, test.from, test.recipients, "subject", "body"); err == nil {
				t.Fatal("injected address was accepted")
			}
		})
	}
}
