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

// warmingRenderer answers with an un-rendered document the first n times and
// a rendered one after, which is what Vite's /__inertia_ssr does for a second
// or two after it starts: 200 with no markup while it transforms the SSR
// entry lazily.
func warmingRenderer(coldAttempts int) (func(http.ResponseWriter) error, *int) {
	attempts := 0
	return func(w http.ResponseWriter) error {
		attempts++
		body := renderedDocument
		if attempts <= coldAttempts {
			body = fallbackDocument
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, err := w.Write([]byte(body))
		return err
	}, &attempts
}

// TestAWarmingRendererIsRetriedRatherThanFailed.
//
// Vite answers 200 with an empty body for the first second or two after it
// starts, so the first page somebody opens after `lemmego dev` comes up had
// no marker through no fault of its own and got a 500. One retry turns that
// into one slow page.
func TestAWarmingRendererIsRetriedRatherThanFailed(t *testing.T) {
	SetStrictSSR(true)
	t.Cleanup(func() { SetStrictSSR(true) })

	before := SSRFailures()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/@ada", nil)

	render, attempts := warmingRenderer(1)
	err := guardedInertia(true).guardedRender(recorder, request, "Profiles/Show", render)

	if err != nil {
		t.Fatalf("err = %v, want the retry to have rendered", err)
	}
	if *attempts != 2 {
		t.Errorf("the renderer was called %d times, want 2 — one cold, one retry", *attempts)
	}
	if !strings.Contains(recorder.Body.String(), ServerRenderedMarker) {
		t.Errorf("the retry's document was not sent: %s", recorder.Body.String())
	}
	// A warmup is not a fallback. Counting it would have the health check and
	// SSRFailures() report a problem that resolved itself.
	if got := SSRFailures(); got != before {
		t.Errorf("SSRFailures went from %d to %d; a successful retry must not count", before, got)
	}
}

// TestARendererThatStaysBrokenStillFails, so the retry has not turned the
// guard off.
func TestARendererThatStaysBrokenStillFails(t *testing.T) {
	SetStrictSSR(true)
	t.Cleanup(func() { SetStrictSSR(true) })

	before := SSRFailures()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/@ada", nil)

	render, attempts := warmingRenderer(99)
	err := guardedInertia(true).guardedRender(recorder, request, "Profiles/Show", render)

	if !errors.Is(err, ErrSSRFellBack) {
		t.Fatalf("err = %v, want ErrSSRFellBack", err)
	}
	if *attempts != 2 {
		t.Errorf("the renderer was called %d times, want exactly 2 — one retry, not a loop", *attempts)
	}
	if recorder.Body.Len() != 0 {
		t.Errorf("something was written, so the caller cannot answer 500: %s", recorder.Body.String())
	}
	if got := SSRFailures(); got != before+1 {
		t.Errorf("SSRFailures went from %d to %d, want one more", before, got)
	}
}

// TestProductionDoesNotRetry, because the fallback is already going out and
// making a real visitor wait for a second attempt is the wrong trade.
func TestProductionDoesNotRetry(t *testing.T) {
	SetStrictSSR(false)
	t.Cleanup(func() { SetStrictSSR(true) })

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/@ada", nil)

	render, attempts := warmingRenderer(1)
	if err := guardedInertia(true).guardedRender(recorder, request, "Profiles/Show", render); err != nil {
		t.Fatalf("err = %v, want the fallback to be served", err)
	}
	if *attempts != 1 {
		t.Errorf("the renderer was called %d times in production, want 1", *attempts)
	}
	if strings.Contains(recorder.Body.String(), ServerRenderedMarker) {
		t.Error("production served a retried render rather than the fallback")
	}
}
