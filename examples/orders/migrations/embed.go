// Package migrations exposes the examples/orders SQL migration files as an
// embed.FS for use with golang-migrate's iofs source driver in integration
// tests. In docker-compose the same files are mounted into the
// migrate/migrate official image.
package migrations

import "embed"

// FS holds the orders migration .sql files in version order.
//
//go:embed *.sql
var FS embed.FS
