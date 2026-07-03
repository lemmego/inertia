package inertia

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/lemmego/api/app"
	"github.com/romsar/gonertia/v3"
)

const ViteHotPath = "./public/hot"
const InertiaRootTemplatePath = "resources/views/root.html"
const InertiaManifestPath = "./public/build/manifest.json"
const InertiaBuildPath = "/public/build/"

// ---------------------------------------------------------------------------
// Lemmego-owned types (no gonertia leak)
// ---------------------------------------------------------------------------

// ValidationErrors holds validation error messages keyed by field name.
type ValidationErrors map[string]any

// Flash contains arbitrary one-time data exposed as page.flash.
type Flash map[string]any

// FlashProvider defines the contract for persisting validation errors and flash
// data across redirects. Implementations store values keyed by session ID.
type FlashProvider interface {
	FlashErrors(ctx context.Context, errors ValidationErrors) error
	GetErrors(ctx context.Context) (ValidationErrors, error)
	Flash(ctx context.Context, flash Flash) error
	GetFlash(ctx context.Context) (Flash, error)
	ShouldClearHistory(ctx context.Context) (bool, error)
	FlashClearHistory(ctx context.Context) error
}

// ---------------------------------------------------------------------------
// Config — internal configuration model
// ---------------------------------------------------------------------------

// Config captures all user-facing inertia options before being translated
// into gonertia options internally.
type Config struct {
	Version         string
	VersionFromFile string
	VersionFS       any  // io/fs.FS
	VersionFSPath   string
	SSRURL          string
	SSRHTTPClient   any  // *http.Client
	FlashProvider   FlashProvider
	ContainerID     string
	EncryptHistory  bool
	JSONMarshaller  JSONMarshaller
	Logger          Logger
	ViteEnabled     bool
	ViteCfg         *ViteConfig
}

// ---------------------------------------------------------------------------
// Option interface + concrete option types
// ---------------------------------------------------------------------------

// Option configures the Inertia instance. Users pass these to NewInertia
// (or Provider). The implementation never exposes gonertia to the caller.
type Option interface {
	Apply(*Config)
}

type withVersion string

func (v withVersion) Apply(c *Config) { c.Version = string(v) }

// WithVersion sets the asset version by an arbitrary string.
func WithVersion(v string) Option { return withVersion(v) }

type withVersionFromFile string

func (v withVersionFromFile) Apply(c *Config) { c.VersionFromFile = string(v) }

// WithVersionFromFile sets the asset version from a file's checksum.
func WithVersionFromFile(path string) Option { return withVersionFromFile(path) }

type withSSR struct{ url string }

func (s withSSR) Apply(c *Config) { c.SSRURL = s.url }

// WithSSR enables server-side rendering at the default URL
// (http://127.0.0.1:13714).
func WithSSR() Option { return withSSR{url: "http://127.0.0.1:13714"} }

// WithSSRAt enables server-side rendering at a custom URL.
func WithSSRAt(url string) Option { return withSSR{url: url} }

type withFlashProvider struct{ p FlashProvider }

func (f withFlashProvider) Apply(c *Config) { c.FlashProvider = f.p }

// WithFlashProvider sets a custom flash data provider.
func WithFlashProvider(p FlashProvider) Option { return withFlashProvider{p: p} }

type withContainerID string

func (id withContainerID) Apply(c *Config) { c.ContainerID = string(id) }

// WithContainerID sets the root container element id (default "app").
func WithContainerID(id string) Option { return withContainerID(id) }

type withEncryptHistory bool

func (e withEncryptHistory) Apply(c *Config) { c.EncryptHistory = bool(e) }

// WithEncryptHistory enables global history encryption.
func WithEncryptHistory() Option { return withEncryptHistory(true) }

type withVersionFromFileFS struct {
	fs   any
	path string
}

func (v withVersionFromFileFS) Apply(c *Config) {
	c.VersionFS = v.fs
	c.VersionFSPath = v.path
}

// WithVersionFromFileFS sets the asset version from a file checksum using an
// embedded filesystem (e.g. embed.FS).
func WithVersionFromFileFS(fs any, path string) Option {
	return withVersionFromFileFS{fs: fs, path: path}
}

type withJSONMarshaller struct{ m JSONMarshaller }

func (j withJSONMarshaller) Apply(c *Config) { c.JSONMarshaller = j.m }

// WithJSONMarshaller sets a custom JSON marshaller for Inertia page data.
func WithJSONMarshaller(m JSONMarshaller) Option { return withJSONMarshaller{m: m} }

