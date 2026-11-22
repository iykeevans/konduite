package projectmembers_test

import (
	"testing"

	"github.com/iykeevans/konduite/backend/internal/authz"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
	"github.com/iykeevans/konduite/backend/internal/projectmembers"
)

const removed authz.ProjectRole = ""

func TestCheckMaintainerChange(t *testing.T) {
	const (
		maintainer  = authz.ProjectRoleMaintainer
		contributor = authz.ProjectRoleContributor
		viewer      = authz.ProjectRoleViewer
	)
	tests := []struct {
		name         string
		implicit     bool
		target, next authz.ProjectRole
		maintainers  int
		want         errs.Kind
	}{
		{"demote the last maintainer", false, maintainer, contributor, 1, errs.KindConflict},
		{"remove the last maintainer", false, maintainer, removed, 1, errs.KindConflict},
		{"demote one of two maintainers", false, maintainer, viewer, 2, ""},
		{"remove one of two maintainers", false, maintainer, removed, 2, ""},
		{"owner or admin demotes the last maintainer", true, maintainer, viewer, 1, ""},
		{"owner or admin removes the last maintainer", true, maintainer, removed, 1, ""},
		{"keep the last maintainer a maintainer", false, maintainer, maintainer, 1, ""},
		{"promote a contributor", false, contributor, maintainer, 1, ""},
		{"demote a contributor", false, contributor, viewer, 1, ""},
		{"remove a viewer", false, viewer, removed, 1, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := projectmembers.CheckMaintainerChange(tt.implicit, tt.target, tt.next, tt.maintainers)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				return
			}
			e, ok := errs.As(err)
			if !ok || e.Kind != tt.want {
				t.Fatalf("err = %v, want %s", err, tt.want)
			}
		})
	}
}
