package worker_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/iykeevans/konduite/backend/internal/worker"
)

func runAsync(ctx context.Context, r *worker.Registry, drain time.Duration) <-chan error {
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx, drain) }()
	return done
}

func TestRunDrainsInFlightWorkOnShutdown(t *testing.T) {
	started, finish := make(chan struct{}), make(chan struct{})
	finished := false
	r := worker.NewRegistry()
	if err := r.Add("relay", func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		<-finish // the in-flight job keeps going after cancellation
		finished = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := runAsync(ctx, r, 5*time.Second)
	<-started
	cancel()

	select {
	case err := <-done:
		t.Fatalf("Run returned %v before the in-flight job finished", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(finish)
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil after a clean drain", err)
	}
	if !finished {
		t.Error("consumer did not finish its job")
	}
}

func TestRunReportsConsumerStuckPastDrainTimeout(t *testing.T) {
	stuck := make(chan struct{})
	defer close(stuck)
	r := worker.NewRegistry()
	_ = r.Add("fast", func(ctx context.Context) error { <-ctx.Done(); return nil })
	_ = r.Add("slow-sync", func(context.Context) error { <-stuck; return nil })

	ctx, cancel := context.WithCancel(t.Context())
	done := runAsync(ctx, r, 50*time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "slow-sync") || strings.Contains(err.Error(), "fast") {
			t.Fatalf("Run = %v, want an error naming only slow-sync", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run ignored the drain timeout")
	}
}

func TestRunStopsEverythingWhenOneConsumerFails(t *testing.T) {
	errDB := errors.New("connection reset")
	otherStopped := make(chan struct{})
	r := worker.NewRegistry()
	_ = r.Add("slack-sync", func(context.Context) error { return errDB })
	_ = r.Add("relay", func(ctx context.Context) error { <-ctx.Done(); close(otherStopped); return nil })

	done := runAsync(t.Context(), r, time.Second)

	select {
	case err := <-done:
		if !errors.Is(err, errDB) || !strings.Contains(err.Error(), "slack-sync") {
			t.Fatalf("Run = %v, want errDB wrapped with the consumer name", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run kept going after a consumer failed")
	}
	select {
	case <-otherStopped:
	default:
		t.Error("the other consumer was not cancelled")
	}
}

func TestRunTreatsPanicAsFailure(t *testing.T) {
	r := worker.NewRegistry()
	_ = r.Add("notifier", func(context.Context) error { panic("nil map write") })
	_ = r.Add("relay", func(ctx context.Context) error { <-ctx.Done(); return nil })

	select {
	case err := <-runAsync(t.Context(), r, time.Second):
		if err == nil || !strings.Contains(err.Error(), "notifier") || !strings.Contains(err.Error(), "nil map write") {
			t.Fatalf("Run = %v, want a failure naming notifier and the panic value", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after a consumer panicked")
	}
}

func TestAddRejectsDuplicateNames(t *testing.T) {
	r := worker.NewRegistry()
	noop := func(context.Context) error { return nil }
	if err := r.Add("relay", noop); err != nil {
		t.Fatal(err)
	}
	if err := r.Add("relay", noop); err == nil {
		t.Fatal("second Add with the same name succeeded")
	}
}

func TestRunWaitsForShutdownEvenWithNoConsumers(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	done := runAsync(ctx, worker.NewRegistry(), time.Second)

	select {
	case err := <-done:
		t.Fatalf("Run returned %v before shutdown was requested", err)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
}
