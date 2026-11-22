package agents

import (
	"context"
	"errors"
	"regexp"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/authz"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
)

const (
	maxProviderLength     = 64
	maxNameLength         = 128
	maxDisplayNameLength  = 128
	maxDescriptionLength  = 1000
	maxSessionScopeLength = 64
)

var providerPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Registry lets users and runtimes register agents before a runtime.hello
// announces them, and lets users view, label and delete agents. A registered
// agent has the same (runtime, provider, name) identity as an announced one,
// so the runtime's next hello takes over the same row.
type Registry struct {
	repo RegistryRepository
	az   Authorizer
}

// NewRegistry returns a Registry backed by repo.
func NewRegistry(repo RegistryRepository, az Authorizer) *Registry {
	return &Registry{repo: repo, az: az}
}

// Register stores an offline agent on runtimeID for p. Only the runtime's
// owner may, and only while their workspace role may manage agents.
func (r *Registry) Register(ctx context.Context, p auth.Principal, runtimeID uuid.UUID, in RegisterInput) (Agent, error) {
	fields := validateRegistration(in)
	if runtimeID == uuid.Nil {
		fields["runtime_id"] = "required"
	}
	if len(fields) > 0 {
		return Agent{}, errs.Invalid("invalid agent registration", fields)
	}
	notFound := errs.NotFound("runtime not found")
	rt, err := r.repo.Runtime(ctx, runtimeID)
	if errors.Is(err, ErrRuntimeNotFound) {
		return Agent{}, notFound
	}
	if err != nil {
		return Agent{}, err
	}
	if err := r.canManage(ctx, p, rt, notFound); err != nil {
		return Agent{}, err
	}
	agent, err := r.repo.Create(ctx, NewAgent{
		Runtime:       Runtime{ID: rt.ID, WorkspaceID: rt.WorkspaceID},
		RegisterInput: in,
		Actor:         Actor{Type: ActorUser, ID: p.ID},
	})
	return agent, createError(err, notFound)
}

// RegisterForRuntime stores an offline agent on rt, the runtime a verified
// runtime access token names. The token is the authorization: it can do no
// more than the runtime's own hello could.
func (r *Registry) RegisterForRuntime(ctx context.Context, rt Runtime, in RegisterInput) (Agent, error) {
	if fields := validateRegistration(in); len(fields) > 0 {
		return Agent{}, errs.Invalid("invalid agent registration", fields)
	}
	agent, err := r.repo.Create(ctx, NewAgent{Runtime: rt, RegisterInput: in, Actor: Actor{Type: ActorRuntime, ID: rt.ID}})
	return agent, createError(err, errs.Unauthenticated("invalid runtime access token"))
}

// ListForRuntime returns every agent on rt, the runtime a verified runtime
// access token names.
func (r *Registry) ListForRuntime(ctx context.Context, rt Runtime) ([]Agent, error) {
	owner, err := r.repo.Runtime(ctx, rt.ID)
	if errors.Is(err, ErrRuntimeNotFound) || (err == nil && owner.WorkspaceID != rt.WorkspaceID) {
		return nil, errs.Unauthenticated("invalid runtime access token")
	}
	if err != nil {
		return nil, err
	}
	return r.repo.RuntimeAgents(ctx, rt.ID)
}

// Get returns an agent any member of its workspace can see.
func (r *Registry) Get(ctx context.Context, p auth.Principal, id uuid.UUID) (Agent, error) {
	notFound := errs.NotFound("agent not found")
	a, err := r.get(ctx, id, notFound)
	if err != nil {
		return Agent{}, err
	}
	if err := r.az.Can(ctx, p, authz.ActionViewAgents, authz.Workspace(a.WorkspaceID)); err != nil {
		return Agent{}, hide(err, notFound)
	}
	return a, nil
}

