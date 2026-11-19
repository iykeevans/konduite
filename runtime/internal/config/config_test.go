package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func envFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestPath(t *testing.T) {
	tests := []struct {
		name string
		flag string
		env  map[string]string
		want string
	}{
		{"home default", "", map[string]string{"HOME": "/home/ada"}, "/home/ada/.config/agenthub/config.yaml"},
		{"xdg over home", "", map[string]string{"HOME": "/home/ada", "XDG_CONFIG_HOME": "/xdg"}, "/xdg/agenthub/config.yaml"},
		{"env over xdg", "", map[string]string{"HOME": "/home/ada", "XDG_CONFIG_HOME": "/xdg", "AGENTHUB_CONFIG": "/etc/ah.yaml"}, "/etc/ah.yaml"},
		{"flag over env", "/flag.yaml", map[string]string{"HOME": "/home/ada", "AGENTHUB_CONFIG": "/etc/ah.yaml"}, "/flag.yaml"},
		{"relative xdg ignored", "", map[string]string{"HOME": "/home/ada", "XDG_CONFIG_HOME": "rel"}, "/home/ada/.config/agenthub/config.yaml"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Path(tt.flag, envFrom(tt.env))
			if err != nil {
				t.Fatalf("Path: %v", err)
			}
			if got != filepath.FromSlash(tt.want) {
				t.Errorf("Path = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPathWithoutHome(t *testing.T) {
	if _, err := Path("", envFrom(nil)); err == nil {
		t.Fatal("Path with no HOME, XDG_CONFIG_HOME or override: want error")
	}
}

func TestDefaults(t *testing.T) {
	d := Default()
	if d.ServerURL == "" || d.LogLevel != "info" || d.RuntimeID != "" {
		t.Errorf("Default = %+v", d)
	}
	if err := d.Validate(); err != nil {
		t.Errorf("Default does not validate: %v", err)
	}
}

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope", "config.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg != Default() {
		t.Errorf("Load = %+v, want defaults %+v", cfg, Default())
	}
}

func TestLoadPartialFileKeepsDefaults(t *testing.T) {
	cfg, err := Load(writeFile(t, "log_level: warn\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LogLevel != "warn" || cfg.ServerURL != Default().ServerURL {
		t.Errorf("Load = %+v", cfg)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agenthub", "config.yaml")
	want := Config{ServerURL: "https://api.example.com", RuntimeID: "7f9c2f0e-1b2a-4c55-9d1e-3a8f0b6c2d11", LogLevel: "debug"}

	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != want {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

func TestSavePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permissions")
	}
	dir := filepath.Join(t.TempDir(), "agenthub")
	path := filepath.Join(dir, "config.yaml")
	if err := Save(path, Default()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	assertMode(t, dir, 0o700)
	assertMode(t, path, 0o600)

	if err := os.Chmod(path, 0o644); err != nil { //nolint:gosec // loosened on purpose to check Save tightens it
		t.Fatal(err)
	}
	if err := Save(path, Default()); err != nil {
		t.Fatalf("Save over existing: %v", err)
	}
	assertMode(t, path, 0o600)
}

func TestLoadRejectsSecrets(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"token", "server_url: https://x.example\naccess_token: abc123\n"},
		{"refresh token", "refresh_token: abc123\n"},
		{"password", "Password: hunter2\n"},
		{"api key", "api_key: abc123\n"},
		{"apikey camel", "apiKey: abc123\n"},
		{"secret", "client_secret: abc123\n"},
		{"private key", "private_key: abc123\n"},
		{"credentials", "credentials: abc123\n"},
		{"nested", "auth:\n  token: abc123\n"},
		{"in list", "things:\n  - session: abc123\n"},
		{"userinfo in url", "server_url: https://ada:hunter2@api.example.com\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeFile(t, tt.body))
			if !errors.Is(err, ErrSecret) {
				t.Fatalf("Load err = %v, want ErrSecret", err)
			}
			if strings.Contains(err.Error(), "abc123") || strings.Contains(err.Error(), "hunter2") {
				t.Errorf("error leaks the secret value: %v", err)
			}
		})
	}
}

func TestLoadRejectsInvalid(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"unknown key", "colour: blue\n"},
		{"bad log level", "log_level: loud\n"},
		{"bad scheme", "server_url: ftp://x.example\n"},
		{"no host", "server_url: https://\n"},
		{"not yaml", "server_url: [\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Load(writeFile(t, tt.body)); err == nil {
				t.Fatal("Load: want error")
			}
		})
	}
}

func TestSaveRefusesSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := Default()
	cfg.ServerURL = "https://ada:hunter2@api.example.com"
	if err := Save(path, cfg); !errors.Is(err, ErrSecret) {
		t.Fatalf("Save err = %v, want ErrSecret", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Save wrote a file despite the error")
	}
}

func TestSet(t *testing.T) {
	tests := []struct {
		key, value string
		wantErr    error
		check      func(Config) bool
	}{
		{key: "server_url", value: "https://api.example.com", check: func(c Config) bool { return c.ServerURL == "https://api.example.com" }},
		{key: "runtime_id", value: "abc", check: func(c Config) bool { return c.RuntimeID == "abc" }},
		{key: "log_level", value: "error", check: func(c Config) bool { return c.LogLevel == "error" }},
		{key: "access_token", value: "abc", wantErr: ErrSecret},
		{key: "password", value: "abc", wantErr: ErrSecret},
		{key: "colour", value: "blue", wantErr: ErrUnknownKey},
		{key: "log_level", value: "loud", wantErr: ErrInvalid},
		{key: "server_url", value: "https://ada:pw@x.example", wantErr: ErrSecret}, //nolint:gosec // fake credential that Set must refuse
	}
	for _, tt := range tests {
		t.Run(tt.key+"="+tt.value, func(t *testing.T) {
			cfg := Default()
			err := cfg.Set(tt.key, tt.value)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Set err = %v, want %v", err, tt.wantErr)
				}
				if cfg != Default() {
					t.Errorf("failed Set changed config: %+v", cfg)
				}
				return
			}
			if err != nil {
				t.Fatalf("Set: %v", err)
			}
			if !tt.check(cfg) {
				t.Errorf("Set did not apply: %+v", cfg)
			}
		})
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != want {
		t.Errorf("%s mode = %o, want %o", path, got, want)
	}
}

func writeFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
