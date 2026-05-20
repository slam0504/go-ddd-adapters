package pgxoutbox

import "time"

// minClaimLease is the lower bound enforced by WithClaimLease. Below
// this, lease-driven crash recovery competes with normal Publisher
// latency variance and produces unhelpful duplicate-publish noise.
const minClaimLease = 100 * time.Millisecond

// defaultClaimLease is the lease window applied when WithClaimLease is
// not supplied. Five seconds targets the common case where
// Publisher.Publish completes well under one second; operators with
// slower brokers should bump this so the lease comfortably exceeds
// their P99 publish latency.
const defaultClaimLease = 5 * time.Second

// Option configures a Store at construction.
type Option func(*Store)

// WithClaimLease overrides the lease window. Each Fetch stamps
// claimed_until = now() + lease on every claimed row, and other
// pollers skip rows whose lease has not yet expired. Values below
// minClaimLease (100ms) are silently clamped up.
//
// The lease is the at-least-once duplicate-publish window: if a
// Publisher.Publish call takes longer than the lease to return, a
// second poller can re-claim and re-publish the same row. Set the
// lease to comfortably exceed your Publisher.Publish P99 to minimise
// duplicates. Consumers must dedup via eventbus/inbox (or equivalent)
// regardless of lease tuning — at-least-once is the contract, not an
// edge case.
func WithClaimLease(d time.Duration) Option {
	return func(s *Store) {
		if d < minClaimLease {
			d = minClaimLease
		}
		s.claimLease = d
	}
}
