package health

import (
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

type livenessBody struct {
	Status string `json:"status"`
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
