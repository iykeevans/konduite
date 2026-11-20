package members_test

import (
	"testing"

	"github.com/iykeevans/konduite/backend/internal/authz"
	"github.com/iykeevans/konduite/backend/internal/members"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
)

const (
	owner = authz.RoleOwner
	admin = authz.RoleAdmin
	dev   = authz.RoleDeveloper
	pm    = authz.RoleProjectManager
)

func kindOf(err error) errs.Kind {
	if err == nil {
		return ""
	}
	if e, ok := errs.As(err); ok {
		return e.Kind
	}
	return "plain error"
}

func TestCheckRoleChange(t *testing.T) {
	tests := []struct {
		name                string
		actor, target, next authz.Role
		owners              int
		want                errs.Kind
	}{
		{"owner promotes developer to admin", owner, dev, admin, 1, ""},
		{"owner promotes admin to owner", owner, admin, owner, 1, ""},
		{"owner demotes another owner", owner, owner, admin, 2, ""},
		{"owner demotes the last owner", owner, owner, admin, 1, errs.KindConflict},
		{"admin changes developer to project manager", admin, dev, pm, 1, ""},
		{"admin promotes another member to admin", admin, dev, admin, 1, ""},
		{"admin demotes an owner", admin, owner, admin, 2, errs.KindForbidden},
		{"admin promotes someone to owner", admin, dev, owner, 1, errs.KindForbidden},
		{"admin promotes themselves to owner", admin, admin, owner, 1, errs.KindForbidden},
		{"developer changes a role", dev, pm, dev, 1, errs.KindForbidden},
		{"project manager changes a role", pm, dev, admin, 1, errs.KindForbidden},
		{"same role is a no-op", owner, owner, owner, 1, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := kindOf(members.CheckRoleChange(tt.actor, tt.target, tt.next, tt.owners))
			if got != tt.want {
				t.Errorf("CheckRoleChange(%s, %s -> %s, owners=%d) = %q, want %q",
					tt.actor, tt.target, tt.next, tt.owners, got, tt.want)
			}
		})
	}
}

func TestCheckRemoval(t *testing.T) {
	tests := []struct {
		name          string
		actor, target authz.Role
		owners        int
		self          bool
		want          errs.Kind
	}{
		{"owner removes developer", owner, dev, 1, false, ""},
		{"owner removes another owner", owner, owner, 2, false, ""},
		{"admin removes developer", admin, dev, 1, false, ""},
		{"admin removes another admin", admin, admin, 1, false, ""},
		{"admin removes an owner", admin, owner, 2, false, errs.KindForbidden},
		{"developer removes someone", dev, pm, 1, false, errs.KindForbidden},
		{"developer leaves", dev, dev, 1, true, ""},
		{"project manager leaves", pm, pm, 1, true, ""},
		{"owner leaves with another owner left", owner, owner, 2, true, ""},
		{"last owner leaves", owner, owner, 1, true, errs.KindConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := kindOf(members.CheckRemoval(tt.actor, tt.target, tt.owners, tt.self))
			if got != tt.want {
				t.Errorf("CheckRemoval(%s removes %s, owners=%d, self=%v) = %q, want %q",
					tt.actor, tt.target, tt.owners, tt.self, got, tt.want)
			}
		})
	}
}
