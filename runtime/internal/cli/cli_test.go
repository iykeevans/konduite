package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iykeevans/konduite/runtime/internal/config"
	"github.com/iykeevans/konduite/runtime/internal/version"
)

func run(t *testing.T, env map[string]string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := New(Options{
		Version: version.Info{Version: "1.2.3", Commit: "abc1234", Date: "2026-11-19T00:00:00Z"},
		Getenv:  func(k string) string { return env[k] },
	})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestVersion(t *testing.T) {
	out, err := run(t, nil, "version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	want := "agenthub 1.2.3 (commit abc1234, built 2026-11-19T00:00:00Z)\n"
	if out != want {
		t.Errorf("version output = %q, want %q", out, want)
	}
}

func TestVersionFlag(t *testing.T) {
	out, err := run(t, nil, "--version")
	if err != nil {
		t.Fatalf("--version: %v", err)
	}
	if !strings.Contains(out, "1.2.3") {
		t.Errorf("--version output = %q", out)
	}
}

func TestConfigPathPrecedence(t *testing.T) {
	env := map[string]string{"HOME": "/home/ada", "AGENTHUB_CONFIG": "/env.yaml"}

	out, err := run(t, env, "config", "path")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != filepath.FromSlash("/env.yaml") {
		t.Errorf("config path = %q, want env override", out)
	}

	out, err = run(t, env, "--config", "/flag.yaml", "config", "path")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != filepath.FromSlash("/flag.yaml") {
		t.Errorf("config path = %q, want flag override", out)
	}
}

func TestConfigSetAndShow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agenthub", "config.yaml")

	if _, err := run(t, nil, "--config", path, "config", "set", "server_url", "https://api.example.com"); err != nil {
		t.Fatalf("config set: %v", err)
	}
	out, err := run(t, nil, "--config", path, "config", "show")
	if err != nil {
		t.Fatalf("config show: %v", err)
	}
	if !strings.Contains(out, "server_url: https://api.example.com") || !strings.Contains(out, "log_level: info") {
		t.Errorf("config show = %q", out)
	}
}

func TestConfigSetRefusesSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	out, err := run(t, nil, "--config", path, "config", "set", "access_token", "s3cr3t")
	if !errors.Is(err, config.ErrSecret) {
		t.Fatalf("config set access_token err = %v, want ErrSecret", err)
	}
	if strings.Contains(out, "s3cr3t") || strings.Contains(err.Error(), "s3cr3t") {
		t.Errorf("secret value echoed back: %q / %v", out, err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("config file written despite refusal")
	}
}
