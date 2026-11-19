package runtimes

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
)

// InterimMembershipCheck stands in for authz.Can(ActionRegisterRuntime)
// until SEC-001 lands, with the same answers: owners, admins and developers
// may register; other members get Forbidden; non-members and unknown
// workspaces get the same NotFound so ids can't be probed. Delete this file
// when main.go switches to *authz.Authorizer.
func InterimMembershipCheck(pool *pgxpool.Pool) AuthorizeRegister {
	return func(ctx context.Context, p auth.Principal, workspaceID uuid.UUID) error {
		if p.Kind != auth.KindUser {
			return errs.NotFound("workspace not found")
		}
		var role string
		err := pool.QueryRow(ctx,
			`SELECT role FROM workspace_members WHERE workspace_id = $1 AND user_id = $2`, workspaceID, p.ID).Scan(&role)
		if errors.Is(err, pgx.ErrNoRows) {
			return errs.NotFound("workspace not found")
		}
		if err != nil {
			return err
		}
		switch role {
		case "owner", "admin", "developer":
			return nil
		}
		return errs.Forbidden("your role cannot register runtimes in this workspace")
	}
}
