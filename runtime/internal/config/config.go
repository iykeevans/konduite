// Package config loads and saves the runtime's non-secret settings file.
// Credentials belong in the OS keychain, so the loader refuses secret-looking keys.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Errors returned by Load, Save and Set; match them with errors.Is.
var (
	ErrSecret     = errors.New("secrets must not be stored in the config file; they belong in the OS keychain")
	ErrUnknownKey = errors.New("unknown config key")
	ErrInvalid    = errors.New("invalid config value")
)

// Config is the runtime's on-disk settings. It must never gain a credential field.
type Config struct {
	ServerURL              string `yaml:"server_url"`
	RuntimeID              string `yaml:"runtime_id,omitempty"`
	WorkspaceID            string `yaml:"workspace_id,omitempty"`
	LogLevel               string `yaml:"log_level"`
	SupabaseURL            string `yaml:"supabase_url,omitempty"`
	SupabasePublishableKey string `yaml:"supabase_publishable_key,omitempty"`
	AuthProvider           string `yaml:"auth_provider,omitempty"`
}

// Keys lists the settings Set accepts.
const Keys = "server_url, runtime_id, workspace_id, log_level, supabase_url, supabase_publishable_key, auth_provider"

// Default is the configuration used when no file exists.
func Default() Config {
	return Config{
		ServerURL: "http://localhost:8080",
		LogLevel:  "info",
	}
}

// Path resolves the config file: flag, then AGENTHUB_CONFIG, then
// $XDG_CONFIG_HOME/agenthub/config.yaml, then ~/.config/agenthub/config.yaml.
// macOS deliberately gets ~/.config too, not os.UserConfigDir's Library path.
func Path(flag string, getenv func(string) string) (string, error) {
	if flag != "" {
		return filepath.Clean(flag), nil
	}
	if p := getenv("AGENTHUB_CONFIG"); p != "" {
		return filepath.Clean(p), nil
	}
	if xdg := getenv("XDG_CONFIG_HOME"); xdg != "" && filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "agenthub", "config.yaml"), nil
	}
	home := getenv("HOME")
	if home == "" {
		home = getenv("USERPROFILE")
	}
	if home == "" {
		return "", errors.New("cannot locate config: HOME is not set; pass --config or set AGENTHUB_CONFIG")
	}
	return filepath.Join(home, ".config", "agenthub", "config.yaml"), nil
}

// Load reads path over the defaults. A missing file yields the defaults.
func Load(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path) // #nosec G304 -- the path is chosen by the local user
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}

	var raw yaml.Node
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	if key, ok := findSecretKey(&raw); ok {
		return Config{}, fmt.Errorf("config %s: key %q: %w", path, key, ErrSecret)
	}

	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("config %s: %w: %w", path, ErrInvalid, err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, nil
}

// Save writes cfg atomically: the directory is created 0700 and the file is always left 0600.
func Save(path string, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.yaml")
	if err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write config: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// Set changes one setting by its file key. On error cfg is unchanged.
func (c *Config) Set(key, value string) error {
	if isSecretKey(key) {
		return fmt.Errorf("key %q: %w", key, ErrSecret)
	}
	next := *c
	switch key {
	case "server_url":
		next.ServerURL = value
	case "runtime_id":
		next.RuntimeID = value
	case "workspace_id":
		next.WorkspaceID = value
	case "log_level":
		next.LogLevel = value
	case "supabase_url":
		next.SupabaseURL = value
	case "supabase_publishable_key":
		next.SupabasePublishableKey = value
	case "auth_provider":
		next.AuthProvider = value
	default:
		return fmt.Errorf("%w %q (known: %s)", ErrUnknownKey, key, Keys)
	}
	if err := next.Validate(); err != nil {
		return err
	}
	*c = next
	return nil
}

// Validate checks every field and returns ErrSecret for credentials embedded in server_url.
func (c Config) Validate() error {
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("%w: log_level must be one of debug, info, warn, error", ErrInvalid)
	}

	if err := validateURL("server_url", c.ServerURL); err != nil {
		return err
	}
	if c.SupabaseURL != "" {
		if err := validateURL("supabase_url", c.SupabaseURL); err != nil {
			return err
		}
	}
	// A Supabase secret key bypasses row-level security; only the publishable key belongs on a developer machine.
	if strings.HasPrefix(c.SupabasePublishableKey, "sb_secret_") {
		return fmt.Errorf("supabase_publishable_key is a Supabase secret key: %w", ErrSecret)
	}
	for _, r := range c.AuthProvider {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			return fmt.Errorf("%w: auth_provider must be a Supabase provider name such as github", ErrInvalid)
		}
	}
	return nil
}

func validateURL(key, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: %s is not a URL", ErrInvalid, key)
	}
	if u.User != nil {
		return fmt.Errorf("%s carries credentials: %w", key, ErrSecret)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: %s must use http or https", ErrInvalid, key)
	}
	if u.Host == "" {
		return fmt.Errorf("%w: %s has no host", ErrInvalid, key)
	}
	return nil
}

var secretMarkers = []string{
	"token", "secret", "password", "passwd", "passphrase", "credential",
	"apikey", "privatekey", "session", "cookie", "bearer", "jwt",
}

func isSecretKey(key string) bool {
	k := strings.ToLower(key)
	k = strings.NewReplacer("_", "", "-", "", ".", "", " ", "").Replace(k)
	for _, m := range secretMarkers {
		if strings.Contains(k, m) {
			return true
		}
	}
	return false
}

func findSecretKey(n *yaml.Node) (string, bool) {
	if n == nil {
		return "", false
	}
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			if k := n.Content[i].Value; isSecretKey(k) {
				return k, true
			}
		}
	}
	for _, c := range n.Content {
		if k, ok := findSecretKey(c); ok {
			return k, true
		}
	}
	return "", false
}
