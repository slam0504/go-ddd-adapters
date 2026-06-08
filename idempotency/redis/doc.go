// Package redisidempotency adapts Redis to the go-ddd-core
// ports/idempotency.Store contract: it guards a non-idempotent inbound request
// (typically an HTTP POST) against duplicate execution when a client retries
// with the same idempotency key. It is distinct from an event-Inbox: this
// guards inbound-request retries, not broker-delivered domain events.
//
// # Storage model
//
// Each (scope, key) is one Redis hash at a composite key:
//
//	keyPrefix + ":" + len(scope) + ":" + scope + key
//
// The length-prefix on scope makes ("tenant:op","k") and ("tenant","op:k")
// distinct keys, so a flattened tuple can never collide. Hash fields are kept
// short to keep the payload small:
//
//	fp    request fingerprint
//	st    status: "P" in-progress, "C" completed
//	tok   lease token (present only while in-progress; deleted on completion)
//	resp  completed response bytes (present only when st == "C")
//
// An in-progress record carries PEXPIRE leaseTTL; a completed record carries
// PEXPIRE retention.
//
// # Error mapping
//
// Begin returns a Reservation whose Status is one of New / InProgress /
// Completed / Mismatch; a non-nil error means the state could not be
// determined. Finish and Cancel follow the contract's three-way error scheme:
//
//	nil                   applied / released, for certain.
//	errorsx.CodeConflict  NOT applied, for certain (stale/expired/forged token,
//	                      vanished record, or already-completed) → 409.
//	any other coded error INDETERMINATE: a transport/timeout/ctx failure where
//	                      the write may or may not have landed. Recover by
//	                      calling Begin again to read the authoritative state.
//
// Malformed input (empty scope/key/fingerprint to Begin; empty
// Scope/Key/LeaseToken to Finish/Cancel) returns errorsx.CodeInvalidArgument
// before any Redis command is issued.
//
// # Atomicity and concurrency
//
// Each of Begin/Finish/Cancel executes a single Lua script via EVAL. Redis is
// single-threaded, so a script runs to completion without interleaving — this,
// not a Go mutex, is what makes the reserve-or-report decision atomic: two
// concurrent Begin calls for the same (scope, key) can never both observe
// StatusNew. The lease token is minted in Go with crypto/rand and passed into
// the script as an argument; the scripts never generate randomness, because Lua
// scripts must be deterministic for replication. A Store is immutable after New
// and safe for concurrent use.
//
// # Reclaim, liveness, and retention
//
// The in-progress PEXPIRE leaseTTL IS the reclaim/liveness mechanism: an
// unfinished reservation auto-expires, so the same (scope, key) can be reserved
// again with a fresh distinct token. The contract guarantees key liveness only,
// NOT exactly-once side effects — the handler or a downstream system must also
// be idempotent. Completed-record retention is adapter policy (the contract
// leaves it to the adapter): a completed record expires retention after Finish
// and is NOT slid forward on replay, so the window is measured from completion,
// not from the last read.
//
// The lease TTL and retention must each be >= 1ms. Both feed Redis PEXPIRE,
// which takes milliseconds, so a sub-millisecond duration would truncate to 0
// and PEXPIRE 0 would fail at runtime; New rejects a sub-ms value
// (ErrLeaseTTLTooSmall / ErrRetentionTooSmall) at construction rather than
// silently rounding the caller's stated duration up to 1ms.
//
// # Client types and the Redis Cluster boundary
//
// New accepts any redis.Scripter (*redis.Client, *redis.ClusterClient,
// *redis.Ring, or a test fake). It rejects a nil interface and a typed-nil of
// the three documented concrete go-redis types; a typed-nil custom Scripter is
// the caller's responsibility and surfaces on first command.
//
// Every script touches exactly one key (KEYS[1]). On a clustered backend a
// single EVAL may only touch keys in one hash slot, and a one-key script
// satisfies that by construction — so *redis.ClusterClient / *redis.Ring SHOULD
// work with no hash tag needed. This is derived from the single-key design and
// the go-redis Scripter API; it is NOT exercised by a live multi-node test (CI
// runs single-node redis:7-alpine only). The invariant to preserve: keep every
// script single-key. If a future change makes one script touch multiple keys,
// those keys MUST share a {…} hash tag to land in the same slot.
package redisidempotency
