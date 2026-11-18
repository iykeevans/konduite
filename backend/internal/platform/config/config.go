// Package config loads process configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"time"
)

// Config is the configuration shared by the API and worker processes.
type Config struct {
	Env             string        // APP_ENV: development | test | production
	HTTPAddr        string        // API_ADDR
	ShutdownTimeout time.Duration // API_SHUTDOWN_TIMEOUT
	DatabaseURL     string        // DATABASE_URL (required)

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
	}
	if c.DatabaseURL == "" {
		problems = append(problems, fmt.Errorf("DATABASE_URL: required (local Supabase: postgresql://postgres:postgres@127.0.0.1:54322/postgres)"))
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

func duration(raw, key string, problems *[]error) time.Duration {
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		*problems = append(*problems, fmt.Errorf("%s: must be a positive duration such as 15s", key))
	}
	return d
}
