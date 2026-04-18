// Package slogger provides a logger.Logger adapter backed by the standard
// library log/slog package. It is the lowest-friction option for projects
// that want structured logging without an extra dependency.
//
// Construct with [New] for the common case (writer/level/format), or with
// [NewWithHandler] when a custom slog.Handler is required (for example,
// to chain handlers or wire a non-standard sink).
//
// The adapter does not register itself as the global slog default; callers
// stay in control of their slog.SetDefault decision.
package slogger
