package config_test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/iykeevans/konduite/backend/internal/platform/config"
)

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

const (
	dbURL       = "postgres://app@127.0.0.1:54322/konduite"
	supabaseURL = "http://127.0.0.1:54321"
)

func TestLoadUsesDefaults(t *testing.T) {
	c, err := config.Load(env(map[string]string{"DATABASE_URL": dbURL, "SUPABASE_URL": supabaseURL}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Env != "development" || c.HTTPAddr != ":8080" || c.ShutdownTimeout != 15*time.Second || c.DatabaseURL != dbURL ||
		c.WorkerDrainTimeout != 30*time.Second || c.SupabaseURL != supabaseURL || c.NATSURL != "nats://127.0.0.1:4222" {
		t.Errorf("config = %+v, want development, :8080, 15s, the given DATABASE_URL, 30s drain and local NATS", c)
	}
}

func TestLoadReadsOverrides(t *testing.T) {
	c, err := config.Load(env(map[string]string{
		"APP_ENV": "production", "API_ADDR": ":9000", "API_SHUTDOWN_TIMEOUT": "30s", "DATABASE_URL": dbURL,
		"SUPABASE_URL":              "https://abc.supabase.co",
		"WORKER_DRAIN_TIMEOUT":      "1m",
		"RUNTIME_TOKEN_SIGNING_KEY": base64.StdEncoding.EncodeToString(make([]byte, ed25519.SeedSize)),
		"NATS_URL":                  "tls://nats.internal:4222",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Env != "production" || c.HTTPAddr != ":9000" || c.ShutdownTimeout != 30*time.Second || c.WorkerDrainTimeout != time.Minute ||
		c.SupabaseURL != "https://abc.supabase.co" || c.NATSURL != "tls://nats.internal:4222" {
		t.Errorf("config = %+v, want production, :9000, 30s, 1m drain and the given NATS_URL", c)
	}
}

func TestLoadReportsEveryProblemAtOnce(t *testing.T) {
	_, err := config.Load(env(map[string]string{
		"APP_ENV": "staging", "API_SHUTDOWN_TIMEOUT": "soon", "WORKER_DRAIN_TIMEOUT": "-5s", "NATS_URL": "localhost:4222",
	}))
	if err == nil {
		t.Fatal("want an error")
	}
	for _, name := range []string{"APP_ENV", "API_SHUTDOWN_TIMEOUT", "DATABASE_URL", "WORKER_DRAIN_TIMEOUT", "SUPABASE_URL", "NATS_URL"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not mention %s", err, name)
		}
	}
}

func TestLoadRejectsASupabaseURLThatIsNotAnHTTPOrigin(t *testing.T) {
	for _, bad := range []string{"127.0.0.1:54321", "ftp://abc.supabase.co", "http://", "not a url"} {
		_, err := config.Load(env(map[string]string{"DATABASE_URL": dbURL, "SUPABASE_URL": bad}))
		if err == nil || !strings.Contains(err.Error(), "SUPABASE_URL") {
			t.Errorf("SUPABASE_URL=%q: err = %v, want a SUPABASE_URL problem", bad, err)
		}
	}
}

func TestLoadReadsTheRuntimeTokenSigningKey(t *testing.T) {
	seed := bytes.Repeat([]byte{1}, ed25519.SeedSize)
	encoded := base64.StdEncoding.EncodeToString(seed)
	c, err := config.Load(env(map[string]string{
		"DATABASE_URL": dbURL, "SUPABASE_URL": supabaseURL, "RUNTIME_TOKEN_SIGNING_KEY": encoded,
	}))
	if err != nil {
		t.Fatal(err)
	}
	key, ok := c.RuntimeTokenKey.Key()
	if !ok || !bytes.Equal(key.Seed(), seed) {
		t.Fatalf("key loaded = %v, want the private key for the given seed", ok)
	}
	js, _ := json.Marshal(c)
	for _, out := range []string{fmt.Sprintf("%v %+v %#v", c, c, c), string(js)} {
		if strings.Contains(out, fmt.Sprintf("%d", []byte(key))) || strings.Contains(out, fmt.Sprintf("%#v", []byte(key))) ||
			strings.Contains(out, encoded) || strings.Contains(out, base64.StdEncoding.EncodeToString(key)) {
			t.Errorf("formatted config reveals the signing key: %s", out)
		}
	}
}

func TestRuntimeTokenSigningKeyIsOptionalOutsideProduction(t *testing.T) {
	c, err := config.Load(env(map[string]string{"DATABASE_URL": dbURL, "SUPABASE_URL": supabaseURL}))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.RuntimeTokenKey.Key(); ok {
		t.Error("a key was loaded from nowhere")
	}
}

func TestRuntimeTokenSigningKeyProblems(t *testing.T) {
	for name, vars := range map[string]map[string]string{
		"missing in production": {"APP_ENV": "production"},
		"not base64":            {"RUNTIME_TOKEN_SIGNING_KEY": "not base64!"},
		"wrong length":          {"RUNTIME_TOKEN_SIGNING_KEY": base64.StdEncoding.EncodeToString([]byte("short"))},
	} {
		t.Run(name, func(t *testing.T) {
			vars["DATABASE_URL"], vars["SUPABASE_URL"] = dbURL, supabaseURL
			_, err := config.Load(env(vars))
			if err == nil || !strings.Contains(err.Error(), "RUNTIME_TOKEN_SIGNING_KEY") {
				t.Errorf("err = %v, want a RUNTIME_TOKEN_SIGNING_KEY problem", err)
			}
		})
	}
}

func TestLoadRejectsANATSURLWithoutANATSScheme(t *testing.T) {
	for _, bad := range []string{"127.0.0.1:4222", "http://127.0.0.1:4222", "nats://", "not a url"} {
		_, err := config.Load(env(map[string]string{"DATABASE_URL": dbURL, "SUPABASE_URL": supabaseURL, "NATS_URL": bad}))
		if err == nil || !strings.Contains(err.Error(), "NATS_URL") {
			t.Errorf("NATS_URL=%q: err = %v, want a NATS_URL problem", bad, err)
		}
	}
}

func TestLoadKeepsNATSCredentialsOutOfErrors(t *testing.T) {
	withCreds := (&url.URL{Scheme: "http", User: url.UserPassword("svc", "s3cret"), Host: "nats:4222"}).String()
	_, err := config.Load(env(map[string]string{"DATABASE_URL": dbURL, "SUPABASE_URL": supabaseURL, "NATS_URL": withCreds}))
	if err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Errorf("err = %v, want a NATS_URL problem that does not echo the URL", err)
	}
}
