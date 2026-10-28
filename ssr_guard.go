package inertia

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
)

// ServerRenderedMarker is what a server-rendered document contains.
//
// Its absence from a document response is the only reliable signal that the
// render fell back, and it is a better one than the log line the counter in
// ssr_health.go watches for: that depends on gonertia's wording, which is
// somebody else's string literal, and it fires after the response has
// already been decided. This is our own output.
const ServerRenderedMarker = `data-server-rendered="true"`

// ErrSSRFellBack is returned by a document render that produced no
// server-rendered markup while server-side rendering was enabled.
//
// Returned rather than logged-and-swallowed so the caller decides. In
// development that decision is a 500; in production it is a degraded page and
// a loud log.
var ErrSSRFellBack = errors.New(
	"inertia: server-side rendering is enabled but the document came back with no " +
		"server-rendered markup, which means the renderer failed and the page will be blank " +
		"until the client bundle takes over")

// strictSSR makes a fallback a hard failure.
//
// Default on, off in production, and that asymmetry is the whole point.
//
// The fallback is the right behaviour for a real visitor: a page that
// hydrates a moment later beats a 500, and an SSR process that has fallen
// over should not take the site down with it. It is the wrong behaviour for
// everybody else. A developer gets a blank page with a 200 and a correct
// <head>; a test that checks a status code passes; a crawler gets an empty
// document and says nothing. The failure is designed to be survivable, which
// makes it invisible, and invisible is how it reaches production.
//
// So outside production a failed render is a 500 with the reason in it, and
// in production it is the fallback plus an error-level log and a counter.
var strictSSR atomic.Bool

func init() { strictSSR.Store(true) }

// SetStrictSSR turns the hard failure on or off. Provider calls it.
func SetStrictSSR(strict bool) { strictSSR.Store(strict) }

// StrictSSR reports whether a fallback is currently a hard failure.
func StrictSSR() bool { return strictSSR.Load() }

// captureWriter buffers a response so it can be inspected before it is sent.
//
// Buffering a response is not free and is not always acceptable, which is why
// this is used for exactly one thing: a full Inertia *document*, which is a
// few kilobytes of HTML with the props inlined. An Inertia XHR response is
// JSON and is never buffered — it has no server-rendered marker to look for,
// so buffering it would both cost something and prove nothing. Neither is a
// file download or a stream, which do not come through this path at all.
//
// It deliberately does not implement http.Flusher. A handler that flushes
// mid-render wants its bytes out, and silently buffering them anyway would
// turn a streaming response into a non-streaming one — a document render does
// not stream, so the right answer is for the type not to offer it.
type captureWriter struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func newCaptureWriter(real http.ResponseWriter) *captureWriter {
	// The real writer's header map is reused rather than copied, so
	// everything gonertia sets — Content-Type, Vary — lands where it would
	// have. Only the body and the status are held back.
	return &captureWriter{header: real.Header()}
}

func (w *captureWriter) Header() http.Header { return w.header }

func (w *captureWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(b)
}

func (w *captureWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

// flushTo copies the buffered response to the real writer.
func (w *captureWriter) flushTo(real http.ResponseWriter) error {
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	real.WriteHeader(status)
	_, err := real.Write(w.body.Bytes())
	return err
}

// serverRendered reports whether the buffered document carries the marker.
func (w *captureWriter) serverRendered() bool {
	return bytes.Contains(w.body.Bytes(), []byte(ServerRenderedMarker))
}

// guardedRender renders a document and checks that it was server-rendered.
//
// It returns ErrSSRFellBack when the marker is missing in strict mode, having
// written nothing — which is what lets the caller answer 500 instead of
// sending a blank page. Outside strict mode the page is sent, because a
// degraded page is better than none for somebody who is just trying to read
// the site.
func (i *Inertia) guardedRender(real http.ResponseWriter, r *http.Request, component string, render func(http.ResponseWriter) error) error {
	// Only a document render is guarded. An Inertia XHR is JSON, with no
	// marker to find, and a request that is not server-rendered by
	// configuration has nothing to fall back from.
	if !i.cfg.SSREnabled || IsInertiaRequest(r) {
		return render(real)
	}

	capture := newCaptureWriter(real)
	if err := render(capture); err != nil {
		// gonertia failed outright rather than falling back. Nothing has
		// been written, so the caller's error path owns the response.
		return err
	}

	if capture.serverRendered() {
		return capture.flushTo(real)
	}

	// A renderer that has only just started answers 200 with an empty body
	// for a second or two: Vite transforms the SSR entry lazily, so the first
	// document rendered after `lemmego dev` comes up has no marker through no
	// fault of the page. Retrying once turns that into one slow page instead
	// of a 500 on whatever somebody happened to open first.
	//
	// Strict mode only. Outside it the fallback is already going out, and
	// making a real visitor wait for a second attempt is the wrong trade —
	// the fallback exists precisely so they do not have to.
	//
	// The failure is not counted when the retry succeeds, because a warmup is
	// not a fallback: inflating the counter would make the health check and
	// SSRFailures() report a problem that resolved itself.
	if StrictSSR() {
		retry := newCaptureWriter(real)
		if err := render(retry); err == nil && retry.serverRendered() {
			slog.Warn("inertia: a page rendered only on the second attempt, which "+
				"is what a renderer that has just started looks like",
				"component", component, "path", r.URL.Path)
			return retry.flushTo(real)
		}
	}

	// The render fell back. gonertia has already logged its own reason, which
	// is the part that says *why*; this says which page, and is at error
	// level because every visitor to it is getting a blank document.
	noteSSRFailure()
	slog.Error("inertia: a page fell back to client rendering, so its document is empty",
		"component", component,
		"path", r.URL.Path,
		"strict", StrictSSR(),
		"failures", SSRFailures())

	if StrictSSR() {
		// Nothing was written, so this is still a clean 500.
		return ErrSSRFellBack
	}
	return capture.flushTo(real)
}

// ssrStrictFromEnv decides strictness from the application environment.
//
// Anything but production is strict. Staging included, deliberately: staging
// exists to find this kind of thing before production does, and a staging
// deploy that silently degrades the way production would is a staging deploy
// that teaches you nothing.
func ssrStrictFromEnv(env string) bool {
	return !strings.EqualFold(strings.TrimSpace(env), "production")
}
