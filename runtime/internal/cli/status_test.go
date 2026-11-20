package cli

import (
	"strings"
	"testing"

	"github.com/iykeevans/konduite/runtime/internal/auth/authtest"
)

func TestStatusBeforeLogin(t *testing.T) {
	e := newLoginEnv(t)

	out, err := e.run(t, "status")

	if err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, want := range []string{e.srv.URL, "not logged in"} {
		if !strings.Contains(out, want) {
			t.Errorf("status = %q, want it to mention %q", out, want)
		}
	}
	if len(e.srv.URLs()) != 0 {
		t.Errorf("status called the server without a credential: %v", e.srv.URLs())
	}
}

func TestStatusShowsTheLocalRuntimeAndTheServersView(t *testing.T) {
	e := newLoginEnv(t)
	if out, err := e.run(t, "login", "--workspace", e.srv.WorkspaceID, "--name", "ada-laptop"); err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}

	out, err := e.run(t, "status")

	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	for _, want := range []string{e.srv.URL, e.srv.WorkspaceID, e.srv.RuntimeID, "OS keychain", "ada-laptop", "active"} {
		if !strings.Contains(out, want) {
			t.Errorf("status = %q, want it to mention %q", out, want)
		}
	}
	for _, secret := range append([]string{e.srv.Credential, "krt_"}, e.srv.AccessTokens()...) {
		if strings.Contains(out, secret) {
			t.Errorf("status printed a secret")
		}
	}
	authtest.AssertNoSecretsInURLs(t, e.srv.URLs(), append([]string{e.srv.Credential}, e.srv.AccessTokens()...)...)
	if n := len(e.srv.AccessTokens()); n != 1 {
		t.Errorf("status exchanged the credential %d times, want 1", n)
	}
}

func TestStatusReportsARejectedCredential(t *testing.T) {
	e := newLoginEnv(t)
	if out, err := e.run(t, "login", "--workspace", e.srv.WorkspaceID, "--name", "laptop"); err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}
	e.srv.RevokeAll()

	out, err := e.run(t, "status")

	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	if !strings.Contains(out, "rejected") || !strings.Contains(out, "agenthub login") {
		t.Errorf("status = %q, want the rejected credential reported with what to do", out)
	}
}

func TestStatusStillShowsLocalStateWhenTheServerIsDown(t *testing.T) {
	e := newLoginEnv(t)
	if out, err := e.run(t, "login", "--workspace", e.srv.WorkspaceID, "--name", "laptop"); err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}
	e.srv.Close()

	out, err := e.run(t, "status")

	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	if !strings.Contains(out, e.srv.RuntimeID) || !strings.Contains(out, "unreachable") {
		t.Errorf("status = %q, want the local runtime id and the server marked unreachable", out)
	}
}
