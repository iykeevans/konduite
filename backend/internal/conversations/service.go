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
	projectNotFound := errs.Invalid("invalid conversation", map[string]string{"project_id": "not found in this workspace"})
	if in.ProjectID != nil {
		err := s.az.Can(ctx, p, authz.ActionContribute, authz.Resource{WorkspaceID: in.WorkspaceID, ProjectID: *in.ProjectID})
		if isNotFound(err) {
			return Conversation{}, projectNotFound
		}
		if err != nil {
			return Conversation{}, err
		}
	}
	c, err := s.repo.Create(ctx, NewConversation{
		WorkspaceID: in.WorkspaceID, ProjectID: in.ProjectID, Title: title, CreatedBy: p.ID,
	})
	if errors.Is(err, ErrProjectNotFound) {
		return Conversation{}, projectNotFound
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
	if err := s.canView(ctx, p, c); err != nil {
		if e, ok := errs.As(err); ok && (e.Kind == errs.KindNotFound || e.Kind == errs.KindForbidden) {
			return Conversation{}, notFound
		}
		return Conversation{}, err
	}
	return c, nil
}

// canView checks that p may read c: any workspace member for a
// workspace-wide conversation, ActionViewProject for one in a project.
func (s *Service) canView(ctx context.Context, p auth.Principal, c Conversation) error {
	if c.ProjectID != nil {
		return s.az.Can(ctx, p, authz.ActionViewProject, authz.Resource{WorkspaceID: c.WorkspaceID, ProjectID: *c.ProjectID})
	}
	return s.az.Can(ctx, p, authz.ActionViewConversations, authz.Workspace(c.WorkspaceID))
}

// canContribute checks that p may change c: the project.contribute action
// for a conversation in a project, and starting a conversation in its
// workspace (every member) for a workspace-wide one.
func (s *Service) canContribute(ctx context.Context, p auth.Principal, c Conversation) error {
	if c.ProjectID != nil {
		return s.az.Can(ctx, p, authz.ActionContribute, authz.Resource{WorkspaceID: c.WorkspaceID, ProjectID: *c.ProjectID})
	}
	return s.az.Can(ctx, p, authz.ActionCreateConversation, authz.Workspace(c.WorkspaceID))
}

// AddParticipant adds a user, agent or runtime to the conversation. It
// returns created false, and the stored participant, when it was already
// there. A user must be able to read the conversation; an agent or runtime
// must belong to its workspace.
func (s *Service) AddParticipant(ctx context.Context, p auth.Principal, conversationID uuid.UUID, in ParticipantInput) (Participant, bool, error) {
	if err := validateParticipant(in.Kind, in.ID); err != nil {
		return Participant{}, false, err
	}
	c, err := s.Changeable(ctx, p, conversationID)
	if err != nil {
		return Participant{}, false, err
	}
	notInWorkspace := errs.Invalid("invalid participant", map[string]string{"id": "not found in this conversation's workspace"})
	if in.Kind == KindUser {
		err := s.canView(ctx, auth.Principal{Kind: auth.KindUser, ID: in.ID}, c)
		if e, ok := errs.As(err); ok && (e.Kind == errs.KindNotFound || e.Kind == errs.KindForbidden) {
			return Participant{}, false, errs.Invalid("invalid participant",
				map[string]string{"id": "is not a user who can read this conversation"})
		}
		if err != nil {
			return Participant{}, false, err
		}
	}
	added, created, err := s.repo.AddParticipant(ctx, c.ID, c.WorkspaceID, in.Kind, in.ID)
	if errors.Is(err, ErrParticipantNotInWorkspace) {
		return Participant{}, false, notInWorkspace
	}
	if errors.Is(err, ErrNotFound) {
		return Participant{}, false, errs.NotFound("conversation not found")
	}
	return added, created, err
}

// RemoveParticipant removes a participant from the conversation. Removing
// one that is not there succeeds.
func (s *Service) RemoveParticipant(ctx context.Context, p auth.Principal, conversationID uuid.UUID, kind ParticipantKind, id uuid.UUID) error {
	if err := validateParticipant(kind, id); err != nil {
		return err
	}
	c, err := s.Changeable(ctx, p, conversationID)
	if err != nil {
		return err
	}
	return s.repo.RemoveParticipant(ctx, c.ID, kind, id)
}

// Changeable returns the conversation if p may contribute to it (post
// messages, manage participants): the same 404 as Get when p cannot read it,
// 403 when p can read but not contribute.
func (s *Service) Changeable(ctx context.Context, p auth.Principal, id uuid.UUID) (Conversation, error) {
	c, err := s.Get(ctx, p, id)
	if err != nil {
		return Conversation{}, err
	}
	if err := s.canContribute(ctx, p, c); err != nil {
		return Conversation{}, err
	}
	return c, nil
}

func validateParticipant(kind ParticipantKind, id uuid.UUID) error {
	fields := map[string]string{}
	switch kind {
	case KindUser, KindAgent, KindRuntime:
	default:
		fields["type"] = "must be user, agent or runtime"
	}
	if id == uuid.Nil {
		fields["id"] = "required"
	}
	if len(fields) > 0 {
		return errs.Invalid("invalid participant", fields)
	}
	return nil
}

func isNotFound(err error) bool {
	e, ok := errs.As(err)
	return ok && e.Kind == errs.KindNotFound
}

// List returns a page of in.WorkspaceID's conversations, newest first.
func (s *Service) List(ctx context.Context, p auth.Principal, in ListInput) (Page, error) {
	if in.WorkspaceID == uuid.Nil {
		return Page{}, errs.Invalid("invalid conversation query", map[string]string{"workspace_id": "required"})
	}
	if err := s.az.Can(ctx, p, authz.ActionViewConversations, authz.Workspace(in.WorkspaceID)); err != nil {
		return Page{}, err
	}
	if in.ProjectID != nil {
		if err := s.az.Can(ctx, p, authz.ActionViewProject, authz.Resource{WorkspaceID: in.WorkspaceID, ProjectID: *in.ProjectID}); err != nil {
			return Page{}, err
		}
	}
	items, err := s.repo.List(ctx, ListFilter{
		WorkspaceID: in.WorkspaceID, ProjectID: in.ProjectID, ViewerID: p.ID, After: in.After, Limit: in.Limit + 1,
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
