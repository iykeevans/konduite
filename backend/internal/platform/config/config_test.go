package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/iykeevans/konduite/backend/internal/platform/config"
)

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestLoadUsesDefaults(t *testing.T) {
	c, err := config.Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Env != "development" || c.HTTPAddr != ":8080" || c.ShutdownTimeout != 15*time.Second {
		t.Errorf("config = %+v, want development, :8080, 15s", c)
	}
}

func TestLoadReadsOverrides(t *testing.T) {
	c, err := config.Load(env(map[string]string{
		"APP_ENV": "production", "API_ADDR": ":9000", "API_SHUTDOWN_TIMEOUT": "30s",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Env != "production" || c.HTTPAddr != ":9000" || c.ShutdownTimeout != 30*time.Second {
		t.Errorf("config = %+v, want production, :9000, 30s", c)
	}
}

func TestLoadReportsEveryProblemAtOnce(t *testing.T) {
	_, err := config.Load(env(map[string]string{"APP_ENV": "staging", "API_SHUTDOWN_TIMEOUT": "soon"}))
	if err == nil {
		t.Fatal("want an error")
	}
	for _, name := range []string{"APP_ENV", "API_SHUTDOWN_TIMEOUT"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not mention %s", err, name)
		}
	}
}
