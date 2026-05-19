// Package outbox provides in-process implementations of the
// github.com/slam0504/go-ddd-core/eventbus.Outbox / eventbus.OutboxStore
// / eventbus.Relay contracts, plus a driver-agnostic polling Relay that
// drains stored records into an eventbus.Publisher.
//
// # NOT a transactional outbox
//
// Core's eventbus.Outbox docstring says implementations "must
// participate in the caller's transaction (typically by reading a tx
// handle from ctx)." The [Memory] type in this package CANNOT do that.
// Memory only provides in-process mutex safety inside its own store —
// it does not enroll in any database transaction. If aggregate
// persistence succeeds but Stage is not called, or the process crashes
// between domain Save and Stage, the corresponding event is lost.
//
// Use this package for tests, single-instance examples, local
// development, and exercising Relay / backoff / DLQ behaviour. Do NOT
// use it for production multi-instance services that require
// at-least-once delivery in the presence of crashes. The forthcoming
// SQL/pgx adapter (sibling sub-package) will deliver a real
// transactional Outbox by writing the staging row in the same DB
// transaction that persists the aggregate.
//
// # Layout
//
// The package is flat — same convention as eventbus/inbox.
// Out-of-process implementations (SQL, Redis, ...) belong in sibling
// sub-packages under eventbus/outbox/ (e.g. eventbus/outbox/pgx) so
// each tech-cluster's transitive dependencies stay isolated.
package outbox
