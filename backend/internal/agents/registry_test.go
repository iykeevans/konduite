package agents_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/agents"
	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/authz"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
	"github.com/iykeevans/konduite/packages/contracts"
)

type fakeRegistry struct {
	roles     map[[2]uuid.UUID]authz.Role
	runtimes  map[uuid.UUID]agents.RuntimeOwner
	agents    map[uuid.UUID]agents.Agent
	list      []agents.Agent
	createErr error

	created []agents.NewAgent
	filter  agents.ListFilter
	updated []agents.UpdateInput
	deleted []uuid.UUID
	actors  []agents.Actor
	writes  int
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{
		roles:    map[[2]uuid.UUID]authz.Role{},
		runtimes: map[uuid.UUID]agents.RuntimeOwner{},
		agents:   map[uuid.UUID]agents.Agent{},
	}
}

func (f *fakeRegistry) Role(_ context.Context, workspaceID, userID uuid.UUID) (authz.Role, error) {
	r, ok := f.roles[[2]uuid.UUID{workspaceID, userID}]
	if !ok {
		return "", authz.ErrNotMember
	}
	return r, nil
}

func (f *fakeRegistry) Runtime(_ context.Context, id uuid.UUID) (agents.RuntimeOwner, error) {
	rt, ok := f.runtimes[id]
	if !ok {
		return agents.RuntimeOwner{}, agents.ErrRuntimeNotFound
	}
	return rt, nil
}

func (f *fakeRegistry) Create(_ context.Context, n agents.NewAgent) (agents.Agent, error) {
	f.writes++
	f.created = append(f.created, n)
	if f.createErr != nil {
		return agents.Agent{}, f.createErr
	}
	return agents.Agent{
		ID: uuid.New(), WorkspaceID: n.Runtime.WorkspaceID, RuntimeID: n.Runtime.ID, Provider: n.Provider, Name: n.Name,
		DisplayName: n.DisplayName, Description: n.Description, Capabilities: n.Capabilities, Status: agents.StatusOffline,
	}, nil
}

func (f *fakeRegistry) Get(_ context.Context, id uuid.UUID) (agents.Agent, error) {
	a, ok := f.agents[id]
	if !ok {
		return agents.Agent{}, agents.ErrNotFound
	}
	return a, nil
}

func (f *fakeRegistry) List(_ context.Context, filter agents.ListFilter) ([]agents.Agent, error) {
	f.filter = filter
	return f.list, nil
}

func (f *fakeRegistry) RuntimeAgents(_ context.Context, runtimeID uuid.UUID) ([]agents.Agent, error) {
	f.filter = agents.ListFilter{RuntimeID: runtimeID}
	return f.list, nil
}

func (f *fakeRegistry) Update(_ context.Context, id uuid.UUID, actor agents.Actor, in agents.UpdateInput) (agents.Agent, error) {
	f.writes++
	f.updated = append(f.updated, in)
	f.actors = append(f.actors, actor)
	return f.agents[id], nil
}

func (f *fakeRegistry) Delete(_ context.Context, id uuid.UUID, actor agents.Actor) error {
	f.writes++
	f.deleted = append(f.deleted, id)
	f.actors = append(f.actors, actor)
	return nil
}

func user(id uuid.UUID) auth.Principal { return auth.Principal{Kind: auth.KindUser, ID: id} }

type world struct {
	repo                       *fakeRegistry
	reg                        *agents.Registry
	ws, otherWS                uuid.UUID
	owner, admin, dev, pm, out uuid.UUID
	rt, pmRT, foreignRT        agents.RuntimeOwner
	agent                      agents.Agent
}

