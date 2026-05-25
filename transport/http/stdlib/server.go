package httpstdlib

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"

	"github.com/slam0504/go-ddd-core/bootstrap"
	"github.com/slam0504/go-ddd-core/ports/logger"
)

// Server wraps an *http.Server with a pre-bound listener so the actual
// listening address (post net.Listen) is exposed via Addr.
//
// New(addr, handler, opts...) is the primary constructor. The returned
// Server's Module() method produces the bootstrap.ModuleFunc consumed
// by bootstrap.App.Use. Module's Start performs net.Listen("tcp", addr)
// synchronously — so bind failures surface as a startup error — and
// only launches Serve in a goroutine on success.
type Server struct {
	configuredAddr string
	handler        http.Handler
	cfg            *config

	mu       sync.Mutex
	listener net.Listener
	server   *http.Server
}

// New constructs a Server. The listener is created during Start
// (not at New time) so bind failures surface via the bootstrap
// lifecycle, not the constructor.
func New(addr string, handler http.Handler, opts ...Option) *Server {
	cfg := newConfig(addr)
	for _, opt := range opts {
		opt(cfg)
	}
	return &Server{
		configuredAddr: addr,
		handler:        handler,
		cfg:            cfg,
	}
}

// Addr returns the actual listening address (host:port resolved by the
// OS). Valid only after Module().Start has returned without error;
// returns "" before Start or after a failed Start.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

// Module adapts the Server into a bootstrap.Module.
func (s *Server) Module() bootstrap.ModuleFunc {
	return bootstrap.ModuleFunc{
		ModuleName: s.cfg.moduleName,
		StartFn:    s.start,
		StopFn:     s.stop,
	}
}

func (s *Server) start(ctx context.Context, _ *bootstrap.App) error {
	ln, err := net.Listen("tcp", s.configuredAddr)
	if err != nil {
		return err
	}

	srv := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: s.cfg.readHeaderTimeout,
		BaseContext:       s.cfg.baseContext,
	}

	s.mu.Lock()
	s.listener = ln
	s.server = srv
	s.mu.Unlock()

	s.cfg.logger.Log(ctx, logger.LevelInfo, "http listening",
		logger.F("addr", ln.Addr().String()),
		logger.F("module", s.cfg.moduleName))

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.cfg.logger.Log(ctx, logger.LevelError, "http serve error",
				logger.F("err", err.Error()),
				logger.F("module", s.cfg.moduleName))
		}
	}()

	return nil
}

func (s *Server) stop(ctx context.Context) error {
	s.mu.Lock()
	srv := s.server
	s.mu.Unlock()
	if srv == nil {
		return nil
	}

	shutdownCtx, cancel := context.WithTimeout(ctx, s.cfg.shutdownTimeout)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
