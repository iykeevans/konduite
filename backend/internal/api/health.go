package api

import (
	"context"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/iykeevans/konduite/backend/internal/platform/httpx"
)

// Check reports whether a dependency is ready. Its error text is never
// returned to callers.
type Check func(ctx context.Context) error

const readinessTimeout = 2 * time.Second

func healthz(w http.ResponseWriter, _ *http.Request) {
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func readyz(checks map[string]Check) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
		defer cancel()

		var (
			mu      sync.Mutex
			wg      sync.WaitGroup
			failing = []string{}
		)
		for name, check := range checks {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := check(ctx); err != nil {
					mu.Lock()
					failing = append(failing, name)
					mu.Unlock()
				}
			}()
		}
		wg.Wait()

		if len(failing) > 0 {
			sort.Strings(failing)
			httpx.Fail(w, http.StatusServiceUnavailable, "unavailable", "service is not ready",
				map[string][]string{"failing": failing})
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
	}
}