func newWorld() world {
	w := world{
		repo: newFakeRegistry(), ws: uuid.New(), otherWS: uuid.New(),
		owner: uuid.New(), admin: uuid.New(), dev: uuid.New(), pm: uuid.New(), out: uuid.New(),
	}
	for id, role := range map[uuid.UUID]authz.Role{
		w.owner: authz.RoleOwner, w.admin: authz.RoleAdmin, w.dev: authz.RoleDeveloper, w.pm: authz.RoleProjectManager,
	} {
		w.repo.roles[[2]uuid.UUID{w.ws, id}] = role
	}
	w.repo.roles[[2]uuid.UUID{w.otherWS, w.out}] = authz.RoleOwner
	w.rt = agents.RuntimeOwner{ID: uuid.New(), WorkspaceID: w.ws, OwnerID: w.dev}
	w.pmRT = agents.RuntimeOwner{ID: uuid.New(), WorkspaceID: w.ws, OwnerID: w.pm}
	w.foreignRT = agents.RuntimeOwner{ID: uuid.New(), WorkspaceID: w.otherWS, OwnerID: w.out}
	for _, rt := range []agents.RuntimeOwner{w.rt, w.pmRT, w.foreignRT} {
		w.repo.runtimes[rt.ID] = rt
	}
	w.agent = agents.Agent{ID: uuid.New(), WorkspaceID: w.ws, RuntimeID: w.rt.ID, Provider: "codex", Name: "codex"}
	w.repo.agents[w.agent.ID] = w.agent
	w.reg = agents.NewRegistry(w.repo, authz.New(w.repo))
	return w
}

func assertKind(t *testing.T, err error, want errs.Kind) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		return
	}
	e, ok := errs.As(err)
	if !ok || e.Kind != want {
		t.Fatalf("err = %v, want kind %s", err, want)
	}
}

func TestRegisterStoresAnOfflineAgentForTheRuntimesWorkspace(t *testing.T) {
	w := newWorld()
	caps := contracts.AgentCapabilities{SupportsCancel: ptr(true)}

	got, err := w.reg.Register(t.Context(), user(w.dev), w.rt.ID, agents.RegisterInput{
		Provider: "claude_code", Name: "claude", DisplayName: "Ada's Claude", Description: "reviews PRs", Capabilities: caps,
	})

	assertKind(t, err, "")
	if got.ID == uuid.Nil || got.Status != agents.StatusOffline {
		t.Errorf("agent = %+v", got)
	}
	if len(w.repo.created) != 1 {
		t.Fatalf("created %d agents, want 1", len(w.repo.created))
	}
	n := w.repo.created[0]
	want := agents.NewAgent{
		Runtime: agents.Runtime{ID: w.rt.ID, WorkspaceID: w.ws},
		RegisterInput: agents.RegisterInput{
			Provider: "claude_code", Name: "claude", DisplayName: "Ada's Claude", Description: "reviews PRs", Capabilities: caps,
		},
		Actor: agents.Actor{Type: agents.ActorUser, ID: w.dev},
	}
	if n.Runtime != want.Runtime || n.Actor != want.Actor || n.Provider != want.Provider || n.Name != want.Name ||
		n.DisplayName != want.DisplayName || n.Description != want.Description || *n.Capabilities.SupportsCancel != true {
		t.Errorf("stored %+v, want %+v", n, want)
	}
}

func TestRegisterChecksWhoMayRegisterOnTheRuntime(t *testing.T) {
	w := newWorld()
	for _, tc := range []struct {
		name    string
		caller  uuid.UUID
		runtime uuid.UUID
		want    errs.Kind
	}{
		{"the runtime's owner", w.dev, w.rt.ID, ""},
		{"a workspace owner who doesn't own the runtime", w.owner, w.rt.ID, errs.KindForbidden},
		{"an admin who doesn't own the runtime", w.admin, w.rt.ID, errs.KindForbidden},
		{"a project manager on their own runtime", w.pm, w.pmRT.ID, errs.KindForbidden},
		{"a user from another workspace", w.out, w.rt.ID, errs.KindNotFound},
		{"a member naming a runtime in another workspace", w.dev, w.foreignRT.ID, errs.KindNotFound},
		{"an unknown runtime", w.dev, uuid.New(), errs.KindNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w.repo.writes = 0
			_, err := w.reg.Register(t.Context(), user(tc.caller), tc.runtime, agents.RegisterInput{Provider: "echo", Name: "echo"})
			assertKind(t, err, tc.want)
			if tc.want != "" && w.repo.writes != 0 {
				t.Error("stored an agent for a rejected registration")
			}
			if tc.want == errs.KindNotFound && !strings.Contains(err.Error(), "runtime not found") {
				t.Errorf("err = %v, want the runtime reported missing", err)
			}
		})
	}
}

