package pgxdb

import "errors"

// ErrNoTx is returned by adapter code that requires a transaction in
// the context but finds none. Stage() in eventbus/outbox/pgx re-exports
// this for caller convenience.
var ErrNoTx = errors.New("ports/database/pgx: no transaction in context")
