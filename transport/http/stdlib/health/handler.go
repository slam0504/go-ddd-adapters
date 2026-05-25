package health

import (
	"context"
	"encoding/json"
	"net/http"
)

// LivenessHandler returns an http.Handler that serves only liveness
// semantics: it always responds 200 OK with body {"status":"ok"} and
// DOES NOT invoke any registered Check. Liveness signals "process is
// alive and can serve HTTP" — a failing dependency must not cause an
// orchestrator to restart the pod.
//
// Method routing (405 for non-GET) is provided by Handler(); calling
// LivenessHandler directly accepts any method.
func (r *Registry) LivenessHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, livenessBody{Status: "ok"})
	})
}

// ReadinessHandler returns an http.Handler that runs every registered
// Check sequentially in registration order, each under the configured
// ProbeTimeout. Returns 200 with status:"ok" when every Check returns
// nil, 503 with status:"unhealthy" when any Check fails. The body
// always lists every Check (passing ones included on the 503 path) so
// operators see partial-failure state.
//
// Method routing (405 for non-GET) is provided by Handler(); calling
// ReadinessHandler directly accepts any method.
func (r *Registry) ReadinessHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		checks := r.snapshot()
		timeout := r.effectiveProbeTimeout()

		results := make([]probeResult, 0, len(checks))
		allOK := true
		for _, c := range checks {
			ctx := req.Context()
			var cancel context.CancelFunc
			if timeout > 0 {
				ctx, cancel = context.WithTimeout(ctx, timeout)
			}
			err := c.Check(ctx)
			if cancel != nil {
				cancel()
			}
			res := probeResult{Name: c.Name(), OK: err == nil}
			if err != nil {
				res.Error = err.Error()
				allOK = false
			}
			results = append(results, res)
		}

		status := http.StatusOK
		summary := "ok"
		if !allOK {
			status = http.StatusServiceUnavailable
			summary = "unhealthy"
		}
		writeJSON(w, status, readinessBody{Status: summary, Checks: results})
	})
}

// Handler returns an http.Handler that serves both /healthz and
// /readyz on a method-aware net/http.ServeMux. The handler matches
// the EXACT paths /healthz and /readyz — non-GET requests on those
// paths return 405, and any other path returns 404 (both behaviours
// come straight from net/http.ServeMux semantics).
//
// To mount under a prefix (e.g. behind an admin path), wrap with
// http.StripPrefix:
//
//	mounted := http.StripPrefix("/admin", reg.Handler())
//	// /admin/healthz and /admin/readyz now route correctly.
//
// Suffix routing is intentionally not provided — callers that need
// prefix mounting use StripPrefix, the standard stdlib idiom.
func (r *Registry) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /healthz", r.LivenessHandler())
	mux.Handle("GET /readyz", r.ReadinessHandler())
	return mux
}

type livenessBody struct {
	Status string `json:"status"`
}

// readinessBody intentionally does NOT use omitempty on Checks so an
// empty registry serializes as "checks":[] rather than dropping the
// field. probeResult.Error uses omitempty so passing checks omit it.
type readinessBody struct {
	Status string        `json:"status"`
	Checks []probeResult `json:"checks"`
}

type probeResult struct {
	Name  string `json:"name"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
