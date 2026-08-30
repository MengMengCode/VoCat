package loghub

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode"
)

var (
	sipIdentityPattern        = regexp.MustCompile(`(?i)\b(sips?|tel):([^@;>,\s]+)(@[^;>,\s]+)?`)
	internationalPhonePattern = regexp.MustCompile(`(?:\+|00)[0-9][0-9 ()-]{5,}[0-9]`)
	longDigitsPattern         = regexp.MustCompile(`\b[0-9]{7,22}\b`)
	labeledIdentityPattern    = regexp.MustCompile(`(?i)\b(iccid|imsi|msisdn|imei|eid)\s*([=:])\s*([a-z0-9+_-]{7,})`)
)

// This switch is intentionally undocumented in the CLI and public
// configuration. It exists only for controlled developer/source audits where
// the normal identity redaction would hide the value being investigated.
const unsafeDisableRedactionEnv = "VOCAT_UNSAFE_DISABLE_LOG_REDACTION"

func redactionDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(unsafeDisableRedactionEnv))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// IsHTTPAccessEntry identifies legacy request-traffic entries. Access traffic
// is intentionally excluded from the user diagnostic log surface.
func IsHTTPAccessEntry(entry Entry) bool {
	if strings.EqualFold(strings.TrimSpace(entry.Message), "http request") {
		return true
	}
	category, _ := entry.Fields["category"].(string)
	return strings.EqualFold(strings.TrimSpace(category), "http_access")
}

// SanitizeEntry also protects records that were persisted by an older build
// before central redaction was introduced.
func SanitizeEntry(entry Entry) Entry {
	if redactionDisabled() {
		return entry
	}
	entry.Message = RedactString(entry.Message)
	entry.Caller = RedactString(entry.Caller)
	if entry.Fields != nil {
		entry.Fields = sanitizeMap(entry.Fields)
	}
	return entry
}

// RedactString masks common telecom identities while retaining enough of the
// suffix to correlate repeated events in an exported diagnostic log.
func RedactString(value string) string {
	if value == "" || redactionDisabled() {
		return value
	}
	value = labeledIdentityPattern.ReplaceAllStringFunc(value, func(match string) string {
		parts := labeledIdentityPattern.FindStringSubmatch(match)
		return parts[1] + parts[2] + maskToken(parts[3])
	})
	value = sipIdentityPattern.ReplaceAllStringFunc(value, func(match string) string {
		parts := sipIdentityPattern.FindStringSubmatch(match)
		domain := parts[3]
		return parts[1] + ":" + maskToken(parts[2]) + domain
	})
	value = internationalPhonePattern.ReplaceAllStringFunc(value, maskToken)
	return longDigitsPattern.ReplaceAllStringFunc(value, maskToken)
}

func sanitizeRecord(record slog.Record) slog.Record {
	if redactionDisabled() {
		return record
	}
	clean := slog.NewRecord(record.Time, record.Level, RedactString(record.Message), record.PC)
	record.Attrs(func(attr slog.Attr) bool {
		clean.AddAttrs(sanitizeAttr(attr))
		return true
	})
	return clean
}

func sanitizeAttrs(attrs []slog.Attr) []slog.Attr {
	if redactionDisabled() {
		return append([]slog.Attr(nil), attrs...)
	}
	clean := make([]slog.Attr, 0, len(attrs))
	for _, attr := range attrs {
		clean = append(clean, sanitizeAttr(attr))
	}
	return clean
}

func sanitizeAttr(attr slog.Attr) slog.Attr {
	if redactionDisabled() {
		return attr
	}
	attr.Value = attr.Value.Resolve()
	if attr.Equal(slog.Attr{}) {
		return attr
	}
	if sensitiveKey(attr.Key) {
		return slog.String(attr.Key, maskToken(valueText(attr.Value.Any())))
	}
	if attr.Value.Kind() == slog.KindGroup {
		children := attr.Value.Group()
		return slog.Group(attr.Key, attrsToAny(sanitizeAttrs(children))...)
	}
	switch attr.Value.Kind() {
	case slog.KindString:
		return slog.String(attr.Key, RedactString(attr.Value.String()))
	case slog.KindAny:
		return slog.Any(attr.Key, sanitizeAny(attr.Value.Any(), attr.Key))
	default:
		return attr
	}
}

func attrsToAny(attrs []slog.Attr) []any {
	values := make([]any, len(attrs))
	for index := range attrs {
		values[index] = attrs[index]
	}
	return values
}

func sanitizeAny(value any, key string) any {
	if value == nil || redactionDisabled() {
		return value
	}
	if sensitiveKey(key) {
		return maskToken(valueText(value))
	}
	switch typed := value.(type) {
	case error:
		return RedactString(typed.Error())
	case string:
		return RedactString(typed)
	case []byte:
		return RedactString(string(typed))
	case json.RawMessage:
		var decoded any
		if json.Unmarshal(typed, &decoded) == nil {
			return sanitizeAny(decoded, key)
		}
		return RedactString(string(typed))
	case map[string]any:
		return sanitizeMap(typed)
	case []any:
		result := make([]any, len(typed))
		for index := range typed {
			result[index] = sanitizeAny(typed[index], key)
		}
		return result
	case time.Time, time.Duration:
		return value
	}

	rv := reflect.ValueOf(value)
	if rv.IsValid() && (rv.Kind() == reflect.Map || rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array || rv.Kind() == reflect.Struct || rv.Kind() == reflect.Pointer) {
		if raw, err := json.Marshal(value); err == nil {
			var decoded any
			if json.Unmarshal(raw, &decoded) == nil {
				return sanitizeAny(decoded, key)
			}
		}
	}
	if stringer, ok := value.(fmt.Stringer); ok {
		return RedactString(stringer.String())
	}
	return value
}

func sanitizeMap(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = sanitizeAny(value, key)
	}
	return result
}

func sensitiveKey(key string) bool {
	normalized := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, key)
	if strings.Contains(normalized, "password") || strings.Contains(normalized, "passwd") ||
		strings.Contains(normalized, "secret") || strings.Contains(normalized, "token") ||
		strings.Contains(normalized, "cookie") || strings.Contains(normalized, "authorization") ||
		strings.Contains(normalized, "privateidentity") || strings.Contains(normalized, "publicidentity") ||
		strings.Contains(normalized, "associatednumber") || strings.Contains(normalized, "sipuri") {
		return true
	}
	switch normalized {
	case "iccid", "imsi", "imei", "eid", "supi", "suci", "msisdn", "phone", "phonenumber",
		"number", "caller", "called", "callee", "recipient", "peer", "from", "to":
		return true
	default:
		return false
	}
}

func valueText(value any) string {
	if value == nil {
		return ""
	}
	if err, ok := value.(error); ok {
		return err.Error()
	}
	return fmt.Sprint(value)
}

func maskToken(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "[REDACTED]"
	}
	runes := []rune(value)
	digits := make([]rune, 0, 4)
	for index := len(runes) - 1; index >= 0 && len(digits) < 4; index-- {
		if unicode.IsDigit(runes[index]) {
			digits = append(digits, runes[index])
		}
	}
	if len(digits) == 0 {
		return "[REDACTED]"
	}
	for left, right := 0, len(digits)-1; left < right; left, right = left+1, right-1 {
		digits[left], digits[right] = digits[right], digits[left]
	}
	return "[REDACTED:" + string(digits) + "]"
}
