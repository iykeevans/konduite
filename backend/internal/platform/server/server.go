// Package server runs an HTTP server until its context is cancelled, then
// shuts it down gracefully.
package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

// Serve serves srv on ln until ctx is cancelled, then stops accepting new
// connections and waits up to timeout for in-flight requests to finish.
func Serve(ctx context.Context, srv *http.Server, ln net.Listener, timeout time.Duration) error {
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
