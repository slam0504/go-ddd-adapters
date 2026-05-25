// Package health exposes a Registry that aggregates
// go-ddd-core ports/health.Check probes into liveness and readiness
// HTTP handlers for the transport/http/stdlib adapter (or any other
// net/http-compatible mount point).
//
// # Endpoint semantics
//
//	/healthz : process liveness, always 200 OK.
//	           DOES NOT run any registered Check; a failing
//	           dependency must not cause an orchestrator to restart
//	           the pod.
//
//	/readyz  : readiness, runs every registered Check sequentially
//	           in registration order, each under SetProbeTimeout
//	           (default 2s per Check). 200 when every Check returns
//	           nil; 503 when any Check fails. The body always lists
//	           every Check, including passing ones on the 503 path,
//	           so operators see partial-failure state.
//
// Only GET is supported on both endpoints; other methods return 405.
//
// # Body shapes
//
// Liveness body (always 200):
//
//	{"status":"ok"}
//
// Readiness body (200, all healthy):
//
//	{"status":"ok","checks":[{"name":"postgres","ok":true}, ...]}
//
// Readiness body (503, any failing):
//
//	{"status":"unhealthy","checks":[
//	  {"name":"postgres","ok":true},
//	  {"name":"kafka","ok":false,"error":"connection refused"}
//	]}
//
// Empty registry yields 200 with an empty checks array (not null):
//
//	{"status":"ok","checks":[]}
//
// # Routing
//
// Registry.Handler() routes the EXACT paths /healthz and /readyz via
// a method-aware net/http.ServeMux. To mount under a prefix, wrap
// with http.StripPrefix:
//
//	mounted := http.StripPrefix("/admin", reg.Handler())
//	// /admin/healthz and /admin/readyz now route correctly.
//
// Suffix routing is intentionally not provided.
//
// # Typical usage
//
// The zero value of Registry is ready to use; Register / MustRegister
// are safe for concurrent calls during startup.
//
//	reg := &health.Registry{}
//	reg.MustRegister(corehealth.NewCheck("postgres", pool.Ping))
//	reg.MustRegister(corehealth.NewCheck("kafka", kafkaProbe))
//
//	// Combined on the application server:
//	app.Use(httpstdlib.Module(":8080", reg.Handler()))
//
//	// Or split: liveness on the app server, readiness on the admin:
//	app.Use(
//	    httpstdlib.Module(":8080", reg.LivenessHandler()),
//	    httpstdlib.Module(":9090", reg.ReadinessHandler(),
//	        httpstdlib.WithModuleName("admin")),
//	)
package health
