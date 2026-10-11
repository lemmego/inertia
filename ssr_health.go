package inertia

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
)

// ssrFailures counts server-side renders that fell back to client rendering.
//
// Counting failures rather than successes is a consequence of where the seam
// is: gonertia's render path is internal, and the one thing it exposes is a
// log line on failure. That turns out to be the signal worth having anyway —
// a failed render is always a problem, whereas a count of successes only
// means something next to a total.
//
// It matters because the failure is invisible at the HTTP layer. gonertia
// falls back to the client-rendered container, so the response is 200 with an
// empty root element: correct-looking to a person, empty to a crawler. A
// status-code alert will never fire. This will.
var ssrFailures atomic.Uint64

// SSRFailures returns how many server-side renders have fallen back since the
// process started.
//
// Expose it on a readiness endpoint and alert on the rate. A non-zero and
// rising count means every page is being served without server-rendered
// markup, and nothing else in the stack will tell you.
func SSRFailures() uint64 { return ssrFailures.Load() }

// noteSSRFailure is called from the logger bridge, which is where gonertia
// reports a failed render.
func noteSSRFailure() { ssrFailures.Add(1) }

// ProbeComponent is the component name a shallow probe asks for.
//
// Deliberately one the bundle cannot have. See CheckSSR.
const ProbeComponent = "__inertia_probe__"

// SSRHealth is what a readiness check reports.
type SSRHealth struct {
	// Enabled is whether this process is configured to server-render at all.
	Enabled bool
	// URL is the render endpoint.
	URL string
	// OK is whether the render endpoint answered a probe.
	OK bool
	// Deep is whether the probe rendered a real page and got server-rendered
	// markup back, rather than only establishing that the renderer is alive.
	// See CheckSSR for the difference and why it matters.
	Deep bool
	// Component is what the probe asked the renderer for.
	Component string
	// Status is the render endpoint's HTTP status.
	Status int
	// Failures is the process's cumulative fallback count.
	Failures uint64
	// Err describes why a probe failed.
	Err string
}

// CheckSSR probes the render endpoint.
//
// It POSTs a page and reads the answer, rather than dialling the port. A Node
// process listening with a broken module graph passes a TCP check and fails
// every render, which is precisely the state this exists to catch — and the
// CLI's own health check dials the port, so it cannot see it.
//
// There are two levels, and the difference is worth understanding before
// trusting the result.
//
// **Shallow**, the default. The probe asks for ProbeComponent, a name the
// bundle cannot have, so the renderer is *expected* to refuse it — and a
// refusal with a body is the signal: the process is up, the module graph
// loaded, and the bundle's router ran far enough to decide the component does
// not exist. The status is not required to be 200, because a correct renderer
// answering this question says 500.
//
// That is a real check and a shallow one. It cannot distinguish a renderer
// that fails only on unknown components from one that fails on all of them,
// which is the condition that matters most.
//
// **Deep**, when the application sets WithSSRProbeComponent. The probe asks
// for a real page and requires 200 and server-rendered markup back, which
// proves the thing an operator actually wants to know: this renderer produces
// documents. It needs a component that renders with no props — a page built
// for the purpose — because a real page given empty props throws, and a probe
// that fails against a healthy renderer is worse than no probe.
//
// The first version of this checked the status before the body and so
// reported every healthy development renderer as unusable, because the
// unknown probe component legitimately answers 500. The comment said a
// refusal with a body was fine; the code disagreed with the comment.
func (i *Inertia) CheckSSR(ctx context.Context) SSRHealth {
	component := ProbeComponent
	deep := false
	if configured := strings.TrimSpace(i.cfg.SSRProbeComponent); configured != "" {
		component = configured
		deep = true
	}

	health := SSRHealth{
		Enabled:   i.cfg.SSREnabled,
		URL:       i.cfg.SSRURL,
		Component: component,
		Deep:      deep,
		Failures:  SSRFailures(),
	}
	if !health.Enabled {
		return health
	}
	if health.URL == "" {
		health.Err = "server-side rendering is enabled but no render URL could be resolved"
		return health
	}

	client := &http.Client{Timeout: defaultSSRTimeout}
	if configured, ok := i.cfg.SSRHTTPClient.(*http.Client); ok && configured != nil {
		client = configured
	}

	probe := map[string]any{
		"component":      component,
		"props":          map[string]any{},
		"url":            "/",
		"version":        "",
		"encryptHistory": false,
		"clearHistory":   false,
	}
	body, err := json.Marshal(probe)
	if err != nil {
		health.Err = err.Error()
		return health
	}

	endpoint := strings.TrimSuffix(health.URL, "/render") + "/render"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		health.Err = err.Error()
		return health
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := client.Do(request)
	if err != nil {
		health.Err = err.Error()
		return health
	}
	defer response.Body.Close()

	answer, err := io.ReadAll(io.LimitReader(response.Body, 1<<16))
	if err != nil {
		health.Err = err.Error()
		return health
	}
	health.Status = response.StatusCode

	// An empty body means the bundle never loaded, at either level. This is
	// the TCP check's blind spot and the reason this function exists.
	if len(bytes.TrimSpace(answer)) == 0 {
		health.Err = "render endpoint answered with an empty body, so the SSR bundle is not loaded"
		return health
	}

	// A 404 is the endpoint itself being wrong — a renderer at another path,
	// or a dev server that is not serving the SSR route — rather than a
	// renderer refusing a component. Worth separating, because the fix is
	// different: configuration rather than a broken bundle.
	if response.StatusCode == http.StatusNotFound {
		health.Err = fmt.Sprintf("there is no render endpoint at %s", endpoint)
		return health
	}

	if !health.Deep {
		// Shallow: the renderer answered about a component it cannot have,
		// which means it is up and executing. A 500 here is the correct
		// answer to the question asked.
		health.OK = true
		return health
	}

	if response.StatusCode != http.StatusOK {
		health.Err = fmt.Sprintf("rendering %s answered %d: %s",
			component, response.StatusCode, firstLine(answer))
		return health
	}
	// Deep: and it produced server-rendered markup. A 200 holding a document
	// with an empty container is the silent fallback, which is the whole
	// failure mode being probed for.
	if !renderedMarkup(answer) {
		health.Err = fmt.Sprintf(
			"rendering %s answered 200 with no server-rendered markup, so every page falls back to client rendering",
			component)
		return health
	}

	health.OK = true
	return health
}

