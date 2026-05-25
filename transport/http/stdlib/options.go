// Package httpstdlib is a thin bootstrap-aware adapter around net/http
// for inbound HTTP transport. See doc.go for an overview.
package httpstdlib

import (
	"context"
	"net"
	"time"

	"github.com/slam0504/go-ddd-core/ports/logger"
)

const (
	defaultReadHeaderTimeout = 5 * time.Second
	defaultShutdownTimeout   = 15 * time.Second
)

// Option configures a Server constructed via New.
type Option func(*config)

type config struct {
	readHeaderTimeout time.Duration
	shutdownTimeout   time.Duration
	logger            logger.Logger
	moduleName        string
	baseContext       func(net.Listener) context.Context
}

func newConfig(addr string) *config {
	return &config{
		readHeaderTimeout: defaultReadHeaderTimeout,
		shutdownTimeout:   defaultShutdownTimeout,
		logger:            nopLogger{},
		moduleName:        "http:" + addr,
	}
}

// WithReadHeaderTimeout overrides the default 5s read-header timeout.
func WithReadHeaderTimeout(d time.Duration) Option {
	return func(c *config) { c.readHeaderTimeout = d }
}

// WithShutdownTimeout overrides the default 15s shutdown timeout.
func WithShutdownTimeout(d time.Duration) Option {
	return func(c *config) { c.shutdownTimeout = d }
}

// WithLogger replaces the no-op logger with l. Used for start /
// serve-error / shutdown events. A nil logger is ignored so the
// default no-op stays in place.
func WithLogger(l logger.Logger) Option {
	return func(c *config) {
		if l != nil {
			c.logger = l
		}
	}
}

// WithModuleName overrides the default "http:<addr>" module name.
// An empty name is ignored.
func WithModuleName(name string) Option {
	return func(c *config) {
		if name != "" {
			c.moduleName = name
		}
	}
}

// WithBaseContext sets http.Server.BaseContext. Default is nil
// (stdlib uses context.Background per-connection).
func WithBaseContext(fn func(net.Listener) context.Context) Option {
	return func(c *config) { c.baseContext = fn }
}

// nopLogger is the zero-config default. It satisfies logger.Logger
// without doing any work, so the adapter has a usable logger surface
// even when WithLogger is not supplied.
type nopLogger struct{}

func (nopLogger) Log(context.Context, logger.Level, string, ...logger.Attr) {}
func (nopLogger) With(...logger.Attr) logger.Logger                         { return nopLogger{} }
func (nopLogger) Enabled(context.Context, logger.Level) bool                { return false }
