package agents

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/platform/errs"
	"github.com/iykeevans/konduite/packages/contracts"
)

// Service records the agents runtimes announce.
type Service struct {
	repo Repository
}

// NewService returns a Service backed by repo.
func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// SyncFromHello makes rt's stored agents match the ones hello announces:
// announced agents are created or updated and marked online, keeping their
// ids across reconnects; agents it no longer announces are marked offline.
// hello must already have passed contract validation. A runtime that no
// longer exists returns errs.NotFound.
func (s *Service) SyncFromHello(ctx context.Context, rt Runtime, hello contracts.RuntimeHello) ([]Agent, error) {
	if rt.ID == uuid.Nil || rt.WorkspaceID == uuid.Nil {
		return nil, errors.New("agents: runtime and workspace ids are required")
	}
	type key struct{ provider, name string }
	seen := make(map[key]bool, len(hello.Agents))
	announced := make([]Announcement, 0, len(hello.Agents))
	for i, a := range hello.Agents {
		k := key{a.Provider, a.Name}
		if seen[k] {
			return nil, errs.Invalid("invalid runtime hello", map[string]string{
				fmt.Sprintf("agents[%d]", i): "duplicate provider and name",
			})
		}
		seen[k] = true
		n := Announcement{Provider: a.Provider, Name: a.Name}
		if a.Version != nil {
			n.Version = *a.Version
		}
		if a.Capabilities != nil {
			n.Capabilities = *a.Capabilities
		}
		announced = append(announced, n)
	}

	agents, err := s.repo.Sync(ctx, rt, announced)
	if errors.Is(err, ErrRuntimeNotFound) {
		return nil, errs.NotFound("runtime not found")
	}
	return agents, err
}

// HasProjectAccess reports whether agentID has been granted access to projectID.
func (s *Service) HasProjectAccess(ctx context.Context, agentID, projectID uuid.UUID) (bool, error) {
	return s.repo.HasProjectAccess(ctx, agentID, projectID)
}