type withLogger struct{ l Logger }

func (lg withLogger) Apply(c *Config) { c.Logger = lg.l }

// WithLogger sets a custom logger for the Inertia instance.
func WithLogger(l Logger) Option { return withLogger{l: l} }

type withSSRHTTPClient struct{ client *http.Client }

func (s withSSRHTTPClient) Apply(c *Config) { c.SSRHTTPClient = s.client }

// WithSSRHTTPClient sets a custom HTTP client for SSR requests.
func WithSSRHTTPClient(client *http.Client) Option { return withSSRHTTPClient{client: client} }

type withVite struct{ opts []ViteOption }

func (v withVite) Apply(c *Config) {
	c.ViteEnabled = true
	cfg := defaultViteConfig()
	for _, opt := range v.opts {
		opt(&cfg)
	}
	c.ViteCfg = &cfg
}

// WithVite enables the full Vite integration with asset preloading, template
// helpers (viteAssets, viteRefresh, viteReactRefresh), and optional CSP support.
// When not used, a basic Vite setup (hot file + manifest resolution) is applied.
func WithVite(opts ...ViteOption) Option { return withVite{opts: opts} }

// ---------------------------------------------------------------------------
// Provider — registers inertia into the app service container
// ---------------------------------------------------------------------------

// Provider implements app.Provider and bootstraps the Inertia instance.
type Provider struct {
	// RootTemplate is the path to the root HTML template file.
	// Defaults to InertiaRootTemplatePath if empty.
	RootTemplate string
	// Options applied during initialization.
	Options []Option
}

func (p *Provider) Provide(a app.App) error {
	root := p.RootTemplate
	if root == "" {
		root = InertiaRootTemplatePath
	}

	opts := make([]Option, 0, len(p.Options)+2)
	opts = append(opts,
		WithVersionFromFile(InertiaManifestPath),
		WithFlashProvider(NewFlash()),
	)
	opts = append(opts, p.Options...)

	inertia := NewInertia(a, root, opts...)
	a.AddService(inertia)
	return nil
}

// ---------------------------------------------------------------------------
// Flash — in-memory flash provider
// ---------------------------------------------------------------------------

// Flash implements FlashProvider using in-memory maps keyed by session ID.
type flashStore struct {
	errors       map[string]ValidationErrors
	flash        map[string]Flash
	clearHistory map[string]bool
}

// NewFlash creates a new in-memory flash provider.
func NewFlash() FlashProvider {
	return &flashStore{
		errors:       make(map[string]ValidationErrors),
		flash:        make(map[string]Flash),
		clearHistory: make(map[string]bool),
	}
}

func (p *flashStore) FlashErrors(ctx context.Context, errors ValidationErrors) error {
	if sessionID, ok := ctx.Value("sessionID").(string); ok {
		p.errors[sessionID] = errors
	}
	return nil
}

func (p *flashStore) GetErrors(ctx context.Context) (ValidationErrors, error) {
	var result ValidationErrors
	if sessionID, ok := ctx.Value("sessionID").(string); ok {
		result = p.errors[sessionID]
		p.errors[sessionID] = nil
	}
	return result, nil
}

func (p *flashStore) ShouldClearHistory(ctx context.Context) (bool, error) {
	if sessionID, ok := ctx.Value("sessionID").(string); ok {
		clearHistory := p.clearHistory[sessionID]
		delete(p.clearHistory, sessionID)
		return clearHistory, nil
	}
	return false, nil
}

func (p *flashStore) FlashClearHistory(ctx context.Context) error {
	if sessionID, ok := ctx.Value("sessionID").(string); ok {
		p.clearHistory[sessionID] = true
	}
	return nil
}

func (p *flashStore) Flash(ctx context.Context, flash Flash) error {
	if sessionID, ok := ctx.Value("sessionID").(string); ok {
		p.flash[sessionID] = flash
	}
	return nil
}

func (p *flashStore) GetFlash(ctx context.Context) (Flash, error) {
	if sessionID, ok := ctx.Value("sessionID").(string); ok {
		flash := p.flash[sessionID]
		delete(p.flash, sessionID)
		return flash, nil
	}
	return Flash{}, errors.New("sessionID missing for inertia")
}

// ---------------------------------------------------------------------------
// gonertia bridge — adapts our FlashProvider to gonertia.FlashProvider
// ---------------------------------------------------------------------------

