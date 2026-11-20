package admin_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/admin"
	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
)

func TestAllowlistCheck(t *testing.T) {
	adminID, otherID := uuid.New(), uuid.New()
	tests := []struct {
		name  string
		list  admin.Allowlist
		p     auth.Principal
		allow bool
	}{
		{"listed user", admin.NewAllowlist([]uuid.UUID{adminID}), auth.Principal{Kind: auth.KindUser, ID: adminID}, true},
		{"unlisted user", admin.NewAllowlist([]uuid.UUID{adminID}), auth.Principal{Kind: auth.KindUser, ID: otherID}, false},
		{"empty list denies everyone", admin.NewAllowlist(nil), auth.Principal{Kind: auth.KindUser, ID: adminID}, false},
		{"zero value denies everyone", admin.Allowlist{}, auth.Principal{Kind: auth.KindUser, ID: adminID}, false},
		{"listed id with another principal kind", admin.NewAllowlist([]uuid.UUID{adminID}), auth.Principal{Kind: "runtime", ID: adminID}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.list.Check(tt.p)
			if tt.allow {
				if err != nil {
					t.Errorf("Check = %v, want allowed", err)
				}
				return
			}
			if e, ok := errs.As(err); !ok || e.Kind != errs.KindNotFound {
				t.Errorf("Check = %v, want errs.NotFound so admin routes stay hidden", err)
			}
		})
	}
}
