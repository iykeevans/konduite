package log

import (
	"log/slog"
	"regexp"
	"strings"
)

// Redacted replaces any value the logger refuses to write.
const Redacted = "[REDACTED]"

var sensitiveKeyParts = []string{
	"authorization",
	"token",
	"password",
	"passwd",
	"secret",
	"cookie",
	"credential",
	"apikey",
	"privatekey",
}

var (
	authSchemeValue = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]+`)
	jwtValue        = regexp.MustCompile(`eyJ[A-Za-z0-9_-]*\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*`)
	urlPassword     = regexp.MustCompile(`(://[^:/@\s]+:)[^@\s/]+@`)
)

func redactAttr(groups []string, a slog.Attr) slog.Attr {
	if sensitiveKey(a.Key) {
		return slog.String(a.Key, Redacted)
	}
	for _, g := range groups {
		if sensitiveKey(g) {
			return slog.String(a.Key, Redacted)
		}
	}
	switch a.Value.Kind() {
	case slog.KindString:
		return slog.String(a.Key, scrub(a.Value.String()))
	case slog.KindAny:
		if err, ok := a.Value.Any().(error); ok && err != nil {
			return slog.String(a.Key, scrub(err.Error()))
		}
	}
	return a
}

func sensitiveKey(key string) bool {
	k := strings.Map(func(r rune) rune {
		switch r {
		case '-', '_', '.', ' ':
			return -1
		}
		return r
	}, strings.ToLower(key))
	for _, part := range sensitiveKeyParts {
		if strings.Contains(k, part) {
			return true
		}
	}
	return false
}

func scrub(s string) string {
	s = authSchemeValue.ReplaceAllString(s, "$1 "+Redacted)
	s = jwtValue.ReplaceAllString(s, Redacted)
	return urlPassword.ReplaceAllString(s, "${1}"+Redacted+"@")
}
