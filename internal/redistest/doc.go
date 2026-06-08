// Package redistest is an internal-only helper that boots a Redis
// testcontainer and returns a ready-to-use *redis.Client. It exists so the
// testcontainers boot ceremony for the idempotency/redis integration tests
// lives in one place.
//
// All exported symbols are guarded by //go:build integration: they are
// invisible to non-integration builds and do not pull the testcontainers
// dependency tree into binaries.
//
// The helper performs no keyspace setup — callers own their own key namespace
// (e.g. a per-test WithKeyPrefix) after StartContainer returns.
package redistest
