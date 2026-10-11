package inertia

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// renderStub stands in for gonertia's render: it writes whatever a broken or
// working SSR pipeline would end up writing to the response.
func renderStub(body string) func(http.ResponseWriter) error {
	return func(w http.ResponseWriter) error {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, err := w.Write([]byte(body))
		return err
	}
}

const (
	renderedDocument = `<!DOCTYPE html><html><body>` +
		`<div id="app" data-server-rendered="true"><h1>Ada</h1></div></body></html>`
	// What the fallback produces: a correct document with an empty container.
	fallbackDocument = `<!DOCTYPE html><html><body>` +
		`<div id="app" data-page="{&quot;component&quot;:&quot;Profiles/Show&quot;}"></div></body></html>`
)

func guardedInertia(ssr bool) *Inertia {
	return &Inertia{cfg: &Config{SSREnabled: ssr}}
}

// TestAFallbackIsAHardFailureInStrictMode is the behaviour this file exists
// for. A render that falls back answers 200 with an empty container, which is
// a blank page that every status-code check in the stack calls healthy.
func TestAFallbackIsAHardFailureInStrictMode(t *testing.T) {
	SetStrictSSR(true)
	t.Cleanup(func() { SetStrictSSR(true) })

	before := SSRFailures()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/@ada", nil)

	err := guardedInertia(true).guardedRender(recorder, request, "Profiles/Show",
		renderStub(fallbackDocument))

	if !errors.Is(err, ErrSSRFellBack) {
		t.Fatalf("err = %v, want ErrSSRFellBack", err)
	}
	// Nothing was written, which is what lets the caller answer 500 rather
	// than having already sent a blank page.
	if recorder.Body.Len() != 0 {
		t.Errorf("a blank page was sent anyway:\n%s", recorder.Body.String())
	}
	if got := SSRFailures(); got != before+1 {
		t.Errorf("failures = %d, want %d", got, before+1)
	}
}

// TestAFallbackIsServedInProductionMode. The fallback is the right behaviour
// for a real visitor — a page that hydrates a moment later beats a 500, and
// an SSR process falling over should not take the site down.
func TestAFallbackIsServedInProductionMode(t *testing.T) {
	SetStrictSSR(false)
	t.Cleanup(func() { SetStrictSSR(true) })

	before := SSRFailures()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/@ada", nil)

	err := guardedInertia(true).guardedRender(recorder, request, "Profiles/Show",
		renderStub(fallbackDocument))
	if err != nil {
		t.Fatalf("production mode refused to serve the fallback: %v", err)
	}
	if !strings.Contains(recorder.Body.String(), `id="app"`) {
		t.Errorf("the fallback page was not sent:\n%s", recorder.Body.String())
	}
	// Served, and still counted. The page works and the operator is told.
	if got := SSRFailures(); got != before+1 {
		t.Errorf("failures = %d, want %d — a served fallback is still a fallback", got, before+1)
	}
}

func TestAServerRenderedDocumentPassesThrough(t *testing.T) {
	SetStrictSSR(true)
	before := SSRFailures()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/@ada", nil)

	if err := guardedInertia(true).guardedRender(recorder, request, "Profiles/Show",
		renderStub(renderedDocument)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(recorder.Body.String(), "<h1>Ada</h1>") {
		t.Errorf("the document was altered:\n%s", recorder.Body.String())
	}
	// Headers set during the render survive the buffering.
	if got := recorder.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := SSRFailures(); got != before {
		t.Errorf("a successful render was counted as a failure")
	}
}

// TestAnInertiaXHRIsNotGuarded. An Inertia visit gets JSON, which has no
// marker to look for — guarding it would fail every client-side navigation.
func TestAnInertiaXHRIsNotGuarded(t *testing.T) {
	SetStrictSSR(true)
	before := SSRFailures()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/@ada", nil)
	request.Header.Set("X-Inertia", "true")

	if err := guardedInertia(true).guardedRender(recorder, request, "Profiles/Show",
		renderStub(`{"component":"Profiles/Show","props":{}}`)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(recorder.Body.String(), `"component"`) {
		t.Errorf("the JSON response was altered:\n%s", recorder.Body.String())
	}
	if got := SSRFailures(); got != before {
		t.Errorf("an XHR was counted as an SSR failure")
	}
}

// TestWithSSRDisabledNothingIsGuarded. There is nothing to fall back from.
func TestWithSSRDisabledNothingIsGuarded(t *testing.T) {
	SetStrictSSR(true)
	before := SSRFailures()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/@ada", nil)

	if err := guardedInertia(false).guardedRender(recorder, request, "Profiles/Show",
		renderStub(fallbackDocument)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(recorder.Body.String(), `id="app"`) {
		t.Error("the page was not sent")
	}
	if got := SSRFailures(); got != before {
		t.Error("a client-rendered application was counted as an SSR failure")
	}
}

// TestARenderErrorIsPassedThroughUntouched. gonertia failing outright is a
// different thing from gonertia falling back, and the caller's error path
// already owns it.
func TestARenderErrorIsPassedThroughUntouched(t *testing.T) {
	SetStrictSSR(true)
	before := SSRFailures()

	boom := errors.New("the root template does not parse")
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/@ada", nil)

	err := guardedInertia(true).guardedRender(recorder, request, "Profiles/Show",
		func(http.ResponseWriter) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the render's own error", err)
	}
	if got := SSRFailures(); got != before {
		t.Error("an outright render error was counted as a fallback")
	}
}

// TestTheStatusCodeSurvivesBuffering: a handler that set 404 before rendering
// must still answer 404, not 200.
func TestTheStatusCodeSurvivesBuffering(t *testing.T) {
	SetStrictSSR(true)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/@nobody", nil)

	err := guardedInertia(true).guardedRender(recorder, request, "Errors/NotFound",
		func(w http.ResponseWriter) error {
			w.WriteHeader(http.StatusNotFound)
			_, err := w.Write([]byte(renderedDocument))
			return err
		})
	if err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", recorder.Code)
	}
}

func TestStrictnessComesFromTheEnvironment(t *testing.T) {
	// Staging is strict too, deliberately: staging exists to find this before
	// production does, and one that degrades the way production would teaches
	// nothing.
	for env, strict := range map[string]bool{
		"production":  false,
		"Production":  false,
		" production": false,
		"staging":     true,
		"development": true,
		"test":        true,
		"":            true,
	} {
		if got := ssrStrictFromEnv(env); got != strict {
			t.Errorf("%q: strict = %v, want %v", env, got, strict)
		}
	}
}