type gonertiaFlashBridge struct {
	provider FlashProvider
}

func (b *gonertiaFlashBridge) FlashErrors(ctx context.Context, errs gonertia.ValidationErrors) error {
	return b.provider.FlashErrors(ctx, ValidationErrors(errs))
}

func (b *gonertiaFlashBridge) GetErrors(ctx context.Context) (gonertia.ValidationErrors, error) {
	errs, err := b.provider.GetErrors(ctx)
	return gonertia.ValidationErrors(errs), err
}

func (b *gonertiaFlashBridge) Flash(ctx context.Context, f gonertia.Flash) error {
	return b.provider.Flash(ctx, Flash(f))
}

func (b *gonertiaFlashBridge) GetFlash(ctx context.Context) (gonertia.Flash, error) {
	f, err := b.provider.GetFlash(ctx)
	return gonertia.Flash(f), err
}

func (b *gonertiaFlashBridge) ShouldClearHistory(ctx context.Context) (bool, error) {
	return b.provider.ShouldClearHistory(ctx)
}

func (b *gonertiaFlashBridge) FlashClearHistory(ctx context.Context) error {
	return b.provider.FlashClearHistory(ctx)
}

// ---------------------------------------------------------------------------
// gonertia bridge — JSON marshaller
// ---------------------------------------------------------------------------

// JSONMarshaller defines the interface for custom JSON serialization used
// internally by the Inertia page rendering pipeline.
type JSONMarshaller interface {
	Decode(r io.Reader, v any) error
	Marshal(v any) ([]byte, error)
}

type gonertiaJSONMarshallerBridge struct {
	m JSONMarshaller
}

func (b *gonertiaJSONMarshallerBridge) Decode(r io.Reader, v any) error {
	return b.m.Decode(r, v)
}

func (b *gonertiaJSONMarshallerBridge) Marshal(v any) ([]byte, error) {
	return b.m.Marshal(v)
}

// ---------------------------------------------------------------------------
// gonertia bridge — Logger
// ---------------------------------------------------------------------------

// Logger defines the interface for logging within the Inertia package.
type Logger interface {
	Printf(format string, v ...any)
	Println(v ...any)
}

type gonertiaLoggerBridge struct {
	l Logger
}

func (b *gonertiaLoggerBridge) Printf(format string, v ...any) {
	b.l.Printf(format, v...)
}

