package sso

import (
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

var ambiguousPathEscape = regexp.MustCompile(`(?i)%(?:2f|5c|2e|25|0[0-9a-f]|1[0-9a-f]|7f)`)

// SafeRedirect accepts an already parsed redirect parameter. It never decodes
// query values a second time and preserves the original query and fragment.
func SafeRedirect(value, origin string) string {
	// Keep the authority check explicit: browsers treat both // and /\ as
	// network-path references, so a leading slash alone is not sufficient.
	if len(value) == 0 || len(value) > 4096 || value[0] != '/' ||
		(len(value) > 1 && (value[1] == '/' || value[1] == '\\')) {
		return "/"
	}
	for _, character := range value {
		if character == '\\' || character <= 0x20 || character == 0x7f {
			return "/"
		}
	}
	// URL.Parse does not validate percent escapes inside RawQuery or Fragment.
	if _, err := url.PathUnescape(value); err != nil {
		return "/"
	}
	path := strings.SplitN(strings.SplitN(value, "?", 2)[0], "#", 2)[0]
	if ambiguousPathEscape.MatchString(path) {
		return "/"
	}
	if origin == "" {
		origin = "https://vocat.invalid"
	}
	base, err := url.Parse(origin)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "/"
	}
	reference, err := url.Parse(value)
	if err != nil || reference.IsAbs() || reference.Host != "" || reference.User != nil || !utf8.ValidString(reference.Path) {
		return "/"
	}
	resolved := base.ResolveReference(reference)
	if resolved.Scheme != base.Scheme || resolved.Host != base.Host || strings.HasPrefix(resolved.Path, "//") {
		return "/"
	}
	// Go's ResolveReference collapses repeated slashes after dot segments;
	// browsers preserve them. Keep empty segments when checking the browser path.
	segments := make([]string, 0)
	for _, segment := range strings.Split(reference.Path, "/") {
		switch segment {
		case ".":
			continue
		case "..":
			if len(segments) > 1 {
				segments = segments[:len(segments)-1]
			}
		default:
			segments = append(segments, segment)
		}
	}
	resolvedPath := strings.ToLower(strings.Join(segments, "/"))
	if strings.HasPrefix(resolvedPath, "//") {
		return "/"
	}
	if resolvedPath == "/login" || strings.HasPrefix(resolvedPath, "/login/") || resolvedPath == "/api/auth" || strings.HasPrefix(resolvedPath, "/api/auth/") {
		return "/"
	}
	return value
}
