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
		Env:      or("APP_ENV", "development"),
		HTTPAddr: or("API_ADDR", ":8080"),
	}
	switch c.Env {
	case "development", "test", "production":
	default:
		problems = append(problems, fmt.Errorf("APP_ENV: %q is not one of development, test, production", c.Env))
	}
	d, err := time.ParseDuration(or("API_SHUTDOWN_TIMEOUT", "15s"))
	if err != nil || d <= 0 {
		problems = append(problems, fmt.Errorf("API_SHUTDOWN_TIMEOUT: must be a positive duration such as 15s"))
	}
	c.ShutdownTimeout = d

	if len(problems) > 0 {
		return Config{}, fmt.Errorf("invalid configuration: %w", errors.Join(problems...))
	}
	return c, nil
}
