package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/iykeevans/konduite/runtime/internal/auth/authtest"
	"github.com/iykeevans/konduite/runtime/internal/config"
	"github.com/iykeevans/konduite/runtime/internal/security"
	"github.com/iykeevans/konduite/runtime/internal/version"
)

type loginEnv struct {
	sb   *authtest.Supabase
	srv  *authtest.Server
	path string
	open func(string) error
}

func newLoginEnv(t *testing.T) *loginEnv {
	t.Helper()
	keyring.MockInit()
	sb := authtest.NewSupabase(t)
	e := &loginEnv{sb: sb, srv: authtest.NewServer(t, sb.AccessToken), path: filepath.Join(t.TempDir(), "config.yaml"), open: authtest.Browser(t)}
	for _, kv := range [][2]string{
		{"server_url", e.srv.URL},
		{"supabase_url", sb.URL},
		{"supabase_publishable_key", sb.PublishableKey},
		{"auth_provider", "github"},
	} {
		if _, err := e.run(t, "config", "set", kv[0], kv[1]); err != nil {
			t.Fatalf("config set %s: %v", kv[0], err)
		}
	}
	return e
}

func (e *loginEnv) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := New(Options{
		Version:     version.Info{Version: "1.2.3"},
		Getenv:      func(string) string { return "" },
		OpenBrowser: e.open,
	})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"--config", e.path}, args...))
	err := cmd.Execute()
	return out.String(), err
}

func (e *loginEnv) config(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.Load(e.path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestLoginRegistersTheRuntimeAndKeepsTheCredentialOutOfTheConfigFile(t *testing.T) {
	e := newLoginEnv(t)

	out, err := e.run(t, "login", "--workspace", e.srv.WorkspaceID, "--name", "ada-laptop")
	if err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}

	cfg := e.config(t)
	if cfg.RuntimeID != e.srv.RuntimeID || cfg.WorkspaceID != e.srv.WorkspaceID {
		t.Errorf("config = %+v, want the runtime and workspace ids saved", cfg)
	}
	raw, err := os.ReadFile(e.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{e.srv.Credential, e.sb.AccessToken, "krt_"} {
		if strings.Contains(string(raw), secret) || strings.Contains(out, secret) {
			t.Errorf("a secret reached the config file or the terminal")
		}
	}
	got, err := security.OSKeychain{}.Get(e.srv.URL)
	if err != nil || got != e.srv.Credential {
		t.Errorf("keychain = %q, %v, want the refresh credential", got, err)
	}
	if names := e.srv.Registered(); len(names) != 1 || names[0] != "ada-laptop" {
		t.Errorf("registered %v, want ada-laptop", names)
	}
	if !strings.Contains(out, e.srv.RuntimeID) {
		t.Errorf("output %q does not name the runtime", out)
	}
}

func TestLoginUsesTheSavedWorkspace(t *testing.T) {
	e := newLoginEnv(t)
	if _, err := e.run(t, "config", "set", "workspace_id", e.srv.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if out, err := e.run(t, "login", "--name", "laptop"); err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}
}

func TestWhoamiAndLogout(t *testing.T) {
	e := newLoginEnv(t)

	out, err := e.run(t, "whoami")
	if err != nil || !strings.Contains(out, "not logged in") {
		t.Errorf("whoami before login = %q, %v", out, err)
	}
	if out, err := e.run(t, "login", "--workspace", e.srv.WorkspaceID, "--name", "laptop"); err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}
	out, err = e.run(t, "whoami")
	if err != nil || !strings.Contains(out, e.srv.RuntimeID) || !strings.Contains(out, "OS keychain") || strings.Contains(out, e.srv.Credential) {
		t.Errorf("whoami after login = %q, %v; want the runtime id and no secret", out, err)
	}

	out, err = e.run(t, "logout")
	if err != nil {
		t.Fatalf("logout: %v\n%s", err, out)
	}
	if !strings.Contains(out, "revoked") {
		t.Errorf("logout output %q does not confirm the revoke", out)
	}
	if got := e.srv.Revoked(); len(got) != 1 {
		t.Errorf("server revoked %d credentials, want 1", len(got))
	}
	if _, err := (security.OSKeychain{}).Get(e.srv.URL); !errors.Is(err, security.ErrNotFound) {
		t.Errorf("keychain after logout: err = %v, want ErrNotFound", err)
	}
	if cfg := e.config(t); cfg.RuntimeID != "" {
		t.Errorf("runtime_id %q left in config after logout", cfg.RuntimeID)
	}
}

func TestLogoutClearsTheKeychainAndSaysSoWhenTheServerIsDown(t *testing.T) {
	e := newLoginEnv(t)
	if out, err := e.run(t, "login", "--workspace", e.srv.WorkspaceID, "--name", "laptop"); err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}
	e.srv.Close()

	out, err := e.run(t, "logout")

	if err == nil || !strings.Contains(err.Error(), "could not revoke") {
		t.Fatalf("logout err = %v, want a failed revoke reported", err)
	}
	if !strings.Contains(out+err.Error(), "removed") {
		t.Errorf("logout output %q / %v does not say the local credential was removed", out, err)
	}
	if _, err := (security.OSKeychain{}).Get(e.srv.URL); !errors.Is(err, security.ErrNotFound) {
		t.Errorf("keychain after logout: err = %v, want ErrNotFound", err)
	}
}

func TestLoginFailsClearlyWithoutAKeychain(t *testing.T) {
	e := newLoginEnv(t)
	keyring.MockInitWithError(errors.New("no secret service on the session bus"))
	t.Cleanup(keyring.MockInit)
	e.open = func(string) error {
		t.Error("browser opened without a keychain")
		return nil
	}

	_, err := e.run(t, "login", "--workspace", e.srv.WorkspaceID, "--name", "laptop")

	if !errors.Is(err, security.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}
