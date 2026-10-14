package inertia

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeHotFile publishes a Vite dev-server URL the way @inertiajs/vite does.
func writeHotFile(t *testing.T, url string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(ViteHotPath), 0o755); err != nil {
		t.Fatalf("creating the public directory: %v", err)
	}
	if err := os.WriteFile(ViteHotPath, []byte(url), 0o600); err != nil {
		t.Fatalf("writing the hot file: %v", err)
	}
}

// The case that made every page of a fresh project a 500.
//
// `lemmego dev` starts air before vite, so when the Go process boots there is
// no ./public/hot and the renderer address resolves to the standalone
// fallback, which dev deliberately never starts. With a single boot-time
// lookup that address was then fixed for the life of the process: vite came
// up a second later, published its URL, and nothing ever read it.
//
// No internals are touched here — it waits out the real TTL — because a test
// that reaches in and clears the cache would also pass against a resolver
// that never refreshed on its own, which is the bug.
func TestTheRendererIsFoundWhenViteComesUpAfterTheServer(t *testing.T) {
	t.Chdir(t.TempDir())

	var got string
	renderer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer renderer.Close()

	transport := newSSRTransport(nil)
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}

	// Boot with no hot file, exactly as dev does, and let the fallback be
	// cached — this is the state the bug left the process in permanently.
	if addr := transport.current(); addr != standaloneSSRURL {
		t.Fatalf("with no hot file the address is %q, want the standalone renderer", addr)
	}

	// Vite starts a moment later and publishes its address.
	writeHotFile(t, renderer.URL)
	time.Sleep(ssrResolveTTL + 100*time.Millisecond)

	// gonertia still renders against whatever it was configured with at boot.
	// The transport is what redirects it.
	request, err := http.NewRequest(http.MethodPost, standaloneSSRURL+"/render", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("the render never reached the dev server: %v", err)
	}
	defer response.Body.Close()

	if got != "/__inertia_ssr" {
		t.Fatalf("the renderer was asked for %q, want /__inertia_ssr — the "+
			"hot file names a Vite dev server, which serves the renderer "+
			"there rather than at /render", got)
	}
}

// In production there is no hot file, and the answer must be the standalone
// renderer rather than an invented address or a swallowed request.
func TestWithNoHotFileTheAddressIsTheStandaloneRenderer(t *testing.T) {
	t.Chdir(t.TempDir())

	if addr := newSSRTransport(nil).current(); addr != standaloneSSRURL {
		t.Fatalf("got %q, want %q", addr, standaloneSSRURL)
	}
}

// And the request has to arrive at /render, which is the production path and
// the one this transport nearly broke.
//
// The standalone address is bare, so url.Parse gives it an empty Path.
// Assigning that over the request's path replaced gonertia's "/render" with
// "" and every render reached the renderer's root instead — a 500 on every
// page, in production only. The earlier version of this test checked the
// resolved *address* and never made a request, so it could not see it; a
// scaffolded project booting against a real Node renderer could, and did.
func TestWithNoHotFileTheRenderPathSurvives(t *testing.T) {
	t.Chdir(t.TempDir())

	paths := make(chan string, 1)
	renderer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer renderer.Close()

	// The renderer is on a test port rather than 13714, so the resolution is
	// pinned through the hot file and then the file is removed — leaving the
	// transport to resolve the bare standalone address while the request
	// itself still points somewhere reachable.
	transport := newSSRTransport(nil)
	transport.resolved = renderer.URL
	transport.at = time.Now()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}

	request, err := http.NewRequest(http.MethodPost, renderer.URL+"/render", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("the render never arrived: %v", err)
	}
	defer response.Body.Close()

	if got := <-paths; got != "/render" {
		t.Fatalf("the renderer was asked for %q, want /render — a bare "+
			"address must not blank out the path gonertia set", got)
	}
}

// Re-reading the file on every render would put a syscall on the hot path for
// no benefit in production, where the answer never changes.
func TestTheResolvedAddressIsCached(t *testing.T) {
	t.Chdir(t.TempDir())
	writeHotFile(t, "http://127.0.0.1:5173")

	transport := newSSRTransport(nil)
	first := transport.current()

	// A second Vite, published while the cache is still warm.
	writeHotFile(t, "http://127.0.0.1:4173")
	if second := transport.current(); second != first {
		t.Fatalf("the address was re-read inside the TTL: %q then %q", first, second)
	}

	time.Sleep(ssrResolveTTL + 100*time.Millisecond)
	if third := transport.current(); third == first {
		t.Fatalf("the address was never re-read after the TTL expired: %q", third)
	}
}
