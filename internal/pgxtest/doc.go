// Package pgxtest is an internal-only helper that boots a Postgres
// testcontainer and returns a ready-to-use *pgxpool.Pool. The package is
// shared across the module's integration tests (ports/database/pgx,
// eventbus/outbox/pgx) so the testcontainers boot ceremony lives in one
// place.
//
// All exported symbols are guarded by //go:build integration: they are
// invisible to non-integration builds and do not pull the testcontainers
// dependency tree into binaries.
//
// The helper does not run any migrations or DDL — callers supply their
// own schema after StartContainer returns. Migration knowledge stays with
// the package under test.
package pgxtest
