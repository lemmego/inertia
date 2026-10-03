package inertia

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// gonertia logs exactly one thing — a failed server-side render — and defaults
// to log.New(io.Discard, …). With that discarded, a broken SSR process is
// undetectable: gonertia falls back to the client-rendered container, so the
// page answers 200 and looks right to a person while a crawler gets an empty
// <div id="app">. These tests pin the default logger that makes it visible.
func TestSlogLoggerRoutesToSlogAtWarn(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	slogLogger{}.Printf("ssr rendering error: %s", "connection refused")

	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &record); err != nil {
		t.Fatalf("logger produced no parseable record: %v (%q)", err, buf.String())
	}
	if record["level"] != "WARN" {
		t.Errorf("level = %v, want WARN — a discarded or DEBUG-level SSR failure is the bug this exists to prevent", record["level"])
	}
	msg, _ := record["msg"].(string)
	if !strings.Contains(msg, "ssr rendering error") || !strings.Contains(msg, "connection refused") {
		t.Errorf("msg = %q, want the formatted gonertia message", msg)
	}
	if !strings.HasPrefix(msg, "inertia: ") {
		t.Errorf("msg = %q, want an inertia: prefix so the source is obvious in a mixed log", msg)
	}
}

func TestSlogLoggerPrintlnDoesNotDoubleNewline(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	slogLogger{}.Println("ssr", "unavailable")

	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &record); err != nil {
		t.Fatal(err)
	}
	msg, _ := record["msg"].(string)
	if strings.HasSuffix(msg, "\n") {
		t.Errorf("msg = %q, want no trailing newline — slog adds its own record boundary", msg)
	}
	if msg != "inertia: ssr unavailable" {
		t.Errorf("msg = %q", msg)
	}
}

// A dead SSR process fails fast and falls back. A hung one would hold the
// request, its goroutine and its connection for as long as it hangs, and
// gonertia's default client has no timeout at all.
func TestDefaultSSRTimeoutIsBounded(t *testing.T) {
	if defaultSSRTimeout <= 0 {
		t.Fatal("defaultSSRTimeout must be positive; gonertia's own default is no timeout")
	}
	if defaultSSRTimeout > 10*time.Second {
		t.Errorf("defaultSSRTimeout = %v, which is long enough to queue requests behind a hung render", defaultSSRTimeout)
	}
}