func TestRegisterValidatesInput(t *testing.T) {
	long := strings.Repeat("x", 129)
	for _, tc := range []struct {
		name  string
		in    agents.RegisterInput
		field string
	}{
		{"no provider", agents.RegisterInput{Name: "a"}, "provider"},
		{"provider with spaces", agents.RegisterInput{Provider: "Claude Code", Name: "a"}, "provider"},
		{"provider too long", agents.RegisterInput{Provider: strings.Repeat("a", 65), Name: "a"}, "provider"},
		{"no name", agents.RegisterInput{Provider: "echo"}, "name"},
		{"name too long", agents.RegisterInput{Provider: "echo", Name: long}, "name"},
		{"display name too long", agents.RegisterInput{Provider: "echo", Name: "a", DisplayName: long}, "display_name"},
		{"description too long", agents.RegisterInput{Provider: "echo", Name: "a", Description: strings.Repeat("x", 1001)}, "description"},
		{"session scope too long", agents.RegisterInput{Provider: "echo", Name: "a",
			Capabilities: contracts.AgentCapabilities{SessionScope: ptr(strings.Repeat("x", 65))}}, "capabilities.session_scope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld()
			_, err := w.reg.Register(t.Context(), user(w.dev), w.rt.ID, tc.in)
			assertKind(t, err, errs.KindInvalid)
			if e, _ := errs.As(err); e.Fields[tc.field] == "" {
				t.Errorf("fields = %v, want a problem with %s", e.Fields, tc.field)
			}
			if w.repo.writes != 0 {
				t.Error("stored an invalid agent")
			}
		})
	}
	t.Run("no runtime", func(t *testing.T) {
		w := newWorld()
		_, err := w.reg.Register(t.Context(), user(w.dev), uuid.Nil, agents.RegisterInput{Provider: "echo", Name: "echo"})
		assertKind(t, err, errs.KindInvalid)
	})
}

func TestRegisterReportsAnExistingAgentAsAConflict(t *testing.T) {
	w := newWorld()
	w.repo.createErr = agents.ErrAgentExists
	_, err := w.reg.Register(t.Context(), user(w.dev), w.rt.ID, agents.RegisterInput{Provider: "codex", Name: "codex"})
	assertKind(t, err, errs.KindConflict)
}

func TestRegisterForRuntimeUsesTheTokensRuntime(t *testing.T) {
	w := newWorld()
	rt := agents.Runtime{ID: w.pmRT.ID, WorkspaceID: w.ws}

	_, err := w.reg.RegisterForRuntime(t.Context(), rt, agents.RegisterInput{Provider: "echo", Name: "echo"})

	assertKind(t, err, "")
	if got := w.repo.created[0]; got.Runtime != rt || got.Actor != (agents.Actor{Type: agents.ActorRuntime, ID: rt.ID}) {
		t.Errorf("stored %+v, want runtime %+v acting as itself", got, rt)
	}
}

func TestRegisterForRuntimeRejects(t *testing.T) {
	w := newWorld()
	for _, tc := range []struct {
		name string
		rt   agents.Runtime
		in   agents.RegisterInput
		want errs.Kind
	}{
		{"a gone runtime", agents.Runtime{ID: uuid.New(), WorkspaceID: w.ws}, agents.RegisterInput{Provider: "echo", Name: "echo"}, errs.KindUnauthenticated},
		{"invalid input", agents.Runtime{ID: w.rt.ID, WorkspaceID: w.ws}, agents.RegisterInput{Provider: "echo"}, errs.KindInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w.repo.createErr = nil
			if tc.want == errs.KindUnauthenticated {
				w.repo.createErr = agents.ErrRuntimeNotFound
			}
			_, err := w.reg.RegisterForRuntime(t.Context(), tc.rt, tc.in)
			assertKind(t, err, tc.want)
		})
	}
}

func TestListForRuntime(t *testing.T) {
	w := newWorld()
	w.repo.list = []agents.Agent{w.agent}

	got, err := w.reg.ListForRuntime(t.Context(), agents.Runtime{ID: w.rt.ID, WorkspaceID: w.ws})
	assertKind(t, err, "")
	if len(got) != 1 || w.repo.filter.RuntimeID != w.rt.ID {
		t.Errorf("got %v for runtime %s", got, w.repo.filter.RuntimeID)
	}

	for name, rt := range map[string]agents.Runtime{
		"gone":                 {ID: uuid.New(), WorkspaceID: w.ws},
		"in another workspace": {ID: w.rt.ID, WorkspaceID: w.otherWS},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := w.reg.ListForRuntime(t.Context(), rt)
			assertKind(t, err, errs.KindUnauthenticated)
		})
	}
}