func (b *gonertiaLoggerBridge) Println(v ...any) {
	b.l.Println(v...)
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

// IsInertiaRequest returns true if the request was made by the Inertia client.
func IsInertiaRequest(r *http.Request) bool {
	return gonertia.IsInertiaRequest(r)
}

// ---------------------------------------------------------------------------
// Inertia — the framework-facing wrapper
// ---------------------------------------------------------------------------

// Inertia wraps gonertia.Inertia so that the rest of the framework never
// imports gonertia directly.
type Inertia struct {
	inner *gonertia.Inertia
}

// InertiaResponse implements the Renderer interface so it can be passed to
// app.Context.Render().
type InertiaResponse struct {
	*Inertia
	filePath string
	gProps   gonertia.Props
	ctx      app.Context
}

// ---------------------------------------------------------------------------
// Constructor — the translation layer
// ---------------------------------------------------------------------------

// NewInertia creates a new Inertia instance. User-supplied Options are
// collected into a Config, then translated into gonertia options internally.
func NewInertia(a app.App, rootTemplatePath string, opts ...Option) *Inertia {
	cfg := &Config{
		ContainerID: "app",
	}
	for _, opt := range opts {
		opt.Apply(cfg)
	}

	// Translate Config → gonertia.Option
	var gOpts []gonertia.Option

	if cfg.Version != "" {
		gOpts = append(gOpts, gonertia.WithVersion(cfg.Version))
	}
	if cfg.VersionFromFile != "" {
		gOpts = append(gOpts, gonertia.WithVersionFromFile(cfg.VersionFromFile))
	}
	if cfg.VersionFS != nil && cfg.VersionFSPath != "" {
		rootFS := cfg.VersionFS.(fs.FS)
		gOpts = append(gOpts, gonertia.WithVersionFromFileFS(rootFS, cfg.VersionFSPath))
	}
	if cfg.SSRURL != "" {
		gOpts = append(gOpts, gonertia.WithSSR(cfg.SSRURL))
	}
	if cfg.SSRHTTPClient != nil {
		gOpts = append(gOpts, gonertia.WithSSRHTTPClient(cfg.SSRHTTPClient.(*http.Client)))
	}
	if cfg.ContainerID != "" && cfg.ContainerID != "app" {
		gOpts = append(gOpts, gonertia.WithContainerID(cfg.ContainerID))
	}
	if cfg.EncryptHistory {
		gOpts = append(gOpts, gonertia.WithEncryptHistory(true))
	}
	if cfg.JSONMarshaller != nil {
		gOpts = append(gOpts, gonertia.WithJSONMarshaller(
			&gonertiaJSONMarshallerBridge{m: cfg.JSONMarshaller},
		))
	}
	if cfg.Logger != nil {
		gOpts = append(gOpts, gonertia.WithLogger(
			&gonertiaLoggerBridge{l: cfg.Logger},
		))
	}
	if cfg.FlashProvider != nil {
		gOpts = append(gOpts, gonertia.WithFlashProvider(
			&gonertiaFlashBridge{provider: cfg.FlashProvider},
		))
	}

	gi, err := gonertia.NewFromFile(rootTemplatePath, gOpts...)
	if err != nil {
		log.Fatal(err)
	}

	// Vite integration
	if cfg.ViteEnabled {
		setupVite(gi, cfg.ViteCfg)
	} else {
		setupLegacyVite(gi)
	}

	gi.ShareTemplateData("env", a.Config().Get("app.env"))

	return &Inertia{inner: gi}
}

// ---------------------------------------------------------------------------
// Vite setup
// ---------------------------------------------------------------------------

func setupVite(gi *gonertia.Inertia, cfg *ViteConfig) {
	viteOpts := viteConfigToGonertiaOpts(cfg)
	if cfg.EmbedFS != nil {
		_, err := gonertia.NewViteFromFS(gi, cfg.EmbedFS.(fs.FS), viteOpts...)
		if err != nil {
			log.Fatalf("vite setup from embed.FS: %s", err)
		}
	} else {
		_, err := gonertia.NewVite(gi, viteOpts...)
		if err != nil {
			log.Fatalf("vite setup: %s", err)
		}
	}
}

func setupLegacyVite(gi *gonertia.Inertia) {
	_, err := os.Stat(ViteHotPath)
	if err == nil {
		gi.ShareTemplateFunc("vite", func(entry string) (string, error) {
			url := readViteHotURL(ViteHotPath)
			if url == "" {
				url = probeVitePort(5173)
			}
			if entry != "" && !strings.HasPrefix(entry, "/") {
				entry = "/" + entry
			}
			return url + entry, nil
		})
	} else {
		gi.ShareTemplateFunc("vite", Vite(InertiaManifestPath, InertiaBuildPath))
	}
}

func viteConfigToGonertiaOpts(vc *ViteConfig) []gonertia.ViteOption {
	var opts []gonertia.ViteOption
	if vc.HotFile != "" {
		opts = append(opts, gonertia.WithHotFile(vc.HotFile))
	}
	if vc.BuildManifest != "" {
		opts = append(opts, gonertia.WithBuildManifest(vc.BuildManifest))
	}
	if vc.FallbackManifest != "" {
		opts = append(opts, gonertia.WithFallbackManifest(vc.FallbackManifest))
	}
	if vc.BuildDir != "" {
		opts = append(opts, gonertia.WithBuildDir(vc.BuildDir))
	}
	if vc.HotReloadPort != "" {
		opts = append(opts, gonertia.WithHotReloadPort(vc.HotReloadPort))
	}
	if len(vc.EntryPoints) > 0 {
		opts = append(opts, gonertia.WithEntryPoints(vc.EntryPoints...))
	}
	if vc.IntegrityEnabled {
		opts = append(opts, gonertia.WithIntegrity())
	}
	switch vc.PreloadStrategy {
	case PreloadNone:
		opts = append(opts, gonertia.WithoutPreloading())
	case PreloadAggressive:
		opts = append(opts, gonertia.WithAggressivePreload())
	case PreloadWaterfall:
		opts = append(opts, gonertia.WithWaterfallPreload(vc.PreloadConcurrent))
	}
	return opts
}

// ---------------------------------------------------------------------------
// Legacy Vite helpers (used when WithVite is not provided)
// ---------------------------------------------------------------------------

// readViteHotURL reads the Vite dev server URL from the hot file.
// Returns empty string if the file doesn't exist or can't be read.
func readViteHotURL(hotPath string) string {
	content, err := os.ReadFile(hotPath)
	if err != nil {
		return ""
	}
	url := strings.TrimSpace(string(content))
	if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
		// Strip protocol prefix to get "//host:port"
		url = url[strings.Index(url, ":")+1:]
		return url
	}
	return ""
}

