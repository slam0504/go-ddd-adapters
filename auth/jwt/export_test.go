package authjwt

import "time"

// SetNow overrides the Verifier's clock for deterministic exp/nbf/leeway tests.
// It is exported only to the external test package via this _test.go file and
// is not part of the public API.
func (v *Verifier) SetNow(now func() time.Time) {
	v.now = now
}