func TestGetAgent(t *testing.T) {
	w := newWorld()
	for _, tc := range []struct {
		name   string
		caller uuid.UUID
		id     uuid.UUID
		want   errs.Kind
	}{
		{"any member", w.pm, w.agent.ID, ""},
		{"a non-member", w.out, w.agent.ID, errs.KindNotFound},
		{"a missing agent", w.dev, uuid.New(), errs.KindNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := w.reg.Get(t.Context(), user(tc.caller), tc.id)
			assertKind(t, err, tc.want)
			if tc.want == "" && got.ID != w.agent.ID {
				t.Errorf("got %+v", got)
			}
			if tc.want == errs.KindNotFound && !strings.Contains(err.Error(), "agent not found") {
				t.Errorf("err = %v, want agent not found", err)
			}
		})
	}
}

func TestListAgents(t *testing.T) {
	w := newWorld()
	now := time.Now()
	a, b := agents.Agent{ID: uuid.New(), CreatedAt: now}, agents.Agent{ID: uuid.New(), CreatedAt: now.Add(-time.Second)}
	w.repo.list = []agents.Agent{a, b}

	page, err := w.reg.List(t.Context(), user(w.pm), agents.ListInput{WorkspaceID: w.ws, RuntimeID: w.rt.ID, Limit: 1})
	assertKind(t, err, "")
	if len(page.Items) != 1 || page.Items[0].ID != a.ID || page.Next == nil || *page.Next != (agents.Position{CreatedAt: a.CreatedAt, ID: a.ID}) {
		t.Errorf("page = %+v", page)
	}
	if w.repo.filter != (agents.ListFilter{WorkspaceID: w.ws, RuntimeID: w.rt.ID, Limit: 2}) {
		t.Errorf("filter = %+v", w.repo.filter)
	}

	page, err = w.reg.List(t.Context(), user(w.pm), agents.ListInput{WorkspaceID: w.ws, Limit: 5})
	assertKind(t, err, "")
	if len(page.Items) != 2 || page.Next != nil {
		t.Errorf("last page = %+v", page)
	}

	_, err = w.reg.List(t.Context(), user(w.out), agents.ListInput{WorkspaceID: w.ws, Limit: 5})
	assertKind(t, err, errs.KindNotFound)
	_, err = w.reg.List(t.Context(), user(w.pm), agents.ListInput{Limit: 5})
	assertKind(t, err, errs.KindInvalid)
}

func TestUpdateAndDeleteNeedTheRuntimesOwner(t *testing.T) {
	w := newWorld()
	name := "Reviewer"
	for _, tc := range []struct {
		name   string
		caller uuid.UUID
		id     uuid.UUID
		want   errs.Kind
	}{
		{"the runtime's owner", w.dev, w.agent.ID, ""},
		{"a workspace owner", w.owner, w.agent.ID, errs.KindForbidden},
		{"a project manager", w.pm, w.agent.ID, errs.KindForbidden},
		{"a non-member", w.out, w.agent.ID, errs.KindNotFound},
		{"a missing agent", w.dev, uuid.New(), errs.KindNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w.repo.writes = 0
			_, err := w.reg.Update(t.Context(), user(tc.caller), tc.id, agents.UpdateInput{DisplayName: &name})
			assertKind(t, err, tc.want)
			err = w.reg.Delete(t.Context(), user(tc.caller), tc.id)
			assertKind(t, err, tc.want)
			if tc.want != "" && w.repo.writes != 0 {
				t.Error("wrote for a rejected caller")
			}
		})
	}
	if w.repo.actors[0] != (agents.Actor{Type: agents.ActorUser, ID: w.dev}) || w.repo.deleted[0] != w.agent.ID {
		t.Errorf("actors = %v, deleted = %v", w.repo.actors, w.repo.deleted)
	}
}

func TestUpdateValidatesLabels(t *testing.T) {
	w := newWorld()
	empty, long := "", strings.Repeat("x", 129)

	_, err := w.reg.Update(t.Context(), user(w.dev), w.agent.ID, agents.UpdateInput{DisplayName: &empty, Description: &empty})
	assertKind(t, err, "")

	_, err = w.reg.Update(t.Context(), user(w.dev), w.agent.ID, agents.UpdateInput{DisplayName: &long})
	assertKind(t, err, errs.KindInvalid)
	if e, _ := errs.As(err); e.Fields["display_name"] == "" {
		t.Errorf("fields = %v", e.Fields)
	}
}
