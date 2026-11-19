package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

const maxResponseBytes = 1 << 20

// APIError is a non-2xx response. Message comes from the response body and
// never includes what was sent.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	if e.Code != "" {
		return fmt.Sprintf("%s (%d %s)", msg, e.Status, e.Code)
	}
	return fmt.Sprintf("%s (%d)", msg, e.Status)
}

type supabase struct {
	url  string
	key  string
	http *http.Client
}

func (s supabase) authorizeURL(provider, redirectTo, challenge string) string {
	return s.url + "/auth/v1/authorize?" + url.Values{
		"provider":              {provider},
		"redirect_to":           {redirectTo},
		"code_challenge":        {challenge},
		"code_challenge_method": {"s256"},
	}.Encode()
}

func (s supabase) sendMagicLink(ctx context.Context, email, redirectTo, challenge string) error {
	target := s.url + "/auth/v1/otp?" + url.Values{"redirect_to": {redirectTo}}.Encode()
	body := map[string]any{
		"email":                 email,
		"create_user":           false,
		"code_challenge":        challenge,
		"code_challenge_method": "s256",
	}
	return send(ctx, s.http, target, s.headers(), body, nil)
}

func (s supabase) exchange(ctx context.Context, code, verifier string) (string, error) {
	var out struct {
		AccessToken string `json:"access_token"`
	}
	err := send(ctx, s.http, s.url+"/auth/v1/token?grant_type=pkce", s.headers(),
		map[string]string{"auth_code": code, "code_verifier": verifier}, &out)
	if err != nil {
		return "", fmt.Errorf("finish sign-in: %w", err)
	}
	if out.AccessToken == "" {
		return "", errors.New("finish sign-in: Supabase returned no access token")
	}
	return out.AccessToken, nil
}

func (s supabase) headers() http.Header {
	return http.Header{"apikey": {s.key}}
}

type server struct {
	url  string
	http *http.Client
}

type registration struct {
	RuntimeID         string `json:"runtime_id"`
	WorkspaceID       string `json:"workspace_id"`
	Name              string `json:"name"`
	RefreshCredential string `json:"refresh_credential"`
}

func (s server) register(ctx context.Context, userToken, workspaceID, name string) (registration, error) {
	var out struct {
		Data registration `json:"data"`
	}
	err := send(ctx, s.http, s.url+"/api/v1/runtimes/register",
		http.Header{"Authorization": {"Bearer " + userToken}},
		map[string]string{"workspace_id": workspaceID, "name": name}, &out)
	if err != nil {
		return registration{}, err
	}
	if out.Data.RefreshCredential == "" || out.Data.RuntimeID == "" {
		return registration{}, errors.New("server returned no runtime credential")
	}
	return out.Data, nil
}

func (s server) revoke(ctx context.Context, refreshCredential string) error {
	return send(ctx, s.http, s.url+"/v1/runtime/revoke", nil,
		map[string]string{"refresh_credential": refreshCredential}, nil)
}

// send POSTs body as JSON and decodes a 2xx response into out. Transport
// errors are reduced to their cause so a request body can never leak.
func send(ctx context.Context, hc *http.Client, target string, h http.Header, body, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(data))
	if err != nil {
		return err
	}
	for k, v := range h {
		req.Header[k] = v
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := hc.Do(req)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			return fmt.Errorf("%s %s: %w", uerr.Op, uerr.URL, uerr.Err)
		}
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return apiError(resp.StatusCode, raw)
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode response from %s: %w", req.URL.Redacted(), err)
	}
	return nil
}

// apiError reads both the Konduite envelope ({"error":{code,message}}) and
// GoTrue's shapes ({error_code,msg} or {error,error_description}).
func apiError(status int, raw []byte) error {
	var body struct {
		Error            json.RawMessage `json:"error"`
		ErrorCode        string          `json:"error_code"`
		Msg              string          `json:"msg"`
		Message          string          `json:"message"`
		ErrorDescription string          `json:"error_description"`
	}
	e := &APIError{Status: status}
	if json.Unmarshal(raw, &body) != nil {
		return e
	}
	var envelope struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	var plain string
	switch {
	case json.Unmarshal(body.Error, &envelope) == nil && envelope.Message != "":
		e.Code, e.Message = envelope.Code, envelope.Message
	case json.Unmarshal(body.Error, &plain) == nil && plain != "":
		e.Code, e.Message = plain, body.ErrorDescription
	default:
		e.Code = body.ErrorCode
		e.Message = firstNonEmpty(body.Msg, body.Message, body.ErrorDescription)
	}
	e.Message = printable(e.Message)
	e.Code = printable(e.Code)
	return e
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