// firstLine trims a renderer's error payload to something a health response
// can carry without embedding a stack trace in a monitoring page.
func firstLine(payload []byte) string {
	text := strings.TrimSpace(string(payload))
	if index := strings.IndexAny(text, "\r\n"); index >= 0 {
		text = text[:index]
	}
	if len(text) > 200 {
		text = text[:200] + "…"
	}
	return text
}

// ErrHotFileInProduction is returned when a production process finds a Vite
// hot file.
//
// It is the worst silent failure in the stack. detectSSRURL reads the hot
// file and points the renderer at a dev server that is not running, and
// viteTags points every asset at it too — so a production deploy serves a
// page whose CSS and JS 404 and whose markup is never server-rendered, with
// nothing in the logs about why. A stale ./public/hot committed or copied
// into an image is an easy mistake and a hard one to diagnose, so it is
// refused at boot instead.
var ErrHotFileInProduction = fmt.Errorf(
	"inertia: %s exists in production; it points assets and server-side rendering at a Vite dev server. "+
		"Delete it — `lemmego build` does, and it belongs in .gitignore", ViteHotPath)

// guardHotFile refuses to boot a production process with a hot file present.
func guardHotFile(inProduction bool) error {
	if !inProduction {
		return nil
	}
	if info, err := os.Stat(ViteHotPath); err == nil && !info.IsDir() {
		return ErrHotFileInProduction
	}
	return nil
}

// renderedMarkup reports whether an SSR response carries server-rendered
// markup.
//
// The renderer answers JSON — {"body": "<div …>", "head": […]} — so the
// marker arrives inside a JSON string with its quotes escaped, and scanning
// the raw bytes for `data-server-rendered="true"` never matches. Decoding
// first is the difference between a probe that works and one that reports
// every healthy renderer as broken; a test caught this, which is the only
// reason it is not in the shipped code.
//
// A payload that is not that shape falls back to a raw scan, so a renderer
// that answers HTML directly is still understood.
func renderedMarkup(payload []byte) bool {
	var page struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(payload, &page); err == nil && page.Body != "" {
		return strings.Contains(page.Body, ServerRenderedMarker)
	}
	return bytes.Contains(payload, []byte(ServerRenderedMarker))
}
