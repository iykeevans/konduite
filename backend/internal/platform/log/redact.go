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
	if SensitiveKey(a.Key) {
		return slog.String(a.Key, Redacted)
	}
	for _, g := range groups {
		if SensitiveKey(g) {
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

// SensitiveKey reports whether a field named key holds a credential. Case and
// the separators - _ . and space are ignored, so api_key matches apikey.
func SensitiveKey(key string) bool {
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

// SecretValue reports whether s looks like a credential whatever its key: a
// bearer or basic auth value, a JWT, or a URL with a password.
func SecretValue(s string) bool {
	return authSchemeValue.MatchString(s) || jwtValue.MatchString(s) || urlPassword.MatchString(s)
}

func scrub(s string) string {
	s = authSchemeValue.ReplaceAllString(s, "$1 "+Redacted)
	s = jwtValue.ReplaceAllString(s, Redacted)
	return urlPassword.ReplaceAllString(s, "${1}"+Redacted+"@")
}
