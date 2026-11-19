// Package config loads process configuration from environment variables.
package config

import (
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
}

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

	if len(problems) > 0 {
		return Config{}, fmt.Errorf("invalid configuration: %w", errors.Join(problems...))
	}
	return c, nil
}

var natsSchemes = map[string]bool{"nats": true, "tls": true, "ws": true, "wss": true}

func duration(raw, key string, problems *[]error) time.Duration {
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		*problems = append(*problems, fmt.Errorf("%s: must be a positive duration such as 15s", key))
	}
	return d
}
