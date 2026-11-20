// Package admin gates platform-operator endpoints, such as the DLQ, that
// see across every workspace and so sit outside authz.Can's workspace roles.
package admin

import (
	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
)

// Allowlist is the set of users allowed on admin endpoints. The zero value
// and an empty list deny everyone.
type Allowlist struct {
	ids map[uuid.UUID]struct{}
}

// NewAllowlist returns a list allowing exactly ids
// (config PLATFORM_ADMIN_USER_IDS).
func NewAllowlist(ids []uuid.UUID) Allowlist {
	a := Allowlist{ids: make(map[uuid.UUID]struct{}, len(ids))}
	for _, id := range ids {
		a.ids[id] = struct{}{}
	}
	return a
}

// Check returns nil for a listed user and errs.NotFound for anyone else, so
// a non-admin can't tell admin endpoints exist.
func (a Allowlist) Check(p auth.Principal) error {
	if p.Kind == auth.KindUser {
		if _, ok := a.ids[p.ID]; ok {
			return nil
		}
	}
	return errs.NotFound("not found")
}
