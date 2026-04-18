package slogger

import (
	"context"
	"io"
	"log/slog"
	"os"

	"github.com/slam0504/go-ddd-core/ports/logger"
)

// Config controls how a Logger is built.
//
// Zero values are usable: an empty Config writes JSON at Info to stdout.
type Config struct {
	// Writer is the destination for log records. Defaults to os.Stdout.
	Writer io.Writer
	// Level is the minimum level emitted. Defaults to slog.LevelInfo.
	Level slog.Level
	// Format selects "json" (default) or "text".
	Format string
	// AddSource causes the file:line of the caller to be attached to records.
	AddSource bool
}

// Logger is a logger.Logger backed by log/slog.
type Logger struct {
	inner *slog.Logger
}

// New constructs a Logger from a Config.
func New(cfg Config) *Logger {
	w := cfg.Writer
	if w == nil {
		w = os.Stdout
	}
	opts := &slog.HandlerOptions{Level: cfg.Level, AddSource: cfg.AddSource}
	var h slog.Handler
	if cfg.Format == "text" {
		h = slog.NewTextHandler(w, opts)
	} else {
		h = slog.NewJSONHandler(w, opts)
	}
	return &Logger{inner: slog.New(h)}
}

// NewWithHandler wraps an arbitrary slog.Handler. Use this when [Config]
// does not express what you need — for example to chain handlers, attach
// a context propagator, or write to a non-stream sink.
func NewWithHandler(h slog.Handler) *Logger {
	return &Logger{inner: slog.New(h)}
}

// Log emits a record at the mapped slog level.
func (l *Logger) Log(ctx context.Context, level logger.Level, msg string, attrs ...logger.Attr) {
	l.inner.LogAttrs(ctx, slog.Level(level), msg, toSlogAttrs(attrs)...)
}

// With returns a Logger derived with the given attrs prepended to every record.
func (l *Logger) With(attrs ...logger.Attr) logger.Logger {
	sl := l.inner.With(toAnySlice(toSlogAttrs(attrs))...)
	return &Logger{inner: sl}
}

// Enabled reports whether the underlying handler would emit this level.
func (l *Logger) Enabled(ctx context.Context, level logger.Level) bool {
	return l.inner.Enabled(ctx, slog.Level(level))
}

var _ logger.Logger = (*Logger)(nil)

func toSlogAttrs(in []logger.Attr) []slog.Attr {
	out := make([]slog.Attr, len(in))
	for i, a := range in {
		out[i] = slog.Any(a.Key, a.Value)
	}
	return out
}

func toAnySlice(in []slog.Attr) []any {
	out := make([]any, len(in))
	for i, a := range in {
		out[i] = a
	}
	return out
}