// probeVitePort probes TCP ports starting from startPort to find a listening
// Vite dev server. Tries up to 100 ports. Returns the first port that responds,
// or falls back to the startPort if none found.
func probeVitePort(startPort int) string {
	for port := startPort; port < startPort+100; port++ {
		addr := fmt.Sprintf("localhost:%d", port)
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return fmt.Sprintf("//localhost:%d", port)
		}
	}
	return fmt.Sprintf("//localhost:%d", startPort)
}

// ---------------------------------------------------------------------------
// Handler-level API
// ---------------------------------------------------------------------------

// Respond renders an Inertia page response. Validation errors from the
// session are automatically merged into props.
func Respond(c app.Context, filePath string, props map[string]any) error {
	return Get(c.App()).Respond(c, filePath, props)
}

// Respond renders an Inertia page response for the given context.
func (i *Inertia) Respond(c app.Context, filePath string, props map[string]any) error {
	if errs := c.PopSession("errors"); errs != nil {
		if props == nil {
			props = map[string]any{}
		}
		props["errors"] = errs
	}
	ir := &InertiaResponse{Inertia: i, ctx: c, filePath: filePath, gProps: toGonertiaProps(props)}
	return c.Render(ir)
}

// Redirect performs a server-side redirect.
func (i *Inertia) Redirect(c app.Context, url string) {
	i.inner.Redirect(c.ResponseWriter(), c.Request(), url)
}

// Back redirects the user to the previous URL.
func (i *Inertia) Back(c app.Context) {
	i.inner.Back(c.ResponseWriter(), c.Request(), c.Status())
}

// Location performs an external redirect. For Inertia requests it sends a 409
// response with an X-Inertia-Location header; otherwise a standard HTTP redirect.
func (i *Inertia) Location(c app.Context, url string, status ...int) {
	i.inner.Location(c.ResponseWriter(), c.Request(), url, status...)
}

// Package-level convenience functions.

// Redirect performs a server-side redirect via the registered Inertia instance.
func Redirect(c app.Context, url string) {
	Get(c.App()).Redirect(c, url)
}

// Back redirects the user to the previous URL via the registered Inertia instance.
func Back(c app.Context) {
	Get(c.App()).Back(c)
}

// Location performs an external redirect via the registered Inertia instance.
func Location(c app.Context, url string, status ...int) {
	Get(c.App()).Location(c, url, status...)
}

// ---------------------------------------------------------------------------
// Context helpers — set/get Inertia data on request contexts
// ---------------------------------------------------------------------------

// --- Validation errors -----------------------------------------------------

// SetValidationErrors stores validation errors on the context for the next
// Inertia request. Typically called before a Redirect.
func SetValidationErrors(ctx context.Context, errors ValidationErrors) context.Context {
	return gonertia.SetValidationErrors(ctx, gonertia.ValidationErrors(errors))
}

// GetValidationErrors retrieves validation errors from the context.
func GetValidationErrors(ctx context.Context) ValidationErrors {
	return ValidationErrors(gonertia.ValidationErrorsFromContext(ctx))
}

// AddValidationErrors appends validation errors to the context.
func AddValidationErrors(ctx context.Context, errors ValidationErrors) context.Context {
	return gonertia.AddValidationErrors(ctx, gonertia.ValidationErrors(errors))
}

// SetValidationError stores a single validation error message on the context.
func SetValidationError(ctx context.Context, key string, msg string) context.Context {
	return gonertia.SetValidationError(ctx, key, msg)
}

// --- Flash data ------------------------------------------------------------

// SetFlash stores flash data on the context for the next Inertia request.
func SetFlash(ctx context.Context, flash Flash) context.Context {
	return gonertia.SetFlash(ctx, gonertia.Flash(flash))
}

// GetFlash retrieves flash data from the context.
func GetFlash(ctx context.Context) Flash {
	return Flash(gonertia.FlashFromContext(ctx))
}

// AddFlash appends flash data to the context.
func AddFlash(ctx context.Context, flash Flash) context.Context {
	return gonertia.AddFlash(ctx, gonertia.Flash(flash))
}

// SetFlashValue stores a single flash value on the context.
func SetFlashValue(ctx context.Context, key string, val any) context.Context {
	return gonertia.SetFlashValue(ctx, key, val)
}

