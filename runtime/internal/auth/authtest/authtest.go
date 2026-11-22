// Package authtest fakes Supabase Auth and the Konduite API for login tests.
package authtest

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Supabase fakes the GoTrue PKCE endpoints: /authorize, /otp, /verify and
// /token?grant_type=pkce.
type Supabase struct {
	*httptest.Server
	PublishableKey string
	AccessToken    string
	// Deny makes /authorize and /verify redirect back with this OAuth error.
	Deny string

	mu         sync.Mutex
	challenge  string
	redirectTo string
	code       string
	email      string
	urls       []string
	otpSent    chan struct{}
}

// NewSupabase starts a fake Supabase Auth server.
func NewSupabase(t *testing.T) *Supabase {
	t.Helper()
	s := &Supabase{PublishableKey: "sb_publishable_test", AccessToken: "user-access-" + random(), otpSent: make(chan struct{}, 1)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth/v1/authorize", s.authorize)
	mux.HandleFunc("POST /auth/v1/otp", s.otp)
	mux.HandleFunc("GET /auth/v1/verify", s.verify)
	mux.HandleFunc("POST /auth/v1/token", s.token)
	s.Server = httptest.NewServer(s.record(mux))
	t.Cleanup(s.Close)
	return s
}

// URLs returns every request URL the fake has seen.
func (s *Supabase) URLs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.urls...)
}

// Email returns the address a magic link was last sent to.
func (s *Supabase) Email() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.email
}

// MagicLink waits for an /otp call and returns the link it would have emailed.
func (s *Supabase) MagicLink(t *testing.T) string {
	t.Helper()
	select {
	case <-s.otpSent:
	case <-time.After(5 * time.Second):
		t.Fatal("no magic link was requested")
	}
	return s.URL + "/auth/v1/verify?type=magiclink&token=pkce_fake"
}

func (s *Supabase) record(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.urls = append(s.urls, r.URL.String())
		s.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

func (s *Supabase) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("provider") == "" || q.Get("code_challenge_method") != "s256" || q.Get("code_challenge") == "" {
		http.Error(w, "bad authorize request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.challenge, s.redirectTo = q.Get("code_challenge"), q.Get("redirect_to")
	s.mu.Unlock()
	s.redirectBack(w, r)
}

func (s *Supabase) otp(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("apikey") != s.PublishableKey {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"msg": "invalid api key"})
		return
	}
	var body struct {
		Email               string `json:"email"`
		CodeChallenge       string `json:"code_challenge"`
		CodeChallengeMethod string `json:"code_challenge_method"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Email == "" ||
		body.CodeChallenge == "" || body.CodeChallengeMethod != "s256" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"msg": "bad otp request"})
		return
	}
	s.mu.Lock()
	s.email, s.challenge, s.redirectTo = body.Email, body.CodeChallenge, r.URL.Query().Get("redirect_to")
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{})
	select {
	case s.otpSent <- struct{}{}:
	default:
	}
}

func (s *Supabase) verify(w http.ResponseWriter, r *http.Request) {
	s.redirectBack(w, r)
}

func (s *Supabase) redirectBack(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	target, err := url.Parse(s.redirectTo)
	if err != nil || target.Host == "" {
		s.mu.Unlock()
		http.Error(w, "bad redirect_to", http.StatusBadRequest)
		return
	}
	q := target.Query()
	if s.Deny != "" {
		q.Set("error", s.Deny)
		q.Set("error_description", "The user denied the request")
	} else {
		s.code = random()
		q.Set("code", s.code)
	}
	s.mu.Unlock()
	target.RawQuery = q.Encode()
	http.Redirect(w, r, target.String(), http.StatusFound)
}

func (s *Supabase) token(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("grant_type") != "pkce" || r.Header.Get("apikey") != s.PublishableKey {
		writeJSON(w, http.StatusBadRequest, map[string]any{"msg": "bad token request"})
		return
	}
	var body struct {
		AuthCode     string `json:"auth_code"`
		CodeVerifier string `json:"code_verifier"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"msg": "bad json"})
		return
	}
	sum := sha256.Sum256([]byte(body.CodeVerifier))
	s.mu.Lock()
	ok := s.code != "" && body.AuthCode == s.code && base64.RawURLEncoding.EncodeToString(sum[:]) == s.challenge
	s.code = ""
	s.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"code": 400, "error_code": "bad_code_verifier", "msg": "code challenge does not match previously saved code verifier",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": s.AccessToken, "token_type": "bearer", "expires_in": 3600,
		"refresh_token": "supabase-refresh-" + random(), "user": map[string]any{"id": "user-1"},
	})
}

// Server fakes the Konduite API's runtime registration, token exchange,
// self lookup, revocation and the runtime's own agent routes.
type Server struct {
	*httptest.Server
	UserToken   string
	WorkspaceID string
	RuntimeID   string
	Credential  string

	mu      sync.Mutex
	names   []string
	revoked []string
	tokens  []string
	urls    []string
	agents  []Agent
}

// Agent is an agent registered with the fake.
type Agent struct {
	ID          string `json:"id"`
	RuntimeID   string `json:"runtime_id"`
	Provider    string `json:"provider"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status"`
}

// NewServer starts a fake Konduite API that accepts userToken.
func NewServer(t *testing.T, userToken string) *Server {
	t.Helper()
	s := &Server{
		UserToken:   userToken,
		WorkspaceID: "0b6c3f9e-8f8e-4c55-9a43-6f1b2d3c4e5f",
		RuntimeID:   "7d0e3c1a-2b4f-4e6a-9c8d-1f2e3d4c5b6a",
		Credential:  "krt_" + random() + random(),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/runtimes/register", s.register)
	mux.HandleFunc("POST /v1/runtime/revoke", s.revoke)
	mux.HandleFunc("POST /v1/runtime/token", s.token)
	mux.HandleFunc("GET /v1/runtime/self", s.self)
	mux.HandleFunc("POST /v1/runtime/agents", s.registerAgent)
	mux.HandleFunc("GET /v1/runtime/agents", s.listAgents)
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.urls = append(s.urls, r.URL.String())
		s.mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

// Registered returns the names of the runtimes registered so far.
func (s *Server) Registered() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.names...)
}

