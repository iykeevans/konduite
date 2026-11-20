package log_test

import (
	"strings"
	"testing"

	"github.com/iykeevans/konduite/backend/internal/platform/log"
)

func TestSensitiveKey(t *testing.T) {
	tests := map[string]bool{
		"token":         true,
		"Access-Token":  true,
		"client_secret": true,
		"API_KEY":       true,
		"Authorization": true,
		"runtime_id":    false,
		"name":          false,
	}
	for key, want := range tests {
		if got := log.SensitiveKey(key); got != want {
			t.Errorf("SensitiveKey(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestSecretValue(t *testing.T) {
	tests := map[string]bool{
		"Bearer abc.def":                          true,
		"basic dXNlcjpwYXNz":                      true,
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2": true,
		"postgres://u:hunter2@db/x":               true,
		"postgres://db/x":                         false,
		"hello world":                             false,
		"":                                        false,
	}
	for value, want := range tests {
		if got := log.SecretValue(value); got != want {
			t.Errorf("SecretValue(%q) = %v, want %v", value, got, want)
		}
	}
}

func TestScrubMasksCredentialValues(t *testing.T) {
	got := log.Scrub("dial nats://u:hunter2@host failed, sent Bearer abc.def")
	if strings.Contains(got, "hunter2") || strings.Contains(got, "abc.def") {
		t.Errorf("Scrub left a credential: %q", got)
	}
}
