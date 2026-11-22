package auth_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iykeevans/konduite/runtime/internal/auth"
	"github.com/iykeevans/konduite/runtime/internal/auth/authtest"
	"github.com/iykeevans/konduite/runtime/internal/config"
	"github.com/iykeevans/konduite/runtime/internal/security"
)

type memKeychain struct {
	mu      sync.Mutex
	items   map[string]string
	sets    int
	failGet error
	failSet error
}

func newKeychain() *memKeychain { return &memKeychain{items: map[string]string{}} }

func (k *memKeychain) Get(account string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.failGet != nil {
		return "", k.failGet
	}
	v, ok := k.items[account]
	if !ok {
		return "", security.ErrNotFound
	}
	return v, nil
}

func (k *memKeychain) Set(account, secret string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.failSet != nil {
		return k.failSet
	}
	k.sets++
	k.items[account] = secret
	return nil
}

func (k *memKeychain) Delete(account string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.items, account)
	return nil
}

type env struct {
	sb     *authtest.Supabase
	srv    *authtest.Server
	kc     *memKeychain
	out    *bytes.Buffer
	client auth.Client
	cfg    config.Config
}

func newEnv(t *testing.T) *env {
	t.Helper()
	sb := authtest.NewSupabase(t)
	srv := authtest.NewServer(t, sb.AccessToken)
	e := &env{sb: sb, srv: srv, kc: newKeychain(), out: &bytes.Buffer{}}
	e.cfg = config.Default()
	e.cfg.ServerURL = srv.URL
	e.cfg.SupabaseURL = sb.URL
	e.cfg.SupabasePublishableKey = sb.PublishableKey
	e.client = auth.Client{Keychain: e.kc, HTTP: http.DefaultClient, Open: authtest.Browser(t), Out: e.out}
	return e
}

func (e *env) input() auth.LoginInput {
	return auth.LoginInput{Config: e.cfg, WorkspaceID: e.srv.WorkspaceID, Name: "laptop", Provider: "github", Timeout: 10 * time.Second}
}

func (e *env) assertNoSecretsInURLs(t *testing.T) {
	t.Helper()
	urls := append(e.sb.URLs(), e.srv.URLs()...)
	authtest.AssertNoSecretsInURLs(t, urls, e.srv.Credential, e.sb.AccessToken)
}

func TestOAuthLoginStoresOnlyTheRuntimeCredentialInTheKeychain(t *testing.T) {
	e := newEnv(t)

	rt, err := e.client.Login(t.Context(), e.input())
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	if rt.ID != e.srv.RuntimeID || rt.WorkspaceID != e.srv.WorkspaceID || rt.Name != "laptop" {
		t.Errorf("runtime = %+v, want the registered runtime", rt)
	}
	if got := e.kc.items[e.srv.URL]; got != e.srv.Credential {
		t.Errorf("keychain holds %q for the server, want the refresh credential", got)
	}
	if e.kc.sets != 1 || len(e.kc.items) != 1 {
		t.Errorf("keychain written %d times with %d items, want only the runtime credential", e.kc.sets, len(e.kc.items))
	}
	if strings.Contains(e.out.String(), e.srv.Credential) || strings.Contains(e.out.String(), e.sb.AccessToken) {
		t.Error("login output contains a secret")
	}
	e.assertNoSecretsInURLs(t)
}

func TestOAuthRedirectsToARandomLoopbackPortWithState(t *testing.T) {
	e := newEnv(t)
	var opened []string
	e.client.Open = func(u string) error {
		opened = append(opened, u)
		return authtest.Browser(t)(u)
	}
	for range 2 {
		e.kc.items = map[string]string{}
		if _, err := e.client.Login(t.Context(), e.input()); err != nil {
			t.Fatal(err)
		}
	}

	var redirects []*url.URL
	for _, o := range opened {
		u, err := url.Parse(o)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(o, e.sb.URL+"/auth/v1/authorize?") || u.Query().Get("provider") != "github" {
			t.Errorf("opened %s, want the Supabase authorize URL for github", o)
		}
		r, err := url.Parse(u.Query().Get("redirect_to"))
		if err != nil {
			t.Fatal(err)
		}
		if r.Hostname() != "127.0.0.1" || r.Port() == "" || len(r.Query().Get("state")) < 20 {
			t.Errorf("redirect_to = %s, want 127.0.0.1 with a port and a long state", r)
		}
		redirects = append(redirects, r)
	}
	if len(redirects) == 2 && redirects[0].Query().Get("state") == redirects[1].Query().Get("state") {
		t.Error("two logins reused the same state")
	}
}

