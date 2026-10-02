package server

import (
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"html/template"
	"strings"
	"time"

	"vocat/internal/store"
)

// These trusted, compiled-in assets are the only active content in an archive.
// No message text, identifiers, or query parameters are interpolated into them.
//
//go:embed sms_export.html
var smsExportHTMLSource string

//go:embed sms_export.css
var smsExportCSS string

//go:embed sms_export.js
var smsExportScript string

var smsExportHTMLPolicy = func() string {
	scriptHash := sha256.Sum256([]byte(smsExportScript))
	return "default-src 'none'; script-src 'sha256-" + base64.StdEncoding.EncodeToString(scriptHash[:]) + "'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'"
}()

var smsExportHTML = template.Must(template.New("sms-export").Funcs(template.FuncMap{
	"timestamp":    func(t time.Time) string { return t.Format(time.RFC3339) },
	"readableTime": func(t time.Time) string { return t.Format("2006/01/02 15:04:05 Z07:00") },
	"incoming": func(direction string) bool {
		d := strings.ToLower(direction)
		return d == "inbound" || d == "received" || d == "in"
	},
	"scope": func(value string) string {
		if strings.TrimSpace(value) == "" {
			return "全部 / All"
		}
		return value
	},
	"policy": func() string { return smsExportHTMLPolicy },
	// Only the embedded source above may bypass template escaping, never SMS data.
	"styles":        func() template.CSS { return template.CSS(smsExportCSS) },
	"script":        func() template.JS { return template.JS(smsExportScript) },
	"deliveryClass": func(m store.SMSMessage) string { class, _, _ := smsExportDelivery(m); return class },
	"deliveryMark":  func(m store.SMSMessage) string { _, mark, _ := smsExportDelivery(m); return mark },
	"deliveryTitle": func(m store.SMSMessage) string { _, _, title := smsExportDelivery(m); return title },
}).Parse(smsExportHTMLSource))

// Match web/src/components/sms/smsText.ts: modem/IMS acceptance is not delivery.
func smsExportDelivery(m store.SMSMessage) (class, mark, title string) {
	delivery, status := strings.ToLower(m.DeliveryState), strings.ToLower(m.Status)
	if delivery == "delivered" || delivery == "delivery_confirmed" {
		return "delivered", "✓✓", "已送达"
	}
	if strings.Contains(delivery, "failed") || strings.Contains(delivery, "rejected") || strings.Contains(status, "failed") || strings.Contains(status, "rejected") || strings.Contains(status, "partial") {
		return "failed", "!", "发送失败"
	}
	if strings.Contains(delivery, "accepted") || strings.Contains(status, "accepted_by_modem") || strings.Contains(status, "accepted_by_ims") {
		return "pending", "✓", "已提交，送达未确认"
	}
	return "unknown", "?", "送达状态未知"
}
