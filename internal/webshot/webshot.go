// Package webshot captures real screenshots of the draiver webui with Playwright.
// It is drv-019's answer to "those screenshots are only green and red squares —
// they should be playwright screenshots of the webui" (ticket decision #22): the
// acceptance suite points it at a live `internal/web` server and attaches the
// resulting PNGs as each scenario's artefact.
//
// Why Node Playwright driving a pre-installed chromium rather than playwright-go
// (decision #23): this box is arm64 with a custom browser revision, and
// playwright-go's driver download 404s on the retired CDN. Node Playwright, by
// contrast, launches the browser already under $PLAYWRIGHT_BROWSERS_PATH via
// executablePath, so it never downloads anything. The Playwright logic lives in the
// embedded capture.js; this file resolves node, the chromium binary, and the
// playwright node module, then runs capture.js over them.
//
// Availability is deliberately a precondition, not a fallback: Available() reports
// whether a real capture can be made, so the bdd environment's healthcheck can
// BLOCK a rerun ("pass the ball", drv-012) on a box without a browser rather than
// emitting bogus evidence.
package webshot

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

//go:embed capture.js
var captureJS []byte

// Options tune a single capture. The zero value yields a 1280x800 viewport
// screenshot with no explicit wait.
type Options struct {
	Width, Height int    // viewport; 0 → 1280x800
	FullPage      bool   // capture the full scrollable page, not just the viewport
	WaitSelector  string // wait for this CSS selector to appear before shooting
	// ClickSelector, when set, is clicked once after the page loads and before the
	// screenshot — so a caller can drive a client-side affordance (select a tab,
	// expand a disclosure) and capture the resulting state. Best-effort: a missing
	// target is not fatal, since the screenshot of the unchanged page is still useful
	// evidence rather than a failed capture.
	ClickSelector string
}

// Capture screenshots url and returns the PNG bytes. It is an error — not an empty
// image — when node, the chromium binary, or the playwright module is missing, so a
// caller that cannot screenshot fails loudly rather than silently producing nothing.
func Capture(ctx context.Context, url string, opts Options) ([]byte, error) {
	node, err := exec.LookPath("node")
	if err != nil {
		return nil, fmt.Errorf("webshot: node not found on PATH: %w", err)
	}
	chrome, ok := resolveChromium()
	if !ok {
		return nil, fmt.Errorf("webshot: no chromium browser found (set PLAYWRIGHT_EXECUTABLE or install the playwright chromium under $PLAYWRIGHT_BROWSERS_PATH)")
	}
	nodeModules, ok := resolveNodeModules()
	if !ok {
		return nil, fmt.Errorf("webshot: playwright node module not found (run `npm install playwright`)")
	}

	tmp, err := os.MkdirTemp("", "webshot-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	script := filepath.Join(tmp, "capture.js")
	if err := os.WriteFile(script, captureJS, 0o644); err != nil {
		return nil, err
	}
	out := filepath.Join(tmp, "shot.png")

	width := opts.Width
	if width == 0 {
		width = 1280
	}
	height := opts.Height
	if height == 0 {
		height = 800
	}
	full := "0"
	if opts.FullPage {
		full = "1"
	}

	cmd := exec.CommandContext(ctx, node, script)
	cmd.Env = append(os.Environ(),
		"WEBSHOT_URL="+url,
		"WEBSHOT_OUT="+out,
		"WEBSHOT_WIDTH="+strconv.Itoa(width),
		"WEBSHOT_HEIGHT="+strconv.Itoa(height),
		"WEBSHOT_FULL="+full,
		"WEBSHOT_WAIT_SELECTOR="+opts.WaitSelector,
		"WEBSHOT_CLICK_SELECTOR="+opts.ClickSelector,
		"PLAYWRIGHT_EXECUTABLE="+chrome,
		// capture.js is in a temp dir, so `require('playwright')` can only resolve via
		// NODE_PATH (verified: a temp-located script finds the dep this way).
		"NODE_PATH="+nodeModules,
	)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("webshot: capture of %s failed: %w\n%s", url, err, strings.TrimSpace(stderr.String()))
	}
	return os.ReadFile(out)
}

// Available reports whether a real capture can be made right now: node on PATH, a
// chromium binary on disk, and a resolvable playwright module. It is the probe the
// bdd environment's healthcheck and the suite's unit-rung tests gate on.
func Available() bool {
	if _, err := exec.LookPath("node"); err != nil {
		return false
	}
	if _, ok := resolveChromium(); !ok {
		return false
	}
	if _, ok := resolveNodeModules(); !ok {
		return false
	}
	return true
}

// resolveChromium finds the chromium binary to launch: an explicit
// PLAYWRIGHT_EXECUTABLE wins; otherwise it globs the playwright browser cache,
// preferring the full chromium build and falling back to the headless shell. The
// revision is wildcarded so a custom revision (chromium-1234 here) still resolves.
func resolveChromium() (string, bool) {
	if p := os.Getenv("PLAYWRIGHT_EXECUTABLE"); p != "" {
		if fileExists(p) {
			return p, true
		}
	}
	base := os.Getenv("PLAYWRIGHT_BROWSERS_PATH")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		base = filepath.Join(home, ".cache", "ms-playwright")
	}
	patterns := []string{
		filepath.Join(base, "chromium-*", "chrome-linux", "chrome"),
		filepath.Join(base, "chromium_headless_shell-*", "chrome-linux", "headless_shell"),
		filepath.Join(base, "chromium-*", "chrome-mac*", "Chromium.app", "Contents", "MacOS", "Chromium"),
	}
	for _, pat := range patterns {
		if m, _ := filepath.Glob(pat); len(m) > 0 {
			return m[0], true
		}
	}
	return "", false
}

// resolveNodeModules finds a node_modules dir containing playwright. It checks (in
// order) an explicit override, each NODE_PATH entry, the conventional install
// locations for the acceptance rung (cwd and features/screenshots), and then walks
// up from cwd — so the resolution works whether the rung set NODE_PATH or not.
func resolveNodeModules() (string, bool) {
	var cands []string
	if p := os.Getenv("DRAIVER_WEBSHOT_NODE_MODULES"); p != "" {
		cands = append(cands, p)
	}
	for _, p := range filepath.SplitList(os.Getenv("NODE_PATH")) {
		if p != "" {
			cands = append(cands, p)
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		// Walk up from cwd, checking both a plain node_modules and the acceptance
		// rung's scoped install (features/screenshots/node_modules) at each ancestor —
		// so resolution works from the repo root (the rung) and from a package dir (go
		// test), regardless of which set NODE_PATH.
		d := cwd
		for i := 0; i < 10; i++ {
			cands = append(cands,
				filepath.Join(d, "node_modules"),
				filepath.Join(d, "features", "screenshots", "node_modules"),
			)
			parent := filepath.Dir(d)
			if parent == d {
				break
			}
			d = parent
		}
	}
	for _, c := range cands {
		if fileExists(filepath.Join(c, "playwright", "package.json")) {
			return c, true
		}
	}
	return "", false
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
