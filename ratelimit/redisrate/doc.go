// Package redisratelimit is a distributed, Redis-backed implementation of core's
// ports/ratelimit.Limiter, wrapping github.com/go-redis/redis_rate/v10 (the
// Generic Cell Rate Algorithm, GCRA).
//
// # Why distributed
//
// Inbound throttling on a horizontally-scaled service needs all instances to
// share one quota. An in-process limiter would let each instance count
// independently (effective quota = N × the configured rate). This adapter keeps
// the counter in Redis, so the quota holds across instances. It matches the
// repo's other Redis-backed adapters (idempotency/redis, jobs/asynq).
//
// # Construction
//
//	l, err := redisratelimit.New(client, redis_rate.Limit{Rate: 100, Burst: 100, Period: time.Minute})
//
// client may be *redis.Client, *redis.ClusterClient, or *redis.Ring (any
// redis.UniversalClient — redis_rate's internal rediser needs Del, which
// redis.Scripter lacks). The limit is positional and required: there is no
// universal safe default, so a missing limit fails loud at New rather than
// silently throttling at some arbitrary rate. WithKeyPrefix overrides the key
// namespace (default "ratelimit"); two Limiters with different prefixes never
// share a bucket for the same key.
//
// # Result projection (accurate-or-absent)
//
// Allow returns the decision as data (Result.Allowed) — ordinary quota
// exhaustion is Result{Allowed:false}, nil, never an error and never
// errorsx.CodeRateLimited. The advisory metadata projects redis_rate's GCRA
// state: Limit is the instantaneous burst (Burst), Remaining is the
// instantaneous capacity (<= Burst). These describe the GCRA burst the key may
// consume right now, NOT a fixed-window "X requests per window" quota; an HTTP
// middleware whose header convention cannot faithfully express that projection
// SHOULD omit the headers rather than fabricate a window number. RetryAfter is 0
// when allowed (redis_rate returns -1 there; this adapter maps it to the
// contract-required 0) and the backend wait hint when denied. ResetAt is absent:
// projecting an absolute reset instant would require blending the client clock
// with a Redis-side duration (skew risk); a WithClock option can add it when a
// middleware consumer needs the header.
//
// # Errors
//
// A non-nil error means Allow could not reach a decision, in fixed precedence:
// an empty key is a missing partition key (errorsx.CodeInvalidArgument, no
// backend contact); a ctx already cancelled or past its deadline returns the
// matching ctx error verbatim (no backend contact); a backend failure is a coded
// errorsx whose CodeOf is never CodeUnknown (unreachable → CodeUnavailable;
// unclassifiable → CodeInternal).
//
// # Redis Cluster / Ring
//
// Every redis_rate operation is single-key, so it stays within one hash slot by
// construction — *redis.ClusterClient / *redis.Ring SHOULD work with no hash
// tag. This is an API-derived claim; CI exercises single-node redis:7-alpine.
package redisratelimit
