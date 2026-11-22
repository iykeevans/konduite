package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/iykeevans/konduite/runtime/internal/config"
	"github.com/iykeevans/konduite/runtime/internal/security"
)

// Errors returned by the agent calls; match them with errors.Is.
var (
	ErrNotLoggedIn        = errors.New("not logged in: run agenthub login first")
	ErrCredentialRejected = errors.New("the server rejected this runtime's credential (revoked or expired): run agenthub logout, then agenthub login")
)

// AgentInput is an agent to register on this machine's runtime. Provider and
// Name must match what the runtime will announce for it.
type AgentInput struct {
	Provider    string `json:"provider"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	Description string `json:"description,omitempty"`
}

// RemoteAgent is the server's view of one of this runtime's agents.
type RemoteAgent struct {
	ID          string  `json:"id"`
	Provider    string  `json:"provider"`
	Name        string  `json:"name"`
	DisplayName *string `json:"display_name"`
	Status      string  `json:"status"`
}

// RegisterAgent registers an agent on this machine's runtime, so it exists
// on the server before the runtime first announces it.
func (c Client) RegisterAgent(ctx context.Context, cfg config.Config, in AgentInput) (RemoteAgent, error) {
	var out struct {
		Data RemoteAgent `json:"data"`
	}
	err := c.asRuntime(ctx, cfg, func(ctx context.Context, srv server, h http.Header) error {
		return do(ctx, srv.http, http.MethodPost, srv.url+"/v1/runtime/agents", h, in, &out)
	})
	return out.Data, err
}

// Agents lists this machine's runtime's agents.
func (c Client) Agents(ctx context.Context, cfg config.Config) ([]RemoteAgent, error) {
	var out struct {
		Data []RemoteAgent `json:"data"`
	}
	err := c.asRuntime(ctx, cfg, func(ctx context.Context, srv server, h http.Header) error {
		return do(ctx, srv.http, http.MethodGet, srv.url+"/v1/runtime/agents", h, nil, &out)
	})
	return out.Data, err
}

// asRuntime exchanges the stored credential for an access token and calls fn
// with it in an Authorization header.
func (c Client) asRuntime(ctx context.Context, cfg config.Config, fn func(context.Context, server, http.Header) error) error {
	account := accountFor(cfg)
	cred, err := c.Keychain.Get(account)
	if errors.Is(err, security.ErrNotFound) {
		return ErrNotLoggedIn
	}
	if err != nil {
		return err
	}
	sctx, cancel := context.WithTimeout(ctx, serverTimeout)
	defer cancel()
	srv := server{url: account, http: c.HTTP}
	tok, err := srv.accessToken(sctx, cred)
	if err == nil {
		err = fn(sctx, srv, http.Header{"Authorization": {"Bearer " + tok}})
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusUnauthorized {
		return ErrCredentialRejected
	}
	if err != nil {
		return fmt.Errorf("%s: %w", account, err)
	}
	return nil
}
