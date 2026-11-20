package main

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/authz"
)

func TestAuthorizerWiringSupportsProjectChecks(t *testing.T) {
	pool, err := pgxpool.New(t.Context(), "postgres://konduite@127.0.0.1:1/konduite?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	err = newAuthorizer(pool).Can(t.Context(), auth.Principal{Kind: auth.KindUser, ID: uuid.New()},
		authz.ActionViewProject, authz.Project(uuid.New()))

	if errors.Is(err, authz.ErrNoProjectLookup) {
		t.Fatalf("err = %v: the API's authorizer cannot look up projects, so every project check would be a 500", err)
	}
	if err == nil {
		t.Fatal("err = nil, want the lookup against an unreachable database to fail")
	}
}
