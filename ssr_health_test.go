package inertia

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// A failed server-side render is invisible at the HTTP layer: gonertia falls
// back to the client-rendered container, so the response is 200 with an empty
// root element. A status-code alert will never fire. The failure counter is
// the only thing that will.
func TestSSRFailuresCountsTheFallback(t *testing.T) {
	before := SSRFailures()

	// This is the exact call gonertia makes when a render fails.
	slogLogger{}.Printf("ssr rendering error: %s", "connection refused")

	if after := SSRFailures(); after != before+1 {
		t.Errorf("SSRFailures went from %d to %d, want one more", before, after)
	}

	// An unrelated message must not be counted, or the signal is noise.
	slogLogger{}.Printf("get flash data error: %s", "no session")
	if after := SSRFailures(); after != before+1 {
		t.Errorf("an unrelated log line was counted as an SSR failure")
	}
}

// A Node process listening with a broken module graph passes a TCP check and
// fails every render. That is the state worth catching, and it is why the
// probe POSTs a page rather than dialling the port.
func TestCheckSSRRequiresARenderedBody(t *testing.T) {
	// A server that accepts the POST but answers nothing: listening, and
	// useless.
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer empty.Close()

	i := &Inertia{cfg: &Config{SSREnabled: true, SSRURL: empty.URL}}
	health := i.CheckSSR(context.Background())
	if health.OK {
		t.Error("a server answering an empty body was reported healthy")
	}
	if health.Err == "" {
		t.Error("no reason given")
	}

	// A server that renders something is healthy.
	working := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"head": []string{"<title>x</title>"},
			"body": `<div id="app">rendered</div>`,
		})
	}))
	defer working.Close()

	i = &Inertia{cfg: &Config{SSREnabled: true, SSRURL: working.URL}}
	if health := i.CheckSSR(context.Background()); !health.OK {
		t.Errorf("a working render server was reported unhealthy: %s", health.Err)
	}
}

func TestCheckSSRReportsADeadEndpoint(t *testing.T) {
	// A port nothing is listening on.
	i := &Inertia{cfg: &Config{SSREnabled: true, SSRURL: "http://127.0.0.1:1"}}
	health := i.CheckSSR(context.Background())
	if health.OK {
		t.Error("a dead endpoint was reported healthy")
	}
	if health.Err == "" {
		t.Error("no reason given")
	}
}

func TestCheckSSRWhenDisabled(t *testing.T) {
	i := &Inertia{cfg: &Config{SSREnabled: false}}
	health := i.CheckSSR(context.Background())
	if health.Enabled {
		t.Error("reported enabled when it is not")
	}
	if health.OK {
		t.Error("reported OK for a disabled renderer; a readiness check should not require what is switched off")
	}
}

func TestCheckSSRReportsAnUnresolvableURL(t *testing.T) {
	i := &Inertia{cfg: &Config{SSREnabled: true, SSRURL: ""}}
	health := i.CheckSSR(context.Background())
	if health.OK {
		t.Error("reported OK with no render URL")
	}
	if health.Err == "" {
		t.Error("no reason given for an unresolvable URL")
	}
}

// The worst silent failure in the stack: a stale hot file in production
// points both the assets and the renderer at a dev server that is not
// running, with nothing in the logs about why.
func TestAHotFileIsRefusedInProduction(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, ViteHotPath)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ViteHotPath), []byte("http://localhost:5173"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := guardHotFile(true); err == nil {
		t.Error("a hot file was allowed in production")
	}
	// Development is where a hot file belongs, so it must not be refused
	// there.
	if err := guardHotFile(false); err != nil {
		t.Errorf("a hot file was refused outside production: %v", err)
	}
}

func TestNoHotFileIsFine(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := guardHotFile(true); err != nil {
		t.Errorf("production boot was refused with no hot file: %v", err)
	}
}
