package inertia

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// The case this file exists for.
//
// @inertiajs/vite answers 200 with the body `null` while it loads the SSR
// entry, and on an application with a few dozen pages that can take tens of
// seconds. Passed through, it becomes a document with no markup — which the
// guard calls a fallback and, outside production, a 500. So the first page
// anybody opened after the dev server started errored, and the cause was a
// renderer that was merely busy.
func TestAWarmingViteIsWaitedOutRatherThanFailed(t *testing.T) {
	t.Chdir(t.TempDir())

	const rendered = `{"head":[],"body":"<div data-server-rendered=\"true\">ok</div>"}`

	var attempts int32
	renderer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if atomic.AddInt32(&attempts, 1) <= 2 {
			// Exactly what the plugin sends while warming up.
			_, _ = w.Write([]byte("null"))
			return
		}
		_, _ = w.Write([]byte(rendered))
	}))
	defer renderer.Close()

	writeHotFile(t, renderer.URL)

	// No client-level Timeout, because the budget is the transport's.
	client := &http.Client{Transport: newSSRTransport(nil)}

	started := time.Now()
	response, err := client.Post(standaloneSSRURL+"/render", "application/json", nil)
	if err != nil {
		t.Fatalf("the render failed instead of waiting: %v", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != rendered {
		t.Fatalf("body = %q, want the rendered page", body)
	}

	// The probe, not just the result: a transport that gave up and returned
	// the first `null` would also have produced a 200, so what matters is
	// that it asked again.
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Errorf("the renderer was asked %d times, want 3 — two warm-ups then the render", got)
	}
	if waited := time.Since(started); waited < 2*viteWarmupPoll {
		t.Errorf("waited %s, want at least %s — it cannot have paused between attempts", waited, 2*viteWarmupPoll)
	}
}

// A renderer that keeps saying "not ready" has to stop being waited for, or a
// dev server that failed its warm-up would hold every request for a minute.
func TestAViteThatNeverWarmsUpEventuallyFails(t *testing.T) {
	t.Chdir(t.TempDir())

	renderer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("null"))
	}))
	defer renderer.Close()

	writeHotFile(t, renderer.URL)

	transport := newSSRTransport(nil).withBudget(600 * time.Millisecond)
	client := &http.Client{Transport: transport}

	if _, err := client.Post(standaloneSSRURL+"/render", "application/json", nil); err == nil {
		t.Fatal("a renderer that never warmed up was reported as a successful render")
	}
}

// Only the plugin's own signal counts. An empty body is equally unrenderable
// and deliberately not treated the same: a renderer answering nothing is
// broken rather than busy, and waiting out the budget would turn a clear
// failure into a slow one.
func TestOnlyALiteralNullIsAWarmUp(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"the plugin's warm-up answer", 200, "null", true},
		{"the same with whitespace", 200, "  null\n", true},
		{"a rendered page", 200, `{"head":[],"body":"<div></div>"}`, false},
		{"nothing at all", 200, "", false},
		{"a refusal", 500, "null", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ssrWarmingUp(c.status, []byte(c.body)); got != c.want {
				t.Errorf("ssrWarmingUp(%d, %q) = %v, want %v", c.status, c.body, got, c.want)
			}
		})
	}
}

// The two renderers are different machines and get budgets an order of
// magnitude apart: one serves a bundle built before it started, the other may
// still be building one.
func TestEachRendererGetsItsOwnBudget(t *testing.T) {
	transport := newSSRTransport(nil)

	if got := transport.budgetFor(false); got != defaultSSRTimeout {
		t.Errorf("standalone budget = %s, want %s", got, defaultSSRTimeout)
	}
	if got := transport.budgetFor(true); got != viteSSRTimeout {
		t.Errorf("vite budget = %s, want %s", got, viteSSRTimeout)
	}

	// A caller who names a number has overridden the distinction.
	pinned := newSSRTransport(nil).withBudget(5 * time.Second)
	if got := pinned.budgetFor(false); got != 5*time.Second {
		t.Errorf("pinned standalone budget = %s, want 5s", got)
	}
	if got := pinned.budgetFor(true); got != 5*time.Second {
		t.Errorf("pinned vite budget = %s, want 5s", got)
	}
}

func TestOnlyTheViteEndpointIsRecognised(t *testing.T) {
	cases := map[string]bool{
		"/__inertia_ssr":        true,
		"/__inertia_ssr/render": true,
		"/render":               false,
		"/":                     false,
		"/__inertia_ssrvariant": false,
	}
	for path, want := range cases {
		if got := isViteSSRPath(path); got != want {
			t.Errorf("isViteSSRPath(%q) = %v, want %v", path, got, want)
		}
	}
}

// WithSSRHTTPClient is the option somebody reaches for to raise the SSR
// timeout, and taking the client verbatim silently dropped the resolving
// transport with it — which reintroduces the bug in ssr_resolve.go's own doc
// comment: every page of a fresh project answering 500 under `lemmego dev`,
// because the renderer's address was fixed before vite published one.
func TestASuppliedClientKeepsTheResolvingTransport(t *testing.T) {
	supplied := &http.Client{Timeout: 42 * time.Second}

	client := ssrClient(supplied, 0)

	if _, ok := client.Transport.(*ssrRedirector); !ok {
		t.Fatalf("transport = %T, want *ssrRedirector", client.Transport)
	}
	if client.Timeout != 42*time.Second {
		t.Errorf("Timeout = %s, want the caller's 42s", client.Timeout)
	}
	if client == supplied {
		t.Error("the caller's own client was handed back, so it was mutated")
	}
	if supplied.Transport != nil {
		t.Error("the caller's own client had its transport replaced")
	}
}

func TestTheDefaultSSRClientHasNoClientLevelTimeout(t *testing.T) {
	client := ssrClient(nil, 0)

	// A Timeout here would have to be the larger of the two budgets and so
	// could not bound the smaller one; the transport owns the deadline.
	if client.Timeout != 0 {
		t.Errorf("Timeout = %s, want zero — the transport bounds the render", client.Timeout)
	}
	if _, ok := client.Transport.(*ssrRedirector); !ok {
		t.Fatalf("transport = %T, want *ssrRedirector", client.Transport)
	}
}
