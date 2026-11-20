package deadletters_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/admin"
	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/deadletters"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
	"github.com/iykeevans/konduite/backend/internal/platform/nats"
)

type fakeStore struct {
	entries  []nats.DeadLetter
	replayed []uint64
	err      error
}

func (f *fakeStore) DeadLetters(_ context.Context, after uint64, limit int) ([]nats.DeadLetter, error) {
	if f.err != nil {
		return nil, f.err
	}
	var out []nats.DeadLetter
	for _, e := range f.entries {
		if e.Seq > after && len(out) < limit {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeStore) ReplayDeadLetter(_ context.Context, seq uint64) error {
	if f.err != nil {
		return f.err
	}
	for _, e := range f.entries {
		if e.Seq == seq {
			f.replayed = append(f.replayed, seq)
			return nil
		}
	}
	return nats.ErrDeadLetterNotFound
}

var adminUser = auth.Principal{Kind: auth.KindUser, ID: uuid.New()}

func newService(store *fakeStore) *deadletters.Service {
	return deadletters.NewService(store, admin.NewAllowlist([]uuid.UUID{adminUser.ID}))
}

func entries(n int) []nats.DeadLetter {
	out := make([]nats.DeadLetter, n)
	for i := range out {
		out[i] = nats.DeadLetter{Seq: uint64(i + 1), Subject: "agenthub.x"}
	}
	return out
}

func wantKind(t *testing.T, err error, kind errs.Kind) {
	t.Helper()
	if e, ok := errs.As(err); !ok || e.Kind != kind {
		t.Errorf("err = %v, want %s", err, kind)
	}
}

func TestListPagesThroughTheDLQ(t *testing.T) {
	svc := newService(&fakeStore{entries: entries(3)})

	page, err := svc.List(t.Context(), adminUser, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Next != 2 {
		t.Fatalf("first page = %d items, next %d; want 2, 2", len(page.Items), page.Next)
	}
	page, err = svc.List(t.Context(), adminUser, page.Next, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Seq != 3 || page.Next != 0 {
		t.Errorf("last page = %+v, want seq 3 and no next", page)
	}
}

func TestNonAdminsCannotSeeOrReplay(t *testing.T) {
	store := &fakeStore{entries: entries(1)}
	svc := newService(store)
	other := auth.Principal{Kind: auth.KindUser, ID: uuid.New()}

	_, err := svc.List(t.Context(), other, 0, 10)
	wantKind(t, err, errs.KindNotFound)
	wantKind(t, svc.Replay(t.Context(), other, 1), errs.KindNotFound)
	if len(store.replayed) != 0 {
		t.Error("a non-admin replay reached the store")
	}
}

func TestReplay(t *testing.T) {
	store := &fakeStore{entries: entries(2)}
	svc := newService(store)

	if err := svc.Replay(t.Context(), adminUser, 2); err != nil {
		t.Fatal(err)
	}
	if len(store.replayed) != 1 || store.replayed[0] != 2 {
		t.Errorf("replayed = %v, want [2]", store.replayed)
	}
	wantKind(t, svc.Replay(t.Context(), adminUser, 9), errs.KindNotFound)

	store.err = nats.ErrNotReplayable
	wantKind(t, svc.Replay(t.Context(), adminUser, 1), errs.KindConflict)

	store.err = errors.New("nats down")
	if err := svc.Replay(t.Context(), adminUser, 1); err == nil {
		t.Error("store failure: want an error")
	} else if _, ok := errs.As(err); ok {
		t.Errorf("store failure became a domain error %v, want an internal error", err)
	}
}
