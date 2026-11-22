// Package auth signs a developer in with Supabase, registers this machine as
// a runtime and keeps the runtime's refresh credential in the OS keychain.
//
// The Supabase session is used once, to register, and then dropped: only the
// runtime credential is stored, so the keychain item can act as this runtime
// and nothing more.
package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/iykeevans/konduite/runtime/internal/config"
	"github.com/iykeevans/konduite/runtime/internal/security"
)

// Errors returned by Client; match them with errors.Is.
var (
	ErrAlreadyLoggedIn = errors.New("already logged in: run agenthub logout first")
	ErrInvalidInput    = errors.New("invalid login input")
	ErrNotLoggedIn     = errors.New("not logged in: run agenthub login")
	ErrRejected        = errors.New("the server rejected this runtime's credential (revoked or expired): run agenthub login")
)

const (
	defaultTimeout = 5 * time.Minute
	serverTimeout  = 15 * time.Second
)

// Client runs login and logout. Open shows a URL in the user's browser and
// Out receives instructions for the user; neither ever sees a secret.
type Client struct {
	Keychain security.Keychain
	HTTP     *http.Client
	Open     func(url string) error
	Out      io.Writer
}

// LoginInput says where to sign in and what to register. Exactly one of
// Provider (a Supabase OAuth provider such as github) or Email (magic link)
// must be set.
type LoginInput struct {
	Config      config.Config
	WorkspaceID string
	Name        string
	Provider    string
	Email       string
	// Timeout bounds the wait for the browser callback; zero means 5 minutes.
	Timeout time.Duration
}

// Runtime is the runtime registered by Login.
type Runtime struct {
	ID          string
	WorkspaceID string
	Name        string
}

// LogoutResult says what Logout managed to do. The local credential is
// always cleared when Logout returns no error, even if revocation failed.
type LogoutResult struct {
	WasLoggedIn bool
	Revoked     bool
	// AlreadyRevoked means the server no longer accepted the credential.
	AlreadyRevoked bool
	// RevokeErr is why the server could not be told; the credential may
	// still be valid there.
	RevokeErr error
}

// Login signs the user in through the browser, registers a runtime and
// stores its refresh credential in the keychain.
func (c Client) Login(ctx context.Context, in LoginInput) (Runtime, error) {
	if err := validate(in); err != nil {
		return Runtime{}, err
	}
	account := accountFor(in.Config)
	if _, err := c.Keychain.Get(account); err == nil {
		return Runtime{}, ErrAlreadyLoggedIn
	} else if !errors.Is(err, security.ErrNotFound) {
		return Runtime{}, err
	}

	sb := supabase{url: strings.TrimRight(in.Config.SupabaseURL, "/"), key: in.Config.SupabasePublishableKey, http: c.HTTP}
	userToken, err := c.signIn(ctx, sb, in)
	if err != nil {
		return Runtime{}, err
	}

	api := server{url: account, http: c.HTTP}
	reg, err := api.register(ctx, userToken, in.WorkspaceID, strings.TrimSpace(in.Name))
	if err != nil {
		return Runtime{}, fmt.Errorf("register runtime: %w", err)
	}
	if err := c.Keychain.Set(account, reg.RefreshCredential); err != nil {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), serverTimeout)
		defer cancel()
		if rerr := api.revoke(rctx, reg.RefreshCredential); rerr != nil {
			return Runtime{}, fmt.Errorf("store the runtime credential: %w; revoking it also failed (%w), so delete runtime %s from the dashboard", err, rerr, reg.RuntimeID)
		}
		return Runtime{}, fmt.Errorf("store the runtime credential: %w; the new credential was revoked", err)
	}
	return Runtime{ID: reg.RuntimeID, WorkspaceID: reg.WorkspaceID, Name: reg.Name}, nil
}

// Logout revokes the runtime credential on the server and then deletes it
// from the keychain, even when the server cannot be reached.
func (c Client) Logout(ctx context.Context, cfg config.Config) (LogoutResult, error) {
	account := accountFor(cfg)
	cred, err := c.Keychain.Get(account)
	if errors.Is(err, security.ErrNotFound) {
		return LogoutResult{}, nil
	}
	if err != nil {
		return LogoutResult{}, err
	}

	res := LogoutResult{WasLoggedIn: true}
	rctx, cancel := context.WithTimeout(ctx, serverTimeout)
	defer cancel()
	err = server{url: account, http: c.HTTP}.revoke(rctx, cred)
	var apiErr *APIError
	switch {
	case err == nil:
		res.Revoked = true
	case errors.As(err, &apiErr) && apiErr.Status == http.StatusUnauthorized:
		res.AlreadyRevoked = true
	default:
		res.RevokeErr = err
	}

	if err := c.Keychain.Delete(account); err != nil {
		return res, err
	}
	return res, nil
}

