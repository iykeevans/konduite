package auth_test

import (
	"encoding/base64"
	"strings"
	"testing"
)

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// tamper swaps the payload for a different one, keeping the original signature.
func tamper(t *testing.T, token string) string {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	parts[1] = b64([]byte(strings.Replace(string(payload), `"sub":"`, `"sub":"0`, 1)))
	return strings.Join(parts, ".")
}
