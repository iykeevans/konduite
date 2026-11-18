package server_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/iykeevans/konduite/backend/internal/platform/server"
)

func TestServeFinishesInFlightRequestOnShutdown(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	srv := &http.Server{
		ReadHeaderTimeout: time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			close(started)
			<-release
			_, _ = io.WriteString(w, "done")
		}),
	}
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- server.Serve(ctx, srv, ln, 5*time.Second) }()

	type result struct {
		status int
		body   string
		err    error
	}
	got := make(chan result, 1)
	go func() {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+ln.Addr().String(), nil)
		if err != nil {
			got <- result{err: err}
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			got <- result{err: err}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		got <- result{status: resp.StatusCode, body: string(b)}
	}()

	<-started
	cancel()

	select {
	case err := <-served:
		t.Fatalf("Serve returned %v while a request was still in flight", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	if r := <-got; r.err != nil || r.status != http.StatusOK || r.body != "done" {
		t.Fatalf("in-flight request = %+v, want 200 done", r)
	}
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("Serve = %v, want nil after clean shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after the in-flight request finished")
	}
}
