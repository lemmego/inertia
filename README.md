# lemmego/inertia

[Inertia.js](https://inertiajs.com) for Lemmego applications: your routing,
validation and data loading stay in Go, and a React or Vue component renders
the page. No separate API to build.

A handler returns a component name and some props; Inertia decides whether the
browser gets a full HTML document or just JSON for a client-side visit.

```go
import "github.com/lemmego/inertia"

func Show(c app.Context) error {
    return inertia.Respond(c, "Posts/Show", map[string]any{
        "post": post,
    })
}
```

## Install

```bash
go get github.com/lemmego/inertia
```

## Register it

```go
// bootstrap/providers.go
&inertia.Provider{
    Options: []inertia.Option{
        inertia.WithSSR(),
    },
},
```

`RootTemplate` defaults to `resources/views/root.html`, which is where
`lemmego new` puts it.

## Responding

| Call | What it does |
|---|---|
| `inertia.Respond(c, "Posts/Show", props)` | render a component |
| `inertia.Redirect(c, "/posts")` | redirect, with the status Inertia expects |
| `inertia.Back(c)` | back to the referring page, for a failed form |
| `inertia.Location(c, url)` | leave the SPA entirely — an external URL, or a sign-in page on another host |

`Redirect` answers **303** for a `PUT`, `PATCH` or `DELETE`, because a browser
following a 302 after one of those re-issues the original method. If you assert
on the status in a test, assert 303 there.

## Props

Everything in the map is sent on the first load. These wrappers change that:

| Wrapper | Sent |
|---|---|
| `inertia.Optional(v)` | only when the client asks for it by name |
| `inertia.Defer(v)` | after the page renders, in a second request |
| `inertia.Always(v)` | on every response, including partial reloads |
| `inertia.Once(v)` | on the initial load only |
| `inertia.Merge(v)` / `DeepMerge(v)` | merged into the existing prop instead of replacing it — for an infinite list |
| `inertia.Scroll(v, …)` | a paginated prop the client appends to while scrolling |

`Defer` is the one to reach for when a page has one slow query and five fast
ones. `Optional` is for data a page needs only sometimes, so a partial reload
can ask for it by name.

### Shared props, and the ordering trap

```go
inertia.Get(a).ShareProp("appName", "Acme")
```

A shared prop is **evaluated when you share it**, so anything that depends on
the current request has to be a closure:

```go
inertia.SetProp(c.RequestContext(), "viewer", func() any {
    user, _ := auth.UserAs[*models.User](c)
    return viewerProps(user)
})
```

This matters more than it looks. Middleware is often prepended ahead of a
route's own `auth.OptionalAuth`, so a shell or layout middleware that reads the
user eagerly gets the signed-*out* answer on a signed-in request — silently,
because a page that renders is a page nobody investigates. A closure is
resolved after your auth middleware has run.

### Flash and validation errors

```go
ctx := inertia.SetValidationErrors(c.RequestContext(), errs)
ctx = inertia.SetFlashValue(ctx, "status", "Saved.")
```

Both ride the request context and survive the redirect, so a failed form
re-renders with the messages attached.

## Vite

```go
inertia.WithVite(
    inertia.WithEntryPoints("resources/js/app.tsx"),
    inertia.WithBuildDir("public/build"),
)
```

In development the asset URLs come from `./public/hot`, which the Vite dev
server writes; in production they come from the build manifest.

**A hot file in production is an error, not a fallback.** You get
`ErrHotFileInProduction` at boot, because the alternative is every asset URL
on your live site pointing at `localhost:5173` — a site that looks deployed
and serves nothing.

Preloading is configurable: `WithoutPreloading`, `WithWaterfallPreload(n)`,
`WithAggressivePreload`, and `WithIntegrity` for subresource integrity hashes.

## Server-side rendering

`WithSSR()` renders the page in Node before sending it, so a crawler, a link
preview and a reader with JavaScript disabled all get real markup.

```go
inertia.WithSSR()                           // resolve the renderer automatically
inertia.WithSSRAt("http://127.0.0.1:13714") // or point at it
inertia.WithoutSSR()                        // off, said out loud
```

**`INERTIA_SSR` overrides all of them**, so you can turn rendering off for an
afternoon without editing `LoadProviders` and remembering to put it back:

```bash
INERTIA_SSR=false   # off, whatever the code says
INERTIA_SSR=true    # on — for checking a page really does render server-side
INERTIA_SSR=        # unset: the application decides
```

An unset or empty value means "as configured", and a value that is not a
boolean means the same plus a warning — it will not take your process down.

In development the renderer *is* the Vite dev server — `@inertiajs/vite`
serves `/__inertia_ssr` — so you do not start a separate process. In
production, `lemmego inertia-ssr` runs the built bundle.

A renderer that has only just started answers **200 with the body `null`**
for a second or two, because Vite transforms the SSR entry lazily. So the
first page you open after `lemmego dev` comes up has no server-rendered
marker through no fault of its own. A document render is retried once before
it counts as a fallback, which turns that into one slow page rather than a
500 — strict mode only, since outside it the fallback is already going out.

### A failed render is the failure you have to go looking for

Inertia's own behaviour on an SSR error is to fall back to client rendering.
The response is **200 and looks perfect to a person**, while a crawler
receives an empty `<div id="app">` — and every meta tag rendered by your page
component disappears with it.

So this package counts and reports it:

```go
inertia.SSRFailures()   // cumulative fallbacks in this process
```

Outside production a failed render answers **500 with the reason** instead,
which is `StrictSSR` and is on by default. Turn it off with
`WithStrictSSR(false)` if you need the degraded page locally.

Two things follow for your page components:

- **Never touch a browser global during render.** `window`, `document` and
  `localStorage` do not exist in the renderer, and reading
  `window.location.search` while rendering throws. Put it in an effect, which
  never runs in Node.
- **Render your `<title>` and meta tags in Go**, not in the page component.
  On the fallback path the component's head output is gone, which is exactly
  the failure you cannot see. [`lemmego/seo`](https://github.com/lemmego/seo)
  does this. Inertia's `<Head title>` would then emit a *second* `<title>`, so
  use a client-only effect if you want the tab title to update during a
  client-side visit.

### Health checks

```go
health := inertia.Get(a).CheckSSR(ctx)
```

`SSRHealth` has two levels. A **shallow** probe establishes that the renderer
is alive. A **deep** one renders a real page and requires server-rendered
markup back:

```go
inertia.WithSSRProbeComponent("Health/Probe")
```

A deep probe needs a component that takes **no props**, because a real page
handed empty props throws — and a probe that fails against a healthy renderer
is worse than no probe. Add a page that exists for nothing else.

Register the check as non-critical unless your site is useless without SSR: a
readiness probe failing on the renderer pulls every instance out of rotation
for a site that still serves pages.

## Testing

`Assertable` reads a response body as an Inertia page, so you assert on props
rather than on rendered HTML:

```go
page := inertia.AssertFromBytes(t, resp.Body.Bytes())
page.AssertComponent("Posts/Show")
page.AssertProps(map[string]any{"post": …})
```

Assert on props and not on strings in the markup. A substring check against
rendered React is fragile — static text beside an interpolated value comes out
as `Continue with <!-- -->GitHub`, because React inserts a comment marker — and
a check against the whole document passes for any query whose text appears
anywhere in it.

[`lemmego/apptest`](https://github.com/lemmego/apptest) wires this up for you,
including the asset version, without which every Inertia test gets a **409**.

```bash
go test ./...
GOWORK=off go test ./...   # as a published module
```
