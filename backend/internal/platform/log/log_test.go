package log_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/iykeevans/konduite/backend/internal/platform/log"
)

func decode(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("log line is not JSON: %v (%q)", err, buf.String())
	}
	return entry
}

func TestRedactsSensitiveKeys(t *testing.T) {
	tests := []struct {
		key  string
		want string
	}{
		{"authorization", log.Redacted},
		{"Authorization", log.Redacted},
		{"token", log.Redacted},
		{"access_token", log.Redacted},
		{"refreshToken", log.Redacted},
		{"password", log.Redacted},
		{"DB_PASSWORD", log.Redacted},
		{"passwd", log.Redacted},
		{"secret", log.Redacted},
		{"client_secret", log.Redacted},
		{"cookie", log.Redacted},
		{"Set-Cookie", log.Redacted},
		{"refresh_credential", log.Redacted},
		{"api_key", log.Redacted},
		{"X-API-Key", log.Redacted},
		{"apikey", log.Redacted},
		{"private_key", log.Redacted},
		{"user_id", "s3cr3t-value"},
		{"path", "s3cr3t-value"},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			var buf bytes.Buffer
			log.New(&buf, slog.LevelInfo).Info("hello", tt.key, "s3cr3t-value")

			if got := decode(t, &buf)[tt.key]; got != tt.want {
				t.Errorf("%s = %v, want %q", tt.key, got, tt.want)
			}
		})
	}
}

func TestRedactsNonStringValuesUnderSensitiveKeys(t *testing.T) {
	var buf bytes.Buffer
	log.New(&buf, slog.LevelInfo).Info("hello", "token", 12345)

	if got := decode(t, &buf)["token"]; got != log.Redacted {
		t.Errorf("token = %v, want %q", got, log.Redacted)
	}
}

func TestRedactsInsideSensitiveGroups(t *testing.T) {
	var buf bytes.Buffer
	log.New(&buf, slog.LevelInfo).Info("hello", slog.Group("credentials", slog.String("value", "s3cr3t")))

	if strings.Contains(buf.String(), "s3cr3t") {
		t.Errorf("group member leaked: %s", buf.String())
	}
}

func TestRedactsSensitiveKeysNestedInGroups(t *testing.T) {
	var buf bytes.Buffer
	log.New(&buf, slog.LevelInfo).Info("hello",
		slog.Group("request", slog.String("authorization", "s3cr3t"), slog.String("method", "GET")))

	req, _ := decode(t, &buf)["request"].(map[string]any)
	if req["authorization"] != log.Redacted {
		t.Errorf("request.authorization = %v, want %q", req["authorization"], log.Redacted)
	}
	if req["method"] != "GET" {
		t.Errorf("request.method = %v, want GET", req["method"])
	}
}

func TestRedactsSecretLookingValues(t *testing.T) {
	jwt := "eyJhbGciOiJFUzI1NiJ9.eyJzdWIiOiIxMjMifQ.c2lnbmF0dXJl"
	tests := []struct {
		name  string
		value any
		leak  string
		keep  string
	}{
		{"bearer header", "Bearer abc.def-ghi", "abc.def-ghi", "Bearer"},
		{"lowercase bearer in prose", "upstream said: bearer abc123xyz rejected", "abc123xyz", "rejected"},
		{"bare jwt", "token was " + jwt, jwt, "token was"},
		{"jwt in error", errors.New("verify " + jwt + ": expired"), jwt, "expired"},
		{"basic auth", "Basic dXNlcjpwYXNz", "dXNlcjpwYXNz", "Basic"},
		{"password in url", "postgres://app:hunter2@db:5432/konduite", "hunter2", "db:5432"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			log.New(&buf, slog.LevelInfo).Info("hello", "detail", tt.value)

			got, _ := decode(t, &buf)["detail"].(string)
			if strings.Contains(got, tt.leak) {
				t.Errorf("detail = %q, leaks %q", got, tt.leak)
			}
			if !strings.Contains(got, tt.keep) {
				t.Errorf("detail = %q, lost context %q", got, tt.keep)
			}
		})
	}
}

func TestRedactsSecretsInMessage(t *testing.T) {
	var buf bytes.Buffer
	log.New(&buf, slog.LevelInfo).Info("calling with Bearer abc123xyz")

	if strings.Contains(buf.String(), "abc123xyz") {
		t.Errorf("message leaks token: %s", buf.String())
	}
}

func TestLeavesOrdinaryValuesAlone(t *testing.T) {
	var buf bytes.Buffer
	log.New(&buf, slog.LevelInfo).Info("api listening", "addr", "127.0.0.1:8080", "count", 3)

	entry := decode(t, &buf)
	if entry["msg"] != "api listening" || entry["addr"] != "127.0.0.1:8080" || entry["count"] != float64(3) {
		t.Errorf("entry = %v", entry)
	}
}

func TestAddsIDsFromContext(t *testing.T) {
	var buf bytes.Buffer
	ctx := log.WithCorrelationID(log.WithRequestID(context.Background(), "req-1"), "corr-1")

	log.New(&buf, slog.LevelInfo).With("component", "test").WithGroup("g").InfoContext(ctx, "hello", "k", "v")

	entry := decode(t, &buf)
	if entry["request_id"] != "req-1" {
		t.Errorf("request_id = %v, want req-1", entry["request_id"])
	}
	if entry["correlation_id"] != "corr-1" {
		t.Errorf("correlation_id = %v, want corr-1", entry["correlation_id"])
	}
}

func TestOmitsIDsWhenContextHasNone(t *testing.T) {
	var buf bytes.Buffer
	log.New(&buf, slog.LevelInfo).InfoContext(context.Background(), "hello")

	entry := decode(t, &buf)
	if _, ok := entry["request_id"]; ok {
		t.Errorf("request_id present without one in context: %v", entry)
	}
	if _, ok := entry["correlation_id"]; ok {
		t.Errorf("correlation_id present without one in context: %v", entry)
	}
}

func TestRespectsLevel(t *testing.T) {
	var buf bytes.Buffer
	log.New(&buf, slog.LevelWarn).Info("quiet")

	if buf.Len() != 0 {
		t.Errorf("info logged at warn level: %s", buf.String())
	}
}
