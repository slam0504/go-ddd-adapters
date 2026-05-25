package health

import (
	"errors"
	"fmt"
	"sync"
	"time"

	corehealth "github.com/slam0504/go-ddd-core/ports/health"
)

const defaultProbeTimeout = 2 * time.Second

// ErrEmptyCheckName is returned by Register when the provided Check
// has an empty Name(). Empty names would render the readiness body
// ambiguous for operators.
var ErrEmptyCheckName = errors.New("health: check name is empty")

// Registry aggregates named ports/health.Check probes for the
// transport/http/stdlib adapter's /healthz and /readyz handlers.
//
// The zero value is ready to use. Register / MustRegister are safe
// for concurrent calls during startup, but adding Checks after
// handlers are serving requests is not recommended (it complicates
// reasoning about per-request snapshots).
type Registry struct {
	mu           sync.RWMutex
	checks       []corehealth.Check
	names        map[string]struct{}
	probeTimeout time.Duration
}

// Register adds c to the registry. Names must be non-empty and
// unique; violations return an error so wiring fails loud at startup.
// A failed Register does not mutate the registry.
func (r *Registry) Register(c corehealth.Check) error {
	name := c.Name()
	if name == "" {
		return ErrEmptyCheckName
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.names[name]; dup {
		return fmt.Errorf("health: duplicate check name %q", name)
	}
	if r.names == nil {
		r.names = make(map[string]struct{})
	}
	r.names[name] = struct{}{}
	r.checks = append(r.checks, c)
	return nil
}

// MustRegister panics on Register error. Convenience for startup
// wiring where a duplicate or empty name is a programming error.
func (r *Registry) MustRegister(c corehealth.Check) {
	if err := r.Register(c); err != nil {
		panic(err)
	}
}

// SetProbeTimeout sets the per-Check ctx timeout used during
// readiness probes. A zero or negative value disables the timeout.
// Default 2s.
func (r *Registry) SetProbeTimeout(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.probeTimeout = d
}

// effectiveProbeTimeout returns the configured timeout or the default.
func (r *Registry) effectiveProbeTimeout() time.Duration {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.probeTimeout == 0 {
		return defaultProbeTimeout
	}
	return r.probeTimeout
}

// snapshot returns a copy of the registered Checks in registration
// order, so handlers can run them without holding the registry lock
// during I/O.
func (r *Registry) snapshot() []corehealth.Check {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]corehealth.Check, len(r.checks))
	copy(out, r.checks)
	return out
}