// --- History encryption / clear --------------------------------------------

// SetEncryptHistory enables or disables history encryption for the next request.
func SetEncryptHistory(ctx context.Context, encrypt ...bool) context.Context {
	return gonertia.SetEncryptHistory(ctx, encrypt...)
}

// ClearHistory marks that history should be cleared on the next request.
func ClearHistory(ctx context.Context) context.Context {
	return gonertia.ClearHistory(ctx)
}

// --- Props (via context, for middleware) ------------------------------------

// SetProps stores props on the context. Props set this way are merged into
// every page rendered during the request.
func SetProps(ctx context.Context, props map[string]any) context.Context {
	return gonertia.SetProps(ctx, gonertia.Props(props))
}

// SetProp stores a single prop value on the context.
func SetProp(ctx context.Context, key string, val any) context.Context {
	return gonertia.SetProp(ctx, key, val)
}

// GetProps retrieves props from the context.
func GetProps(ctx context.Context) map[string]any {
	return map[string]any(gonertia.PropsFromContext(ctx))
}

// --- Template data (via context, for middleware) ----------------------------

// SetTemplateData stores template data on the context. Values set this way
// are available in the root template.
func SetTemplateData(ctx context.Context, data map[string]any) context.Context {
	return gonertia.SetTemplateData(ctx, gonertia.TemplateData(data))
}

// SetTemplateDatum stores a single template datum on the context.
func SetTemplateDatum(ctx context.Context, key string, val any) context.Context {
	return gonertia.SetTemplateDatum(ctx, key, val)
}

// GetTemplateData retrieves template data from the context.
func GetTemplateData(ctx context.Context) map[string]any {
	return map[string]any(gonertia.TemplateDataFromContext(ctx))
}

// ---------------------------------------------------------------------------
// Renderer implementation
// ---------------------------------------------------------------------------

// Render satisfies the Renderer interface expected by app.Context.Render().
func (ir *InertiaResponse) Render(w io.Writer) error {
	if ir.ctx.Status() == 0 {
		ir.ctx.SetStatus(http.StatusOK)
	}
	return ir.inner.Render(w.(http.ResponseWriter), ir.ctx.Request(), ir.filePath, ir.gProps)
}

// ---------------------------------------------------------------------------
// Wrapping helpers — expose gonertia features without leaking the type
// ---------------------------------------------------------------------------

// ShareTemplateFunc makes a function available in the root template.
func (i *Inertia) ShareTemplateFunc(key string, val any) error {
	return i.inner.ShareTemplateFunc(key, val)
}

// ShareTemplateData makes a value available in the root template.
func (i *Inertia) ShareTemplateData(key string, val any) {
	i.inner.ShareTemplateData(key, val)
}

// ShareProp shares a prop globally across all pages.
func (i *Inertia) ShareProp(key string, val any) {
	i.inner.ShareProp(key, val)
}

// Middleware returns an HTTP middleware that handles Inertia request detection,
// version checks, flash data resolution, and response wrapping.
func (i *Inertia) Middleware(next http.Handler) http.Handler {
	return i.inner.Middleware(next)
}

// ---------------------------------------------------------------------------
// Retrieval
// ---------------------------------------------------------------------------

// Get retrieves the registered Inertia instance from the app container.
func Get(a app.App) *Inertia {
	return app.Get[*Inertia](a)
}

// ---------------------------------------------------------------------------
// Vite helper (standalone)
// ---------------------------------------------------------------------------

// Vite returns a template function that resolves Vite asset paths from a
// manifest file. Used when not leveraging gonertia's built-in Vite support.
func Vite(manifestPath, buildDir string) func(p string) (string, error) {
	f, err := os.Open(manifestPath)
	if err != nil {
		log.Fatalf("cannot open provided vite manifest file: %s", err)
	}
	defer f.Close()

	viteAssets := make(map[string]*struct {
		File   string `json:"file"`
		Source string `json:"src"`
	})
	err = json.NewDecoder(f).Decode(&viteAssets)
	for k, v := range viteAssets {
		log.Printf("%s: %s\n", k, v.File)
	}

	if err != nil {
		log.Fatalf("cannot unmarshal vite manifest file to json: %s", err)
	}

	return func(p string) (string, error) {
		if val, ok := viteAssets[p]; ok {
			return path.Join("/", buildDir, val.File), nil
		}
		return "", fmt.Errorf("asset %q not found", p)
	}
}
