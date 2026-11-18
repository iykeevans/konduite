// Package worker runs the worker process's named consumers until shutdown.
package worker

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"
)

// Consumer processes work until ctx is cancelled. It should finish its
// in-flight job and then return.
type Consumer func(ctx context.Context) error

// Registry holds the consumers a worker process runs.
type Registry struct {
	names     []string
	consumers map[string]Consumer
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{consumers: map[string]Consumer{}}
}

// Add registers a consumer under a unique name.
func (r *Registry) Add(name string, c Consumer) error {
	if _, dup := r.consumers[name]; dup {
		return fmt.Errorf("worker: consumer %q already registered", name)
	}
	r.names = append(r.names, name)
	r.consumers[name] = c
	return nil
}

// Run starts every consumer and blocks until ctx is cancelled or a
// consumer fails, then waits for the rest to stop.
//
// Cancelling ctx asks consumers to finish their in-flight work. If one
// consumer fails, the others are cancelled the same way and Run returns
// that failure, so the process exits and gets restarted. A consumer still
// running drain after cancellation is abandoned and named in the error.
func (r *Registry) Run(ctx context.Context, drain time.Duration) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		mu      sync.Mutex
		running = map[string]bool{}
		failure error
		failed  = make(chan struct{})
		wg      sync.WaitGroup
	)
	for _, name := range r.names {
		running[name] = true
	}
	for _, name := range r.names {
		c := r.consumers[name]
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := safeRun(runCtx, c)
			mu.Lock()
			defer mu.Unlock()
			delete(running, name)
			if err != nil && !errors.Is(err, context.Canceled) && failure == nil {
				failure = fmt.Errorf("worker: consumer %s: %w", name, err)
				close(failed)
			}
		}()
	}

	allDone := make(chan struct{})
	go func() { wg.Wait(); close(allDone) }()

	// Consumers that return nil early (or none at all) don't stop the
	// process; only shutdown or a failure does.
	select {
	case <-ctx.Done():
	case <-failed:
	}
	cancel()

	select {
	case <-allDone:
	case <-time.After(drain):
		mu.Lock()
		defer mu.Unlock()
		stuck := make([]string, 0, len(running))
		for name := range running {
			stuck = append(stuck, name)
		}
		sort.Strings(stuck)
		return errors.Join(failure, fmt.Errorf("worker: drain timeout %s exceeded, still running: %s",
			drain, strings.Join(stuck, ", ")))
	}

	mu.Lock()
	defer mu.Unlock()
	return failure
}

func safeRun(ctx context.Context, c Consumer) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v\n%s", p, debug.Stack())
		}
	}()
	return c(ctx)
}
