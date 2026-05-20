// Package migrations exposes the pgxoutbox SQL migration files as an
// embed.FS for use with golang-migrate's iofs source driver in tests and
// example wiring.
//
// In production deployments callers should run these SQL files via their
// own migration tooling (golang-migrate, goose, atlas, flyway, ...) —
// the adapter does not ship a runtime Migrate API. See the package
// doc.go in eventbus/outbox/pgx for guidance.
package migrations

import "embed"

// FS holds the migration .sql files in version order.
//
//go:embed *.sql
var FS embed.FS