func TestMagicLinkLogin(t *testing.T) {
	e := newEnv(t)
	e.client.Open = func(string) error {
		t.Error("magic-link login opened a browser")
		return nil
	}
	in := e.input()
	in.Provider, in.Email = "", "ada@example.com"

	done := make(chan error, 1)
	go func() {
		_, err := e.client.Login(t.Context(), in)
		done <- err
	}()
	if status := authtest.Visit(t, e.sb.MagicLink(t)); status != http.StatusOK {
		t.Errorf("magic link landed with status %d, want 200", status)
	}

	if err := <-done; err != nil {
		t.Fatalf("login: %v", err)
	}
	if e.sb.Email() != "ada@example.com" || e.kc.items[e.srv.URL] != e.srv.Credential {
		t.Errorf("email %q, keychain %v; want the link sent to ada and the credential stored", e.sb.Email(), e.kc.items)
	}
	e.assertNoSecretsInURLs(t)
}

func TestCallbackWithTheWrongStateIsIgnored(t *testing.T) {
	e := newEnv(t)
	e.client.Open = func(u string) error {
		parsed, _ := url.Parse(u)
		redirect, _ := url.Parse(parsed.Query().Get("redirect_to"))
		forged := *redirect
		forged.RawQuery = url.Values{"state": {"forged"}, "code": {"attacker-code"}}.Encode()
		if status := authtest.Visit(t, forged.String()); status != http.StatusBadRequest {
			t.Errorf("forged callback status = %d, want 400", status)
		}
		return authtest.Browser(t)(u)
	}

	if _, err := e.client.Login(t.Context(), e.input()); err != nil {
		t.Fatalf("login after a forged callback: %v", err)
	}
}

func TestLoginFailsWhenTheUserDenies(t *testing.T) {
	e := newEnv(t)
	e.sb.Deny = "access_denied"

	_, err := e.client.Login(t.Context(), e.input())

	if err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("err = %v, want the Supabase error", err)
	}
	if len(e.kc.items) != 0 || len(e.srv.Registered()) != 0 {
		t.Error("a denied login registered a runtime or wrote the keychain")
	}
}

func TestLoginTimesOutWithoutACallback(t *testing.T) {
	e := newEnv(t)
	e.client.Open = func(string) error { return nil }
	in := e.input()
	in.Timeout = 50 * time.Millisecond

	_, err := e.client.Login(t.Context(), in)

	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want a timeout", err)
	}
}

func TestLoginStillWorksWhenTheBrowserCannotOpen(t *testing.T) {
	e := newEnv(t)
	var printed string
	e.client.Open = func(u string) error {
		printed = u
		go func() { _ = authtest.Browser(t)(u) }()
		return errors.New("no display")
	}

	if _, err := e.client.Login(t.Context(), e.input()); err != nil {
		t.Fatalf("login: %v", err)
	}
	if !strings.Contains(e.out.String(), printed) {
		t.Errorf("output %q does not show the sign-in URL to open by hand", e.out.String())
	}
}

func TestLoginRefusesWhenAlreadyLoggedIn(t *testing.T) {
	e := newEnv(t)
	e.kc.items[e.srv.URL] = "krt_existing"
	e.client.Open = func(string) error {
		t.Error("browser opened while already logged in")
		return nil
	}

	_, err := e.client.Login(t.Context(), e.input())

	if !errors.Is(err, auth.ErrAlreadyLoggedIn) {
		t.Fatalf("err = %v, want ErrAlreadyLoggedIn", err)
	}
	if e.kc.items[e.srv.URL] != "krt_existing" {
		t.Error("existing credential was replaced")
	}
}

