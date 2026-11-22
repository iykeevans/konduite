package cli

import (
	"strings"
	"testing"

	"github.com/iykeevans/konduite/runtime/internal/auth/authtest"
)

func TestAgentsRegisterAndList(t *testing.T) {
	e := newLoginEnv(t)
	if out, err := e.run(t, "login", "--workspace", e.srv.WorkspaceID, "--name", "laptop"); err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}

	out, err := e.run(t, "agents", "register", "--provider", "claude_code", "--name", "claude",
		"--display-name", "Reviewer", "--description", "reviews PRs")
	if err != nil {
		t.Fatalf("register: %v\n%s", err, out)
	}
	agents := e.srv.Agents()
	if len(agents) != 1 {
		t.Fatalf("server has %d agents, want 1", len(agents))
	}
	a := agents[0]
	if a.Provider != "claude_code" || a.Name != "claude" || a.DisplayName != "Reviewer" || a.Description != "reviews PRs" {
		t.Errorf("registered %+v", a)
	}
	for _, want := range []string{a.ID, "claude_code/claude", "offline"} {
		if !strings.Contains(out, want) {
			t.Errorf("register printed %q, want it to mention %q", out, want)
		}
	}

	out, err = e.run(t, "agents", "list")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	for _, want := range []string{a.ID, "claude_code/claude", "Reviewer", "offline"} {
		if !strings.Contains(out, want) {
			t.Errorf("list printed %q, want it to mention %q", out, want)
		}
	}

	if out, err := e.run(t, "agents", "register", "--provider", "claude_code", "--name", "claude"); err == nil ||
		!strings.Contains(err.Error(), "already has an agent") {
		t.Errorf("registering twice: err = %v (%s), want the conflict reported", err, out)
	}

	authtest.AssertNoSecretsInURLs(t, e.srv.URLs(), append([]string{e.srv.Credential}, e.srv.AccessTokens()...)...)
	for _, secret := range append([]string{e.srv.Credential, "krt_"}, e.srv.AccessTokens()...) {
		if strings.Contains(out, secret) {
			t.Error("agents printed a secret")
		}
	}
}

func TestAgentsListWithNoAgents(t *testing.T) {
	e := newLoginEnv(t)
	if out, err := e.run(t, "login", "--workspace", e.srv.WorkspaceID, "--name", "laptop"); err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}
	out, err := e.run(t, "agents", "list")
	if err != nil || !strings.Contains(out, "no agents") {
		t.Errorf("list = %q, %v; want it to say there are no agents", out, err)
	}
}

func TestAgentsNeedALogin(t *testing.T) {
	e := newLoginEnv(t)
	for _, args := range [][]string{
		{"agents", "register", "--provider", "echo", "--name", "echo"},
		{"agents", "list"},
	} {
		_, err := e.run(t, args...)
		if err == nil || !strings.Contains(err.Error(), "agenthub login") {
			t.Errorf("%v: err = %v, want a hint to log in", args, err)
		}
	}
	if len(e.srv.URLs()) != 0 {
		t.Errorf("called the server without a credential: %v", e.srv.URLs())
	}
}

func TestAgentsRegisterNeedsProviderAndName(t *testing.T) {
	e := newLoginEnv(t)
	if out, err := e.run(t, "login", "--workspace", e.srv.WorkspaceID, "--name", "laptop"); err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}
	for _, args := range [][]string{
		{"agents", "register", "--name", "claude"},
		{"agents", "register", "--provider", "claude_code"},
	} {
		if _, err := e.run(t, args...); err == nil {
			t.Errorf("%v: err = nil, want a missing flag reported", args)
		}
	}
	if n := len(e.srv.Agents()); n != 0 {
		t.Errorf("registered %d agents, want 0", n)
	}
}

func TestAgentsReportARejectedCredential(t *testing.T) {
	e := newLoginEnv(t)
	if out, err := e.run(t, "login", "--workspace", e.srv.WorkspaceID, "--name", "laptop"); err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}
	e.srv.RevokeAll()
	_, err := e.run(t, "agents", "list")
	if err == nil || !strings.Contains(err.Error(), "agenthub login") {
		t.Errorf("err = %v, want the rejected credential reported with what to do", err)
	}
}
