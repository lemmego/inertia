package inertia

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ssrResolveTTL bounds how often the hot file is consulted.
//
// One stat per second per process, against a render path that already makes an
// HTTP request to Node. Long enough that it is free, short enough that a dev
// server coming up is picked up on the next page load rather than the next
// restart.
const ssrResolveTTL = time.Second

// standaloneSSRURL is where @inertiajs/react's own server listens by default,
// and so where a render goes when no Vite dev server has published itself.
const standaloneSSRURL = "http://127.0.0.1:13714"

// viteSSRPath is the endpoint @inertiajs/vite mounts on the dev server, and so
// what detectSSRURL appends to the address in the hot file. It is also how a
// render tells the two renderers apart, which decides both how long it is
// given and whether a "not ready yet" answer is understood — so there is one
// spelling of it.
const viteSSRPath = "/__inertia_ssr"

// viteWarmupPoll is how long to wait before asking a warming Vite server
// again. Short enough that the wait costs little once it is ready, long
// enough not to spin on a server that is busy compiling.
const viteWarmupPoll = 250 * time.Millisecond

// ssrRedirector rewrites each server-render request to wherever the renderer
// is *now*, rather than wherever it was when the process booted, and bounds
// each render by what the renderer on the other end can reasonably take.
//
// Resolving once at construction cannot work under `lemmego dev`, and the
// reason is a race worth knowing about. Strict SSR is on outside production,
// so a failed render is an error rather than a silent degrade. `dev` does not
// start the standalone renderer, because @inertiajs/vite already serves
// /__inertia_ssr from the Vite dev server, and that URL is published by
// writing ./public/hot. But `dev` starts air before vite, so on first run the
// hot file does not exist yet — a single early lookup finds nothing listening
// at the standalone fallback and then holds that address for the life of the
// process, leaving every page a 500 until the binary restarts.
//
// Resolving per render closes it. gonertia takes its SSR URL as a string at
// construction and we do not control that, but the HTTP client is ours, so the
// address is decided in the transport where it can still change.
//
// In production there is no hot file, so this resolves to the configured
// standalone URL every time and costs one cached stat.
type ssrRedirector struct {
	base http.RoundTripper

	// standalone and vite are the per-render budgets. Zero means the
	// package default for that renderer; see budgetFor.
	standalone time.Duration
	vite       time.Duration

	// pinned stops the hot file being consulted, because the caller named a
	// renderer and a discovered one must not override a chosen one.
	pinned bool

	mu       sync.Mutex
	resolved string
	at       time.Time
}

func newSSRTransport(base http.RoundTripper) *ssrRedirector {
	if base == nil {
		base = http.DefaultTransport
	}
	return &ssrRedirector{base: base}
}

// withBudget pins both renderers to one caller-chosen budget, because a
// caller who names a number has overridden the question the defaults answer.
func (s *ssrRedirector) withBudget(d time.Duration) *ssrRedirector {
	if d > 0 {
		s.standalone, s.vite = d, d
	}
	return s
}

// pin stops this transport retargeting, leaving every render to go wherever
// gonertia was configured to send it.
//
// For the development setup that keeps rendering out of the reload loop: the
// standalone renderer is named explicitly while a Vite dev server is also
// running and publishing ./public/hot, and auto-detection would otherwise
// take the render straight back to Vite. See EnvSSRURL.
func (s *ssrRedirector) pin() *ssrRedirector {
	s.pinned = true
	return s
}

func (s *ssrRedirector) budgetFor(toVite bool) time.Duration {
	if toVite {
		if s.vite > 0 {
			return s.vite
		}
		return viteSSRTimeout
	}
	if s.standalone > 0 {
		return s.standalone
	}
	return defaultSSRTimeout
}

func (s *ssrRedirector) RoundTrip(req *http.Request) (*http.Response, error) {
	outgoing, toVite := s.retarget(req)
	budget := s.budgetFor(toVite)

	// The deadline lives here rather than on the client, because only this
	// far down is it known which renderer the request is about to reach, and
	// the two want budgets an order of magnitude apart. A client-level
	// Timeout would have to be the larger of the two and so could not bound
	// the smaller.
	ctx, cancel := context.WithTimeout(outgoing.Context(), budget)

	if !toVite {
		resp, err := s.base.RoundTrip(outgoing.Clone(ctx))
		if err != nil {
			cancel()
			return nil, err
		}
		// The deadline has to outlive this call, because the body is read by
		// the caller afterwards. Cancelling on Close is what keeps the
		// context from leaking without cutting the read short.
		resp.Body = cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
		return resp, nil
	}

	defer cancel()
	return s.renderThroughVite(ctx, outgoing, budget)
}