// List returns a page of in.WorkspaceID's agents, newest first.
func (r *Registry) List(ctx context.Context, p auth.Principal, in ListInput) (Page, error) {
	if in.WorkspaceID == uuid.Nil {
		return Page{}, errs.Invalid("invalid agent query", map[string]string{"workspace_id": "required"})
	}
	if err := r.az.Can(ctx, p, authz.ActionViewAgents, authz.Workspace(in.WorkspaceID)); err != nil {
		return Page{}, err
	}
	items, err := r.repo.List(ctx, ListFilter{WorkspaceID: in.WorkspaceID, RuntimeID: in.RuntimeID, After: in.After, Limit: in.Limit + 1})
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

// Update changes an agent's labels. Only the owner of the agent's runtime may.
func (r *Registry) Update(ctx context.Context, p auth.Principal, id uuid.UUID, in UpdateInput) (Agent, error) {
	fields := map[string]string{}
	if in.DisplayName != nil && utf8.RuneCountInString(*in.DisplayName) > maxDisplayNameLength {
		fields["display_name"] = "must be at most 128 characters"
	}
	if in.Description != nil && utf8.RuneCountInString(*in.Description) > maxDescriptionLength {
		fields["description"] = "must be at most 1000 characters"
	}
	if len(fields) > 0 {
		return Agent{}, errs.Invalid("invalid agent update", fields)
	}
	if err := r.authorizeWrite(ctx, p, id); err != nil {
		return Agent{}, err
	}
	a, err := r.repo.Update(ctx, id, Actor{Type: ActorUser, ID: p.ID}, in)
	if errors.Is(err, ErrNotFound) {
		return Agent{}, errs.NotFound("agent not found")
	}
	return a, err
}

// Delete removes an agent and its project grants. Only the owner of the
// agent's runtime may. A runtime that still announces the agent recreates it,
// with a new id and no grants, on its next hello.
func (r *Registry) Delete(ctx context.Context, p auth.Principal, id uuid.UUID) error {
	if err := r.authorizeWrite(ctx, p, id); err != nil {
		return err
	}
	err := r.repo.Delete(ctx, id, Actor{Type: ActorUser, ID: p.ID})
	if errors.Is(err, ErrNotFound) {
		return errs.NotFound("agent not found")
	}
	return err
}

func (r *Registry) authorizeWrite(ctx context.Context, p auth.Principal, id uuid.UUID) error {
	notFound := errs.NotFound("agent not found")
	a, err := r.get(ctx, id, notFound)
	if err != nil {
		return err
	}
	rt, err := r.repo.Runtime(ctx, a.RuntimeID)
	if errors.Is(err, ErrRuntimeNotFound) {
		return notFound
	}
	if err != nil {
		return err
	}
	return r.canManage(ctx, p, rt, notFound)
}

func (r *Registry) canManage(ctx context.Context, p auth.Principal, rt RuntimeOwner, notFound error) error {
	if err := r.az.Can(ctx, p, authz.ActionManageAgents, authz.Workspace(rt.WorkspaceID)); err != nil {
		return hide(err, notFound)
	}
	if rt.OwnerID != p.ID {
		return errs.Forbidden("only the runtime's owner can manage its agents")
	}
	return nil
}

func (r *Registry) get(ctx context.Context, id uuid.UUID, notFound error) (Agent, error) {
	a, err := r.repo.Get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return Agent{}, notFound
	}
	return a, err
}

func hide(err, notFound error) error {
	if e, ok := errs.As(err); ok && e.Kind == errs.KindNotFound {
		return notFound
	}
	return err
}

func createError(err, runtimeGone error) error {
	switch {
	case errors.Is(err, ErrAgentExists):
		return errs.Conflict("the runtime already has an agent with this provider and name")
	case errors.Is(err, ErrRuntimeNotFound):
		return runtimeGone
	}
	return err
}

func validateRegistration(in RegisterInput) map[string]string {
	fields := map[string]string{}
	switch {
	case in.Provider == "":
		fields["provider"] = "required"
	case len(in.Provider) > maxProviderLength || !providerPattern.MatchString(in.Provider):
		fields["provider"] = "must be at most 64 lowercase letters, digits or underscores, starting with a letter"
	}
	switch n := utf8.RuneCountInString(in.Name); {
	case n == 0:
		fields["name"] = "required"
	case n > maxNameLength:
		fields["name"] = "must be at most 128 characters"
	}
	if utf8.RuneCountInString(in.DisplayName) > maxDisplayNameLength {
		fields["display_name"] = "must be at most 128 characters"
	}
	if utf8.RuneCountInString(in.Description) > maxDescriptionLength {
		fields["description"] = "must be at most 1000 characters"
	}
	if s := in.Capabilities.SessionScope; s != nil && utf8.RuneCountInString(*s) > maxSessionScopeLength {
		fields["capabilities.session_scope"] = "must be at most 64 characters"
	}
	return fields
}
