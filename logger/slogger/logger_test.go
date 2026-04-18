package slogger

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/slam0504/go-ddd-core/ports/logger"
)

func TestLogger_JSONFormat(t *testing.T) {
	var buf bytes.Buffer
	l := New(Config{Writer: &buf, Level: slog.LevelInfo})

	l.Log(context.Background(), logger.LevelInfo, "hello", logger.F("user", "alice"))

	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec); err != nil {
		t.Fatalf("expected valid JSON line, got %q: %v", buf.String(), err)
	}
	if got := rec["msg"]; got != "hello" {
		t.Errorf("msg: want %q, got %v", "hello", got)
	}
	if got := rec["level"]; got != "INFO" {
		t.Errorf("level: want %q, got %v", "INFO", got)
	}
	if got := rec["user"]; got != "alice" {
		t.Errorf("user attr: want %q, got %v", "alice", got)
	}
}

func TestLogger_With_PrependsAttrs(t *testing.T) {
	var buf bytes.Buffer
	base := New(Config{Writer: &buf, Level: slog.LevelInfo})

	scoped := base.With(logger.F("service", "billing"))
	scoped.Log(context.Background(), logger.LevelInfo, "started")

	if !strings.Contains(buf.String(), `"service":"billing"`) {
		t.Errorf("expected service attr in output, got %q", buf.String())
	}
}

func TestLogger_Enabled_RespectsLevel(t *testing.T) {
	l := New(Config{Writer: &bytes.Buffer{}, Level: slog.LevelInfo})
	ctx := context.Background()

	if l.Enabled(ctx, logger.LevelDebug) {
		t.Error("Debug should be disabled when level=Info")
	}
	if !l.Enabled(ctx, logger.LevelInfo) {
		t.Error("Info should be enabled when level=Info")
	}
	if !l.Enabled(ctx, logger.LevelError) {
		t.Error("Error should be enabled when level=Info")
	}
}

func TestLogger_TextFormat(t *testing.T) {
	var buf bytes.Buffer
	l := New(Config{Writer: &buf, Format: "text"})

	l.Log(context.Background(), logger.LevelInfo, "hello")

	out := buf.String()
	if !strings.Contains(out, "msg=hello") {
		t.Errorf("expected text-format output, got %q", out)
	}
}
