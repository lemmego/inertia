package inertia

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

// ssrServer stands in for the Node renderer.
func ssrServer(t *testing.T, status int, body string) *Inertia {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/render" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`not found`))
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return &Inertia{cfg: &Config{SSREnabled: true, SSRURL: server.URL}}
}

// TestAShallowProbeAcceptsARefusal is the regression for a check that could
// never pass.
//
// The probe asks for a component the bundle cannot have, so a correct
// renderer answers 500 — and the first version of this checked the status
// before the body, reporting every healthy development renderer as unusable.
// The comment already said a refusal with a body was fine; the code
// disagreed with it.
func TestAShallowProbeAcceptsARefusal(t *testing.T) {
	renderer := ssrServer(t, http.StatusInternalServerError,
		`{"error":"Inertia SSR: no page component at ./Pages/__inertia_probe__.tsx"}`)

	health := renderer.CheckSSR(context.Background())
	if !health.OK {
		t.Fatalf("a live renderer refusing the probe component was reported unusable: %s", health.Err)
	}
	if health.Deep {
		t.Error("the probe reported itself deep with no component configured")
	}
	if health.Component != ProbeComponent {
		t.Errorf("component = %q", health.Component)
	}
	if health.Status != http.StatusInternalServerError {
		t.Errorf("status = %d, want the renderer's own", health.Status)
	}
}

// TestAnEmptyBodyIsAlwaysAFailure — the blind spot a TCP check has and the
// reason this probe exists. A Node process listening with a broken module
// graph accepts the connection and renders nothing.
func TestAnEmptyBodyIsAlwaysAFailure(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusInternalServerError} {
		renderer := ssrServer(t, status, "")
		health := renderer.CheckSSR(context.Background())
		if health.OK {
			t.Errorf("status %d with an empty body was reported healthy", status)
		}
		if !strings.Contains(health.Err, "bundle is not loaded") {
			t.Errorf("status %d: err = %q", status, health.Err)
		}
	}
}

// TestADeepProbeRequiresServerRenderedMarkup. This is the condition a shallow
// probe cannot see: a renderer that fails on every component, not just
// unknown ones.
func TestADeepProbeRequiresServerRenderedMarkup(t *testing.T) {
	t.Run("a real render passes", func(t *testing.T) {
		renderer := ssrServer(t, http.StatusOK,
			`{"body":"<div id=\"app\" data-server-rendered=\"true\">ok</div>","head":[]}`)
		renderer.cfg.SSRProbeComponent = "Health/Probe"

		health := renderer.CheckSSR(context.Background())
		if !health.OK {
			t.Fatalf("a working renderer failed the deep probe: %s", health.Err)
		}
		if !health.Deep {
			t.Error("the probe did not report itself deep")
		}
	})

	t.Run("a broken bundle fails", func(t *testing.T) {
		renderer := ssrServer(t, http.StatusInternalServerError,
			"{\"error\":\"Cannot find module './chunk-abc.js'\"}\nstack...")
		renderer.cfg.SSRProbeComponent = "Health/Probe"

		health := renderer.CheckSSR(context.Background())
		if health.OK {
			t.Fatal("a renderer failing every component passed the deep probe")
		}
		// The message carries the renderer's first line and not its stack.
		if !strings.Contains(health.Err, "Cannot find module") {
			t.Errorf("err = %q", health.Err)
		}
		if strings.Contains(health.Err, "stack...") {
			t.Errorf("the stack trace reached the health response: %q", health.Err)
		}
	})

	t.Run("a 200 with no markup fails", func(t *testing.T) {
		// The silent fallback itself: a document that renders, with an empty
		// container.
		renderer := ssrServer(t, http.StatusOK, `{"body":"<div id=\"app\"></div>","head":[]}`)
		renderer.cfg.SSRProbeComponent = "Health/Probe"

		health := renderer.CheckSSR(context.Background())
		if health.OK {
			t.Fatal("a document with an empty container passed the deep probe")
		}
		if !strings.Contains(health.Err, "no server-rendered markup") {
			t.Errorf("err = %q", health.Err)
		}
	})
}

// TestAMissingEndpointIsReportedAsConfiguration, because the fix is different
// from a broken bundle.
func TestAMissingEndpointIsReportedAsConfiguration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("Cannot POST /render"))
	}))
	defer server.Close()

	renderer := &Inertia{cfg: &Config{SSREnabled: true, SSRURL: server.URL}}
	health := renderer.CheckSSR(context.Background())
	if health.OK {
		t.Fatal("a 404 was reported healthy")
	}
	if !strings.Contains(health.Err, "no render endpoint") {
		t.Errorf("err = %q", health.Err)
	}
}

func TestAnUnreachableRendererIsReported(t *testing.T) {
	// A port nothing is listening on.
	renderer := &Inertia{cfg: &Config{SSREnabled: true, SSRURL: "http://127.0.0.1:1"}}
	health := renderer.CheckSSR(context.Background())
	if health.OK {
		t.Fatal("an unreachable renderer was reported healthy")
	}
	if health.Err == "" {
		t.Error("no reason given")
	}
}

func TestSSRDisabledIsNotAFailure(t *testing.T) {
	renderer := &Inertia{cfg: &Config{SSREnabled: false}}
	health := renderer.CheckSSR(context.Background())
	if health.Enabled {
		t.Error("reported enabled")
	}
	// Not OK and not an error either: there is nothing to be unhealthy about,
	// which is for the caller to interpret.
	if health.Err != "" {
		t.Errorf("err = %q", health.Err)
	}
}
