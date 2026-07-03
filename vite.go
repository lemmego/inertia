package inertia

// ViteConfig holds Vite-specific settings for the Inertia instance.
// Applied via WithVite().
type ViteConfig struct {
	HotFile           string
	BuildManifest     string
	FallbackManifest  string
	BuildDir          string
	HotReloadPort     string
	EntryPoints       []string
	IntegrityEnabled  bool
	PreloadStrategy   PreloadStrategy
	PreloadConcurrent int
	EmbedFS           any // fs.FS for embedded manifest loading
}

// PreloadStrategy controls how Vite assets are preloaded.
type PreloadStrategy string

const (
	PreloadNone       PreloadStrategy = "none"
	PreloadAggressive PreloadStrategy = "aggressive"
	PreloadWaterfall  PreloadStrategy = "waterfall"
)

// ViteOption configures the Vite integration.
type ViteOption func(*ViteConfig)

// WithHotFile sets the hot reload file path.
func WithHotFile(path string) ViteOption {
	return func(c *ViteConfig) { c.HotFile = path }
}

// WithBuildManifest sets the build manifest path.
func WithBuildManifest(path string) ViteOption {
	return func(c *ViteConfig) { c.BuildManifest = path }
}

// WithFallbackManifest sets the fallback manifest path.
func WithFallbackManifest(path string) ViteOption {
	return func(c *ViteConfig) { c.FallbackManifest = path }
}

// WithBuildDir sets the build output directory.
func WithBuildDir(dir string) ViteOption {
	return func(c *ViteConfig) { c.BuildDir = dir }
}

// WithHotReloadPort sets the hot reload server port.
func WithHotReloadPort(port string) ViteOption {
	return func(c *ViteConfig) { c.HotReloadPort = port }
}

// WithEntryPoints sets the entry points for asset generation.
func WithEntryPoints(entries ...string) ViteOption {
	return func(c *ViteConfig) { c.EntryPoints = entries }
}

// WithIntegrity enables SubResource Integrity (requires vite-plugin).
func WithIntegrity() ViteOption {
	return func(c *ViteConfig) { c.IntegrityEnabled = true }
}

// WithoutPreloading disables asset preloading (default).
func WithoutPreloading() ViteOption {
	return func(c *ViteConfig) {
		c.PreloadStrategy = PreloadNone
		c.PreloadConcurrent = 0
	}
}

// WithAggressivePreload preloads all dependencies immediately.
func WithAggressivePreload() ViteOption {
	return func(c *ViteConfig) {
		c.PreloadStrategy = PreloadAggressive
		c.PreloadConcurrent = 0
	}
}

// WithWaterfallPreload enables batched prefetch with concurrency control.
func WithWaterfallPreload(concurrent int) ViteOption {
	return func(c *ViteConfig) {
		c.PreloadStrategy = PreloadWaterfall
		c.PreloadConcurrent = concurrent
	}
}

// WithViteEmbedFS sets an embed.FS for production builds.
func WithViteEmbedFS(fs any) ViteOption {
	return func(c *ViteConfig) { c.EmbedFS = fs }
}

func defaultViteConfig() ViteConfig {
	return ViteConfig{
		HotFile:           "public/hot",
		BuildManifest:     "public/build/manifest.json",
		FallbackManifest:  "public/build/.vite/manifest.json",
		BuildDir:          "/build/",
		HotReloadPort:     "//localhost:5173",
		PreloadStrategy:   PreloadNone,
		PreloadConcurrent: 3,
	}
}
