package inertia

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/romsar/gonertia/v3"
)

func withSession(id string) context.Context {
	return context.WithValue(context.Background(), "sessionID", id)
}

// An anonymous visitor has no session, which means they have no flash data.
// That is a fact, not a failure. GetFlash was the only one of the six
// FlashProvider methods that reported it as an error, and since
// Provider.Provide installs this store for every Inertia application, the
// result was a logged warning on every single anonymous request.
func TestGetFlashWithoutASessionIsNotAnError(t *testing.T) {
	store := NewFlash()

	flash, err := store.GetFlash(context.Background())
	if err != nil {
		t.Fatalf("GetFlash without a session = %v, want no error", err)
	}
	if len(flash) != 0 {
		t.Errorf("GetFlash returned %+v, want no flash data", flash)
	}
}

// The other five already behave this way; this pins the consistency so the
// odd one out cannot come back.
func TestFlashProviderIsConsistentWithoutASession(t *testing.T) {
	store := NewFlash()
	ctx := context.Background()

	if err := store.Flash(ctx, Flash{}); err != nil {
		t.Errorf("Flash: %v", err)
	}
	if err := store.FlashErrors(ctx, ValidationErrors{}); err != nil {
		t.Errorf("FlashErrors: %v", err)
	}
	if err := store.FlashClearHistory(ctx); err != nil {
		t.Errorf("FlashClearHistory: %v", err)
	}
	if _, err := store.GetErrors(ctx); err != nil {
		t.Errorf("GetErrors: %v", err)
	}
	if _, err := store.ShouldClearHistory(ctx); err != nil {
		t.Errorf("ShouldClearHistory: %v", err)
	}
	if _, err := store.GetFlash(ctx); err != nil {
		t.Errorf("GetFlash: %v", err)
	}
}

// The store is shared by every request in the process and holds three plain
// maps. Without synchronisation, two concurrent requests are a concurrent map
// write — which is a fatal runtime error, not a panic any recoverer can
// catch. Run with -race.
func TestFlashStoreIsSafeForConcurrentRequests(t *testing.T) {
	store := NewFlash()

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Distinct sessions, as distinct visitors would have: the race is
			// on the map itself, not on one entry.
			ctx := withSession(string(rune('a' + i%26)))
			_ = store.Flash(ctx, Flash{"message": "saved"})
			_ = store.FlashErrors(ctx, ValidationErrors{"email": "taken"})
			_ = store.FlashClearHistory(ctx)
			_, _ = store.GetFlash(ctx)
			_, _ = store.GetErrors(ctx)
			_, _ = store.ShouldClearHistory(ctx)
		}(i)
	}
	wg.Wait()
}

// Flash data is read once and consumed, for each of the two carriers.
func TestFlashIsConsumedOnRead(t *testing.T) {
	store := NewFlash()
	ctx := withSession("s1")

	if err := store.Flash(ctx, Flash{"message": "saved"}); err != nil {
		t.Fatal(err)
	}
	first, err := store.GetFlash(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first["message"] != "saved" {
		t.Fatalf("first read = %+v", first)
	}
	second, err := store.GetFlash(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Errorf("second read = %+v, want it consumed", second)
	}
}

// A favicon must not eat a flash message.
//
// Flash data is one-shot: gonertia's middleware reads and clears it on every
// request it wraps, and Provider.Provide installs that middleware on the whole
// router rather than per route. So any request a browser makes between a
// redirect and the page it redirected to — a favicon, a stylesheet, a
// prefetch — consumes the message the page was going to show.
//
// The symptom is a flash that vanishes now and then with nothing in any log,
// which is why this is a test rather than a note. It was found by watching a
// browser do it: a 404 for /favicon.ico, then a document with no flash.
func TestASubresourceRequestDoesNotConsumeFlashData(t *testing.T) {
	i, store := middlewareForFlashTest(t)

	var seen []gonertia.Flash
	handler := i.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, gonertia.FlashFromContext(r.Context()))
	}))

	ctx := withSession("session-1")
	if err := store.Flash(ctx, Flash{"success": "Saved"}); err != nil {
		t.Fatalf("Flash: %v", err)
	}

	// A browser's own favicon request, labelled the way a browser labels it.
	favicon := httptest.NewRequest(http.MethodGet, "/favicon.ico", nil).WithContext(ctx)
	favicon.Header.Set("Sec-Fetch-Dest", "image")
	handler.ServeHTTP(httptest.NewRecorder(), favicon)

	// Then the document the redirect was for.
	document := httptest.NewRequest(http.MethodGet, "/things", nil).WithContext(ctx)
	document.Header.Set("Sec-Fetch-Dest", "document")
	handler.ServeHTTP(httptest.NewRecorder(), document)

	if len(seen) != 2 {
		t.Fatalf("the middleware did not reach the handler twice: %d", len(seen))
	}
	if got := seen[0]["success"]; got != nil {
		t.Errorf("the favicon request was handed the flash message (%v), which "+
			"means it consumed it", got)
	}
	if got := seen[1]["success"]; got != "Saved" {
		t.Errorf("the document was handed %v, want \"Saved\" — something ate the "+
			"flash before the page that was supposed to show it", got)
	}
}

// And the ordinary paths still read it, which is the half that would make the
// fix above vacuous if it over-reached: a navigation, an Inertia visit, and a
// client that sends no fetch metadata at all.
func TestAPageRequestStillConsumesFlashData(t *testing.T) {
	for _, tt := range []struct {
		name    string
		headers map[string]string
	}{
		{"a document navigation", map[string]string{"Sec-Fetch-Dest": "document"}},
		{"an inertia visit", map[string]string{"Sec-Fetch-Dest": "empty", "X-Inertia": "true"}},
		{"a client with no fetch metadata", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			i, store := middlewareForFlashTest(t)

			var seen gonertia.Flash
			handler := i.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = gonertia.FlashFromContext(r.Context())
			}))

			ctx := withSession("session-1")
			if err := store.Flash(ctx, Flash{"success": "Saved"}); err != nil {
				t.Fatalf("Flash: %v", err)
			}

			r := httptest.NewRequest(http.MethodGet, "/things", nil).WithContext(ctx)
			for k, v := range tt.headers {
				r.Header.Set(k, v)
			}
			handler.ServeHTTP(httptest.NewRecorder(), r)

			if got := seen["success"]; got != "Saved" {
				t.Errorf("got %v, want \"Saved\"", got)
			}
		})
	}
}

// middlewareForFlashTest builds the smallest Inertia that has a flash
// provider, without an app.App — these two tests are about the middleware and
// the store, and booting a container would only add ways to fail.
func middlewareForFlashTest(t *testing.T) (*Inertia, FlashProvider) {
	t.Helper()

	root := filepath.Join(t.TempDir(), "root.html")
	if err := os.WriteFile(root, []byte("<html>{{ .inertia }}</html>"), 0o600); err != nil {
		t.Fatal(err)
	}

	store := NewFlash()
	gi, err := gonertia.NewFromFile(root, gonertia.WithFlashProvider(&gonertiaFlashBridge{provider: store}))
	if err != nil {
		t.Fatalf("gonertia.NewFromFile: %v", err)
	}
	return &Inertia{inner: gi, cfg: &Config{}}, store
}
