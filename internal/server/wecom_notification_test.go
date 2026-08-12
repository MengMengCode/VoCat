package server

import (
	"errors"
	"net/http"
	"testing"
)

func TestRenderWecomPayloadEscapesTemplateValues(t *testing.T) {
	payload, err := renderWecomPayload(
		`{"msgtype":"text","text":{"content":{{message}},"number":{{number}}}}`,
		wecomTemplateValues{
			"message": "quote: \"\nline",
			"number":  "+447386",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(payload), `{"msgtype":"text","text":{"content":"quote: \"\nline","number":"+447386"}}`; got != want {
		t.Fatalf("payload = %s, want %s", got, want)
	}
}

func TestRenderWecomPayloadRejectsInvalidTemplate(t *testing.T) {
	for _, template := range []string{
		`{"text":{{unknown}}}`,
		`[]`,
		`{"msgtype":"text"`,
	} {
		t.Run(template, func(t *testing.T) {
			if _, err := renderWecomPayload(template, wecomTemplateValues{}); err == nil {
				t.Fatalf("template %q was accepted", template)
			}
		})
	}
}

func TestValidateWecomResponse(t *testing.T) {
	if err := validateWecomResponse(http.StatusOK, []byte(`{"errcode":0,"errmsg":"ok"}`)); err != nil {
		t.Fatalf("successful response = %v", err)
	}
	for _, response := range []struct {
		status int
		body   string
	}{
		{http.StatusBadGateway, `{"errcode":0}`},
		{http.StatusOK, `{"errcode":40058,"errmsg":"invalid"}`},
		{http.StatusOK, `{}`},
		{http.StatusOK, `not-json`},
	} {
		if err := validateWecomResponse(response.status, []byte(response.body)); !errors.Is(err, errProviderRejected) {
			t.Fatalf("validateWecomResponse(%d, %s) = %v", response.status, response.body, err)
		}
	}
}
