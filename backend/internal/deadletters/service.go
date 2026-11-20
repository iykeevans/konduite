// Package deadletters lets platform admins inspect and replay events that
// consumers gave up on (the AGENTHUB_DLQ stream).
package deadletters

import (
	"context"
	"errors"

	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
	"github.com/iykeevans/konduite/backend/internal/platform/nats"
)

// Store reads and replays DLQ entries. *nats.Client satisfies it.
type Store interface {
	DeadLetters(ctx context.Context, after uint64, limit int) ([]nats.DeadLetter, error)
	ReplayDeadLetter(ctx context.Context, seq uint64) error
}

// Gate decides who is a platform admin. admin.Allowlist satisfies it.
type Gate interface {
	Check(p auth.Principal) error
}

// Service applies the admin gate in front of the DLQ.
type Service struct {
	store Store
	gate  Gate
}

// NewService returns a Service over store, allowing only callers gate passes.
func NewService(store Store, gate Gate) *Service {
	return &Service{store: store, gate: gate}
}

// Page is one page of entries. Next is the cursor sequence for the
// following page, or 0 on the last one.
type Page struct {
	Items []nats.DeadLetter
	Next  uint64
}

// List returns up to limit entries after sequence after, oldest first.
func (s *Service) List(ctx context.Context, p auth.Principal, after uint64, limit int) (Page, error) {
	if err := s.gate.Check(p); err != nil {
		return Page{}, err
	}
	items, err := s.store.DeadLetters(ctx, after, limit+1)
	if err != nil {
		return Page{}, err
	}
	var next uint64
	if len(items) > limit {
		items = items[:limit]
		next = items[limit-1].Seq
	}
	return Page{Items: items, Next: next}, nil
}

// Replay republishes entry seq to its original subject and removes it.
func (s *Service) Replay(ctx context.Context, p auth.Principal, seq uint64) error {
	if err := s.gate.Check(p); err != nil {
		return err
	}
	err := s.store.ReplayDeadLetter(ctx, seq)
	switch {
	case errors.Is(err, nats.ErrDeadLetterNotFound):
		return errs.NotFound("dead letter not found")
	case errors.Is(err, nats.ErrNotReplayable):
		return errs.Conflict("dead letter has no original subject to replay to")
	}
	return err
}
