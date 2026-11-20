package authz_test

import (
	"github.com/iykeevans/konduite/backend/internal/authz"
)

var _ authz.Directory = authz.StoreMemberships(nil)