func TestLoginFailsBeforeTheBrowserWhenTheKeychainIsUnavailable(t *testing.T) {
	e := newEnv(t)
	e.kc.failGet = fmt.Errorf("%w: no secret service", security.ErrUnavailable)
	e.client.Open = func(string) error {
		t.Error("browser opened without a keychain")
		return nil
	}

	_, err := e.client.Login(t.Context(), e.input())

	if !errors.Is(err, security.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestLoginRevokesTheCredentialWhenTheKeychainWriteFails(t *testing.T) {
	e := newEnv(t)
	e.kc.failSet = fmt.Errorf("%w: locked", security.ErrUnavailable)

	_, err := e.client.Login(t.Context(), e.input())

	if !errors.Is(err, security.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if got := e.srv.Revoked(); len(got) != 1 || got[0] != e.srv.Credential {
		t.Errorf("revoked %d credentials, want the orphaned one revoked", len(got))
	}
	if strings.Contains(err.Error(), e.srv.Credential) {
		t.Error("error message contains the credential")
	}
}

func TestLoginReportsServerErrors(t *testing.T) {
	e := newEnv(t)
	in := e.input()
	in.WorkspaceID = "11111111-1111-4111-8111-111111111111"

	_, err := e.client.Login(t.Context(), in)

	if err == nil || !strings.Contains(err.Error(), "workspace not found") {
		t.Fatalf("err = %v, want the server's message", err)
	}
	if strings.Contains(err.Error(), e.sb.AccessToken) {
		t.Error("error message contains the user access token")
	}
	if len(e.kc.items) != 0 {
		t.Error("keychain written after a failed registration")
	}
}

func TestLoginValidatesItsInput(t *testing.T) {
	e := newEnv(t)
	tests := map[string]func(*auth.LoginInput){
		"no supabase url":     func(in *auth.LoginInput) { in.Config.SupabaseURL = "" },
		"no publishable key":  func(in *auth.LoginInput) { in.Config.SupabasePublishableKey = "" },
		"no workspace":        func(in *auth.LoginInput) { in.WorkspaceID = "" },
		"no name":             func(in *auth.LoginInput) { in.Name = " " },
		"no sign-in method":   func(in *auth.LoginInput) { in.Provider, in.Email = "", "" },
		"two sign-in methods": func(in *auth.LoginInput) { in.Email = "ada@example.com" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			in := e.input()
			mutate(&in)
			if _, err := e.client.Login(t.Context(), in); !errors.Is(err, auth.ErrInvalidInput) {
				t.Errorf("err = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestLogoutRevokesThenClearsTheKeychain(t *testing.T) {
	e := newEnv(t)
	e.kc.items[e.srv.URL] = e.srv.Credential

	res, err := e.client.Logout(t.Context(), e.cfg)

	if err != nil {
		t.Fatalf("logout: %v", err)
	}
	if !res.WasLoggedIn || !res.Revoked || res.RevokeErr != nil {
		t.Errorf("result = %+v, want revoked", res)
	}
	if got := e.srv.Revoked(); len(got) != 1 || got[0] != e.srv.Credential {
		t.Error("server did not revoke the credential")
	}
	if len(e.kc.items) != 0 {
		t.Error("keychain not cleared")
	}
	e.assertNoSecretsInURLs(t)
}

func TestLogoutClearsLocallyWhenTheServerIsUnreachable(t *testing.T) {
	e := newEnv(t)
	e.kc.items[e.srv.URL] = e.srv.Credential
	e.cfg.ServerURL = e.srv.URL
	e.srv.Close()

	res, err := e.client.Logout(context.Background(), e.cfg)

	if err != nil {
		t.Fatalf("logout: %v", err)
	}
	if res.Revoked || res.RevokeErr == nil {
		t.Errorf("result = %+v, want a revoke error reported", res)
	}
	if len(e.kc.items) != 0 {
		t.Error("keychain not cleared after a failed revoke")
	}
	if strings.Contains(res.RevokeErr.Error(), e.srv.Credential) {
		t.Error("revoke error contains the credential")
	}
}

func TestLogoutWhenTheServerNoLongerKnowsTheCredential(t *testing.T) {
	e := newEnv(t)
	e.kc.items[e.srv.URL] = "krt_unknown"

	res, err := e.client.Logout(t.Context(), e.cfg)

	if err != nil {
		t.Fatal(err)
	}
	if !res.AlreadyRevoked || res.Revoked || len(e.kc.items) != 0 {
		t.Errorf("result = %+v, keychain %v; want already revoked and cleared", res, e.kc.items)
	}
}

func TestLogoutWhenNotLoggedIn(t *testing.T) {
	e := newEnv(t)

	res, err := e.client.Logout(t.Context(), e.cfg)

	if err != nil || res.WasLoggedIn {
		t.Errorf("result = %+v, err = %v, want not logged in and no error", res, err)
	}
	if len(e.srv.URLs()) != 0 {
		t.Error("logout called the server with nothing to revoke")
	}
}

func TestLoggedIn(t *testing.T) {
	e := newEnv(t)
	if ok, err := e.client.LoggedIn(e.cfg); err != nil || ok {
		t.Errorf("LoggedIn = %v, %v before login", ok, err)
	}
	e.kc.items[e.srv.URL] = e.srv.Credential
	if ok, err := e.client.LoggedIn(e.cfg); err != nil || !ok {
		t.Errorf("LoggedIn = %v, %v after login", ok, err)
	}
}

func TestAccessTokenExchangesTheStoredCredential(t *testing.T) {
	e := newEnv(t)
	e.kc.items[e.srv.URL] = e.srv.Credential

	tok, err := e.client.AccessToken(t.Context(), e.cfg)

	if err != nil {
		t.Fatal(err)
	}
	if issued := e.srv.AccessTokens(); len(issued) != 1 || tok != issued[0] {
		t.Errorf("token %q is not the one the server issued", tok)
	}
	e.assertNoSecretsInURLs(t)
}

func TestAccessTokenWhenNotLoggedInOrRejected(t *testing.T) {
	e := newEnv(t)
	if _, err := e.client.AccessToken(t.Context(), e.cfg); !errors.Is(err, auth.ErrNotLoggedIn) {
		t.Errorf("without a credential: err = %v, want ErrNotLoggedIn", err)
	}

	e.kc.items[e.srv.URL] = e.srv.Credential
	e.srv.RevokeAll()
	if _, err := e.client.AccessToken(t.Context(), e.cfg); !errors.Is(err, auth.ErrRejected) {
		t.Errorf("revoked credential: err = %v, want ErrRejected", err)
	}
}
