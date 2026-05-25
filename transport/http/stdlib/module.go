package httpstdlib

import (
	"net/http"

	"github.com/slam0504/go-ddd-core/bootstrap"
)

// Module is a convenience wrapper equivalent to
// New(addr, handler, opts...).Module() for callers that do not need
// access to the resolved listener address via Server.Addr.
func Module(addr string, handler http.Handler, opts ...Option) bootstrap.ModuleFunc {
	return New(addr, handler, opts...).Module()
}