// RemoteRuntime is the server's view of this machine's runtime.
type RemoteRuntime struct {
	ID                   string     `json:"id"`
	WorkspaceID          string     `json:"workspace_id"`
	Name                 string     `json:"name"`
	Revoked              bool       `json:"revoked"`
	CredentialLastUsedAt *time.Time `json:"credential_last_used_at"`
	CreatedAt            time.Time  `json:"created_at"`
}

// Status is what Status found. Remote is set when the server accepted the
// stored credential; otherwise Rejected (the server refused it) or RemoteErr
// (it could not be asked) says why not.
type Status struct {
	LoggedIn  bool
	Remote    *RemoteRuntime
	Rejected  bool
	RemoteErr error
}

// Status reports whether a credential is stored for cfg's server and, if so,
// asks the server about the runtime by exchanging the credential for a
// short-lived access token. Only a keychain failure is an error.
func (c Client) Status(ctx context.Context, cfg config.Config) (Status, error) {
	account := accountFor(cfg)
	cred, err := c.Keychain.Get(account)
	if errors.Is(err, security.ErrNotFound) {
		return Status{}, nil
	}
	if err != nil {
		return Status{}, err
	}

	st := Status{LoggedIn: true}
	sctx, cancel := context.WithTimeout(ctx, serverTimeout)
	defer cancel()
	srv := server{url: account, http: c.HTTP}
	tok, err := srv.accessToken(sctx, cred)
	if err == nil {
		var rt RemoteRuntime
		if rt, err = srv.self(sctx, tok); err == nil {
			st.Remote = &rt
			return st, nil
		}
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusUnauthorized {
		st.Rejected = true
	} else {
		st.RemoteErr = err
	}
	return st, nil
}

// AccessToken exchanges the stored refresh credential for a short-lived
// runtime access token. It returns ErrNotLoggedIn when no credential is
// stored and ErrRejected when the server refuses it.
func (c Client) AccessToken(ctx context.Context, cfg config.Config) (string, error) {
	account := accountFor(cfg)
	cred, err := c.Keychain.Get(account)
	if errors.Is(err, security.ErrNotFound) {
		return "", ErrNotLoggedIn
	}
	if err != nil {
		return "", err
	}
	sctx, cancel := context.WithTimeout(ctx, serverTimeout)
	defer cancel()
	tok, err := server{url: account, http: c.HTTP}.accessToken(sctx, cred)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusUnauthorized {
		return "", ErrRejected
	}
	return tok, err
}

// LoggedIn reports whether the keychain holds a credential for cfg's server.
func (c Client) LoggedIn(cfg config.Config) (bool, error) {
	_, err := c.Keychain.Get(accountFor(cfg))
	if errors.Is(err, security.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func accountFor(cfg config.Config) string {
	return strings.TrimRight(cfg.ServerURL, "/")
}

func validate(in LoginInput) error {
	var problems []string
	if in.Config.SupabaseURL == "" {
		problems = append(problems, "supabase_url is not set (agenthub config set supabase_url <url>)")
	}
	if in.Config.SupabasePublishableKey == "" {
		problems = append(problems, "supabase_publishable_key is not set (agenthub config set supabase_publishable_key <key>)")
	}
	if in.WorkspaceID == "" {
		problems = append(problems, "no workspace: pass --workspace or set workspace_id")
	}
	if strings.TrimSpace(in.Name) == "" {
		problems = append(problems, "no runtime name: pass --name")
	}
	switch {
	case in.Provider == "" && in.Email == "":
		problems = append(problems, "choose how to sign in: --provider <name> (or set auth_provider) or --email <address>")
	case in.Provider != "" && in.Email != "":
		problems = append(problems, "use either --provider or --email, not both")
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalidInput, strings.Join(problems, "; "))
	}
	return nil
}