// Revoked returns the credentials revoked so far.
func (s *Server) Revoked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.revoked...)
}

// AccessTokens returns the runtime access tokens issued so far.
func (s *Server) AccessTokens() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.tokens...)
}

// RevokeAll revokes the credential server-side, as the dashboard would.
func (s *Server) RevokeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revoked = append(s.revoked, s.Credential)
}

// URLs returns every request URL the fake has seen.
func (s *Server) URLs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.urls...)
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+s.UserToken {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "missing or invalid access token")
		return
	}
	var body struct {
		WorkspaceID string `json:"workspace_id"`
		Name        string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid", "request body must be a JSON object")
		return
	}
	if body.WorkspaceID != s.WorkspaceID {
		writeError(w, http.StatusNotFound, "not_found", "workspace not found")
		return
	}
	s.mu.Lock()
	s.names = append(s.names, body.Name)
	s.mu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]any{"data": map[string]any{
		"runtime_id": s.RuntimeID, "workspace_id": s.WorkspaceID, "owner_id": "user-1",
		"name": body.Name, "refresh_credential": s.Credential, "created_at": "2026-11-19T00:00:00Z",
	}})
}

func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RefreshCredential string `json:"refresh_credential"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.RefreshCredential == "" {
		writeError(w, http.StatusUnprocessableEntity, "invalid", "refresh_credential is required in the request body")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.revoked {
		if c == body.RefreshCredential {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "invalid refresh credential")
			return
		}
	}
	if body.RefreshCredential != s.Credential {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "invalid refresh credential")
		return
	}
	s.revoked = append(s.revoked, body.RefreshCredential)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RefreshCredential string `json:"refresh_credential"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.RefreshCredential == "" {
		writeError(w, http.StatusUnprocessableEntity, "invalid", "refresh_credential is required in the request body")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if body.RefreshCredential != s.Credential || slices.Contains(s.revoked, body.RefreshCredential) {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "invalid refresh credential")
		return
	}
	tok := "rat." + random()
	s.tokens = append(s.tokens, tok)
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"access_token": tok, "token_type": "Bearer", "expires_in": 900, "expires_at": "2026-11-19T00:15:00Z",
	}})
}

func (s *Server) self(w http.ResponseWriter, r *http.Request) {
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	defer s.mu.Unlock()
	if tok == "" || !slices.Contains(s.tokens, tok) {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "invalid runtime access token")
		return
	}
	name := ""
	if len(s.names) > 0 {
		name = s.names[len(s.names)-1]
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{ //nolint:gosec // credential_last_used_at is a timestamp field name, not a secret
		"id": s.RuntimeID, "workspace_id": s.WorkspaceID, "owner_id": "user-1", "name": name,
		"revoked": slices.Contains(s.revoked, s.Credential), "credential_last_used_at": "2026-11-19T00:00:00Z",
		"created_at": "2026-11-19T00:00:00Z", "updated_at": "2026-11-19T00:00:00Z",
	}})
}

// Agents returns the agents registered so far.
func (s *Server) Agents() []Agent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Agent(nil), s.agents...)
}

func (s *Server) runtimeAuthorized(w http.ResponseWriter, r *http.Request) bool {
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if tok == "" || !slices.Contains(s.tokens, tok) {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "invalid runtime access token")
		return false
	}
	return true
}

func (s *Server) registerAgent(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.runtimeAuthorized(w, r) {
		return
	}
	var a Agent
	if err := json.NewDecoder(r.Body).Decode(&a); err != nil || a.Provider == "" || a.Name == "" {
		writeError(w, http.StatusUnprocessableEntity, "invalid", "invalid agent registration")
		return
	}
	for _, have := range s.agents {
		if have.Provider == a.Provider && have.Name == a.Name {
			writeError(w, http.StatusConflict, "conflict", "the runtime already has an agent with this provider and name")
			return
		}
	}
	a.ID, a.RuntimeID, a.Status = "agent-"+random(), s.RuntimeID, "offline"
	s.agents = append(s.agents, a)
	writeJSON(w, http.StatusCreated, map[string]any{"data": a})
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.runtimeAuthorized(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": append([]Agent{}, s.agents...)})
}

// Browser returns an opener that visits the URL like a browser whose user
// approves the sign-in, following redirects back to the loopback listener.
func Browser(t *testing.T) func(string) error {
	t.Helper()
	return func(u string) error {
		resp, err := http.Get(u) //nolint:gosec,noctx // test browser following the URL under test
		if err != nil {
			return err
		}
		return resp.Body.Close()
	}
}

// Visit fetches u and returns the status code, like a user opening a link.
func Visit(t *testing.T, u string) int {
	t.Helper()
	resp, err := http.Get(u) //nolint:gosec,noctx // test browser following the URL under test
	if err != nil {
		t.Errorf("visit %s: %v", u, err)
		return 0
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// AssertNoSecretsInURLs fails if any recorded URL contains one of secrets.
func AssertNoSecretsInURLs(t *testing.T, urls []string, secrets ...string) {
	t.Helper()
	for _, u := range urls {
		for i, s := range secrets {
			if s != "" && strings.Contains(u, s) {
				t.Errorf("secret #%d appeared in a request URL", i)
			}
		}
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": message}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func random() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
