// Package config loads process configuration from environment variables.
package config

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"time"
)

// Config is the configuration shared by the API and worker processes.
type Config struct {
	Env             string        // APP_ENV: development | test | production
	HTTPAddr        string        // API_ADDR
	ShutdownTimeout time.Duration // API_SHUTDOWN_TIMEOUT
	DatabaseURL     string        // DATABASE_URL (required)
	SupabaseURL     string        // SUPABASE_URL (required): project URL, gives the JWKS and token issuer
	NATSURL         string        // NATS_URL

	WorkerDrainTimeout time.Duration // WORKER_DRAIN_TIMEOUT

	RuntimeTokenKey SigningKey // RUNTIME_TOKEN_SIGNING_KEY: base64 Ed25519 seed (required in production)
}

// SigningKey holds a private key. It formats and marshals as "[redacted]" so
// logging a Config can't leak it.
type SigningKey struct {
	key ed25519.PrivateKey
}

// Key returns the private key, or false if none was configured.
func (k SigningKey) Key() (ed25519.PrivateKey, bool) { return k.key, k.key != nil }

// String hides the key from %v and %s.
func (SigningKey) String() string { return "[redacted]" }

// GoString hides the key from %#v.
func (k SigningKey) GoString() string { return k.String() }

// MarshalJSON hides the key from JSON log handlers.
func (k SigningKey) MarshalJSON() ([]byte, error) { return []byte(`"[redacted]"`), nil }

// Load reads configuration through getenv (os.Getenv in production) and
// reports every missing or invalid variable in a single error.
func Load(getenv func(string) string) (Config, error) {
	var problems []error
	or := func(key, def string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return def
	}

	c := Config{
		Env:         or("APP_ENV", "development"),
		HTTPAddr:    or("API_ADDR", ":8080"),
		DatabaseURL: getenv("DATABASE_URL"),
		SupabaseURL: getenv("SUPABASE_URL"),
		NATSURL:     or("NATS_URL", "nats://127.0.0.1:4222"),
	}
	if c.DatabaseURL == "" {
		problems = append(problems, fmt.Errorf("DATABASE_URL: required (local Supabase: postgresql://postgres:postgres@127.0.0.1:54322/postgres)"))
	}
	if u, err := url.Parse(c.SupabaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		problems = append(problems, fmt.Errorf("SUPABASE_URL: required, an http(s) URL such as http://127.0.0.1:54321 (local Supabase)"))
	}
	if u, err := url.Parse(c.NATSURL); err != nil || !natsSchemes[u.Scheme] || u.Host == "" {
		problems = append(problems, fmt.Errorf("NATS_URL: must be a nats://, tls://, ws:// or wss:// URL such as nats://127.0.0.1:4222"))
	}
	switch c.Env {
	case "development", "test", "production":
	default:
		problems = append(problems, fmt.Errorf("APP_ENV: %q is not one of development, test, production", c.Env))
	}
	c.ShutdownTimeout = duration(or("API_SHUTDOWN_TIMEOUT", "15s"), "API_SHUTDOWN_TIMEOUT", &problems)
	c.WorkerDrainTimeout = duration(or("WORKER_DRAIN_TIMEOUT", "30s"), "WORKER_DRAIN_TIMEOUT", &problems)
	c.RuntimeTokenKey = signingKey(getenv("RUNTIME_TOKEN_SIGNING_KEY"), c.Env, &problems)

	if len(problems) > 0 {
		return Config{}, fmt.Errorf("invalid configuration: %w", errors.Join(problems...))
	}
	return c, nil
}

func signingKey(raw, env string, problems *[]error) SigningKey {
	const help = "RUNTIME_TOKEN_SIGNING_KEY: must be a base64 32-byte Ed25519 seed (openssl rand -base64 32)"
	if raw == "" {
		if env == "production" {
			*problems = append(*problems, errors.New(help+", required in production"))
		}
		return SigningKey{}
	}
	seed, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(seed) != ed25519.SeedSize {
		*problems = append(*problems, errors.New(help))
		return SigningKey{}
	}
	return SigningKey{key: ed25519.NewKeyFromSeed(seed)}
}

var natsSchemes = map[string]bool{"nats": true, "tls": true, "ws": true, "wss": true}

func duration(raw, key string, problems *[]error) time.Duration {
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		*problems = append(*problems, fmt.Errorf("%s: must be a positive duration such as 15s", key))
	}
	return d
}
