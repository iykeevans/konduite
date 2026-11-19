package conversations

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/authz"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
)

const maxTitleLength = 200

// Service creates, reads and lists conversations, checking authorization
// on every call.
type Service struct {
	repo Repository
	az   Authorizer
}

// NewService returns a Service backed by repo.
func NewService(repo Repository, az Authorizer) *Service {
	return &Service{repo: repo, az: az}
}

// Create starts a conversation in in.WorkspaceID with p as its creator and
// first participant.
func (s *Service) Create(ctx context.Context, p auth.Principal, in CreateInput) (Conversation, error) {
	title := strings.TrimSpace(in.Title)
	fields := map[string]string{}
	if in.WorkspaceID == uuid.Nil {
		fields["workspace_id"] = "required"
	}
	if utf8.RuneCountInString(title) > maxTitleLength {
		fields["title"] = "must be at most 200 characters"
	}
	if len(fields) > 0 {
		return Conversation{}, errs.Invalid("invalid conversation", fields)
	}
	if err := s.az.Can(ctx, p, authz.ActionCreateConversation, authz.Workspace(in.WorkspaceID)); err != nil {
		return Conversation{}, err
	}
	c, err := s.repo.Create(ctx, NewConversation{
		WorkspaceID: in.WorkspaceID, ProjectID: in.ProjectID, Title: title, CreatedBy: p.ID,
	})
	if errors.Is(err, ErrProjectNotFound) {
		return Conversation{}, errs.Invalid("invalid conversation",
			map[string]string{"project_id": "not found in this workspace"})
	}
	return c, err
}

// Get returns a conversation and its participants. A conversation in a
// workspace p cannot see gets the same error as one that does not exist.
func (s *Service) Get(ctx context.Context, p auth.Principal, id uuid.UUID) (Conversation, error) {
	notFound := errs.NotFound("conversation not found")
	c, err := s.repo.Get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return Conversation{}, notFound
	}
	if err != nil {
		return Conversation{}, err
	}
	if err := s.az.Can(ctx, p, authz.ActionViewConversations, authz.Workspace(c.WorkspaceID)); err != nil {
		if e, ok := errs.As(err); ok && e.Kind == errs.KindNotFound {
			return Conversation{}, notFound
		}
		return Conversation{}, err
	}
	return c, nil
}

// List returns a page of in.WorkspaceID's conversations, newest first.
func (s *Service) List(ctx context.Context, p auth.Principal, in ListInput) (Page, error) {
	if in.WorkspaceID == uuid.Nil {
		return Page{}, errs.Invalid("invalid conversation query", map[string]string{"workspace_id": "required"})
	}
	if err := s.az.Can(ctx, p, authz.ActionViewConversations, authz.Workspace(in.WorkspaceID)); err != nil {
		return Page{}, err
	}
	items, err := s.repo.List(ctx, ListFilter{
		WorkspaceID: in.WorkspaceID, ProjectID: in.ProjectID, After: in.After, Limit: in.Limit + 1,
	})
	if err != nil {
		return Page{}, err
	}
	if len(items) <= in.Limit {
		return Page{Items: items}, nil
	}
	items = items[:in.Limit]
	last := items[len(items)-1]
	return Page{Items: items, Next: &Position{CreatedAt: last.CreatedAt, ID: last.ID}}, nil
}
