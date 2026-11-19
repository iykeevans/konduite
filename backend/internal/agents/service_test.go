package agents_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/agents"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
	"github.com/iykeevans/konduite/packages/contracts"
)

type fakeRepo struct {
	syncCalls int
	gotRT     agents.Runtime
	got       []agents.Announcement
	agents    []agents.Agent
	err       error
}

func (f *fakeRepo) Sync(_ context.Context, rt agents.Runtime, announced []agents.Announcement) ([]agents.Agent, error) {
	f.syncCalls++
	f.gotRT, f.got = rt, announced
	return f.agents, f.err
}

func (f *fakeRepo) HasProjectAccess(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return false, nil
}

func ptr[T any](v T) *T { return &v }

func TestSyncFromHelloPassesEveryAnnouncedAgentToTheRepository(t *testing.T) {
	repo := &fakeRepo{agents: []agents.Agent{{ID: uuid.New()}}}
	svc := agents.NewService(repo)
	rt := agents.Runtime{ID: uuid.New(), WorkspaceID: uuid.New()}
	caps := contracts.AgentCapabilities{SupportsCancel: ptr(true), SessionScope: ptr("project")}

	got, err := svc.SyncFromHello(t.Context(), rt, contracts.RuntimeHello{
		RuntimeVersion: "0.1.0",
		Agents: []contracts.AnnouncedAgent{
			{Provider: "claude_code", Name: "claude", Version: ptr("2.1.0"), Capabilities: &caps},
			{Provider: "echo", Name: "echo"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, repo.agents) {
		t.Errorf("agents = %+v, want the repository's %+v", got, repo.agents)
	}
	if repo.gotRT != rt {
		t.Errorf("runtime = %+v, want %+v", repo.gotRT, rt)
	}
	want := []agents.Announcement{
		{Provider: "claude_code", Name: "claude", Version: "2.1.0", Capabilities: caps},
		{Provider: "echo", Name: "echo"},
	}
	if !reflect.DeepEqual(repo.got, want) {
		t.Errorf("announced = %+v, want %+v", repo.got, want)
	}
}

func TestSyncFromHelloWithNoAgentsStillSyncs(t *testing.T) {
	repo := &fakeRepo{}
	_, err := agents.NewService(repo).SyncFromHello(t.Context(),
		agents.Runtime{ID: uuid.New(), WorkspaceID: uuid.New()}, contracts.RuntimeHello{RuntimeVersion: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	if repo.syncCalls != 1 || len(repo.got) != 0 {
		t.Errorf("sync calls = %d with %d agents, want 1 call with none", repo.syncCalls, len(repo.got))
	}
}

func TestSyncFromHelloRejects(t *testing.T) {
	rt := agents.Runtime{ID: uuid.New(), WorkspaceID: uuid.New()}
	for _, tc := range []struct {
		name  string
		rt    agents.Runtime
		hello contracts.RuntimeHello
		kind  errs.Kind
	}{
		{
			name: "the same agent twice",
			rt:   rt,
			hello: contracts.RuntimeHello{Agents: []contracts.AnnouncedAgent{
				{Provider: "codex", Name: "codex"}, {Provider: "codex", Name: "codex", Version: ptr("2")},
			}},
			kind: errs.KindInvalid,
		},
		{name: "a runtime without an id", rt: agents.Runtime{WorkspaceID: rt.WorkspaceID}},
		{name: "a runtime without a workspace", rt: agents.Runtime{ID: rt.ID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeRepo{}
			_, err := agents.NewService(repo).SyncFromHello(t.Context(), tc.rt, tc.hello)
			if err == nil {
				t.Fatal("err = nil, want an error")
			}
			e, ok := errs.As(err)
			if tc.kind == "" && ok {
				t.Errorf("err = %v, want a plain error", err)
			}
			if tc.kind != "" && (!ok || e.Kind != tc.kind) {
				t.Errorf("err = %v, want kind %s", err, tc.kind)
			}
			if repo.syncCalls != 0 {
				t.Error("repository called for a rejected hello")
			}
		})
	}
}

func TestSyncFromHelloReportsAMissingRuntimeAsNotFound(t *testing.T) {
	repo := &fakeRepo{err: agents.ErrRuntimeNotFound}
	_, err := agents.NewService(repo).SyncFromHello(t.Context(),
		agents.Runtime{ID: uuid.New(), WorkspaceID: uuid.New()}, contracts.RuntimeHello{})
	if e, ok := errs.As(err); !ok || e.Kind != errs.KindNotFound {
		t.Errorf("err = %v, want not found", err)
	}
}

func TestSyncFromHelloPassesStorageErrorsThrough(t *testing.T) {
	boom := errors.New("boom")
	repo := &fakeRepo{err: boom}
	_, err := agents.NewService(repo).SyncFromHello(t.Context(),
		agents.Runtime{ID: uuid.New(), WorkspaceID: uuid.New()}, contracts.RuntimeHello{})
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want %v", err, boom)
	}
}