// renderThroughVite posts to the Vite dev endpoint, waiting out a warm-up
// rather than reporting one as a failed render.
//
// @inertiajs/vite loads the SSR entry when the dev server starts listening,
// and until that finishes it answers every render with 200 and a body of
// `null` — deliberately, as a "not yet", logged on its side as "SSR skipped,
// module graph is still warming up". Returned as-is that becomes a document
// with no markup, which the SSR guard correctly calls a fallback and, in
// strict mode, a 500. So the first page opened after the dev server starts
// errors through nobody's fault, and on a large page graph the transform
// takes tens of seconds, which is long enough for it to look permanent.
//
// Asking again after a beat is the whole fix: the answer is explicitly
// "later", so later is when to ask.
func (s *ssrRedirector) renderThroughVite(ctx context.Context, req *http.Request, budget time.Duration) (*http.Response, error) {
	// Buffered because a retry needs the payload a second time, and the body
	// handed to a RoundTripper can only be read once. A page payload is
	// small — it is the props that were about to be rendered.
	payload, err := drainBody(req)
	if err != nil {
		return nil, err
	}

	waited := false
	for {
		attempt := req.Clone(ctx)
		attempt.Body = io.NopCloser(bytes.NewReader(payload))
		attempt.ContentLength = int64(len(payload))
		attempt.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(payload)), nil
		}

		resp, err := s.base.RoundTrip(attempt)
		if err != nil {
			return nil, err
		}

		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		resp.Body = io.NopCloser(bytes.NewReader(body))
		resp.ContentLength = int64(len(body))

		if !ssrWarmingUp(resp.StatusCode, body) {
			if waited {
				slog.Info("inertia: the Vite dev server finished warming up and the page rendered")
			}
			return resp, nil
		}

		if !waited {
			// Once per render rather than once per attempt, so a long
			// transform is one line and not forty.
			slog.Info("inertia: the Vite dev server is still transforming the SSR "+
				"module graph, so this render is waiting for it", "budget", budget)
			waited = true
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("inertia: the Vite dev server was still warming up after %s: %w", budget, ctx.Err())
		case <-time.After(viteWarmupPoll):
		}
	}
}

// ssrWarmingUp reports whether an answer means "not ready" rather than
// "rendered".
//
// Exactly `null`, and nothing looser. That is the literal body
// @inertiajs/vite sends while its module graph loads, so it is a deliberate
// "ask again" and safe to act on.
//
// An empty body is *not* included, though it is equally unrenderable: a
// renderer that answers nothing is broken rather than busy, and waiting a
// minute to say so would turn a clear failure into a slow one. A stub
// renderer answering 200 with no body is a real shape — one of this
// package's own tests is written that way — and treating it as a warm-up
// hung that test for the full budget.
func ssrWarmingUp(status int, body []byte) bool {
	if status != http.StatusOK {
		return false
	}
	return bytes.Equal(bytes.TrimSpace(body), []byte("null"))
}

// retarget points the request at the renderer's current address, and reports
// whether that address is the Vite dev endpoint.
func (s *ssrRedirector) retarget(req *http.Request) (*http.Request, bool) {
	// A shallow copy, because a RoundTripper must not modify the request it
	// is given — the same *http.Request may be retried or inspected by the
	// caller afterwards.
	clone := req.Clone(req.Context())

	target := s.current()
	if target == "" {
		return clone, isViteSSRPath(clone.URL.Path)
	}

	parsed, err := url.Parse(target)
	if err != nil {
		// An unreadable hot file is not a reason to fail the render: let it
		// go to whatever gonertia was configured with, and let the SSR guard
		// report the degrade if that address is dead too.
		return clone, isViteSSRPath(clone.URL.Path)
	}

	clone.URL.Scheme = parsed.Scheme
	clone.URL.Host = parsed.Host
	clone.Host = parsed.Host
	// Only when the resolved address carries a path of its own. The standalone
	// renderer is bare — "http://127.0.0.1:13714" parses with an empty Path —
	// so assigning it unconditionally replaced gonertia's "/render" with "",
	// and every render in production reached the renderer's root and failed.
	// The Vite dev server is the opposite case: its address *is* the endpoint
	// ("…:5173/__inertia_ssr"), so there the path has to win.
	if parsed.Path != "" && parsed.Path != "/" {
		clone.URL.Path = parsed.Path
	}
	return clone, isViteSSRPath(clone.URL.Path)
}

func isViteSSRPath(p string) bool {
	return p == viteSSRPath || strings.HasPrefix(p, viteSSRPath+"/")
}

func drainBody(req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}
	defer req.Body.Close()
	return io.ReadAll(req.Body)
}

// cancelOnClose releases a request's context once its body is closed.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

// current returns the renderer address, re-reading the hot file at most once
// per ssrResolveTTL.
func (s *ssrRedirector) current() string {
	if s.pinned {
		return ""
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.resolved != "" && time.Since(s.at) < ssrResolveTTL {
		return s.resolved
	}
	s.resolved = detectSSRURL()
	s.at = time.Now()
	return s.resolved
}
