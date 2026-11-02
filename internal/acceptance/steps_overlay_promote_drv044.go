package acceptance

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/config"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
	"github.com/Dawil/draiver/internal/web"
	"github.com/Dawil/draiver/internal/webshot"
	"github.com/Dawil/draiver/internal/worktree"
)

// steps_overlay_promote_drv044.go realises drv-044's acceptance criteria
// (features/overlays/promotions.feature): the three gaps that stopped a promotion
// declared in a test-pyramid overlay from working off the CLI are closed. Two
// scenarios, two surfaces:
//
//   - `draiver config repo --overlay` set/clear/read round-trip, shelling the real
//     binary against an isolated config — and proving the value written is byte-for-byte
//     what the web repo-settings panel (config.SetRepoSettings) writes for the same input.
//   - an overlay-only promotion going green from the attempt-page Check button over a
//     LIVE webui: no in-repo pyramid, an overlay declaring the promotion, a real POST to
//     the promote handler that must now run the overlay qa (with the overlay script-env)
//     and record a commit-bound pass + overlay provenance — not StatusNoPromotion.
//
// The steps register into the shared registry from init(), leaving earlier features
// untouched.

const (
	d44Ticket = "OVP-1"
	d44Att    = "0001"
)

func init() {
	drv044 := map[string]StepFunc{
		"a repo with an isolated operator config":                                                 d44GivenIsolatedConfig,
		"I run draiver config repo with --overlay pointing at an overlay repo":                    d44WhenConfigSetOverlay,
		"config.json records the explicit per-repo overlay":                                       d44ThenConfigRecordsOverlay,
		"the value written is byte-for-byte what the web repo-settings panel writes":              d44ThenConfigByteForByte,
		"I run draiver config repo with --overlay cleared":                                        d44WhenConfigClearOverlay,
		"config.json no longer records a per-repo overlay":                                        d44ThenConfigOverlayCleared,
		"I run draiver config repo with no flags":                                                 d44WhenConfigRead,
		"the read form prints the resolved overlay alongside remote, branch, and rung":            d44ThenReadFormPrintsOverlay,
		"a Review attempt whose repo has no in-repo pyramid but an overlay declaring a promotion": d44GivenOverlayPromotionBoard,
		"I click the attempt-page Check button for the overlay's environment":                     d44WhenClickCheck,
		"the promotion runs the overlay qa with the overlay script-env and returns a real pass":   d44ThenRealPass,
		"a commit-bound promotion-result pass is recorded, not a no-promotion":                    d44ThenResultRecorded,
		"the board-triggered promotion records the overlay provenance a reviewer can see":         d44ThenProvenanceRecorded,
		"the attempt page shows the overlay promotion promoted at HEAD":                           d44ThenPromotedAtHead,
	}
	for text, fn := range drv044 {
		registry[text] = fn
	}
}

// --- Scenario A: `config repo --overlay` round-trip ---

// d44ConfigWorld is the CLI scenario's isolated state: a config file the shelled binary
// writes, a data root it resolves, a cache dir, the codebase repo path (the settings
// key — a path string, not necessarily a live repo), and the overlay location the flag
// pins. runOut/runErr capture the last invocation.
type d44ConfigWorld struct {
	configPath  string
	dataRoot    string
	cacheDir    string
	repoPath    string
	overlayRepo string
	runOut      string
	runErr      error
}

func d44Config(w *World) (*d44ConfigWorld, error) {
	c, _ := w.get("d44cfg").(*d44ConfigWorld)
	if c == nil {
		return nil, fmt.Errorf("no config scenario set up in scope")
	}
	return c, nil
}

func d44GivenIsolatedConfig(w *World, sr *StepRun) error {
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("git not on PATH")
	}
	c := &d44ConfigWorld{
		configPath:  filepath.Join(mustTemp(w, "d44-cfg-"), "config.json"),
		dataRoot:    mustTemp(w, "d44-data-"),
		cacheDir:    mustTemp(w, "d44-cache-"),
		repoPath:    mustTemp(w, "d44-repo-"),
		overlayRepo: mustTemp(w, "d44-overlay-"),
	}
	w.set("d44cfg", c)
	return nil
}

// d44RunConfig shells the freshly-built binary's `config repo` verb against the isolated
// config/data/cache, so the write path under test is the real CLI, not an in-process call.
func d44RunConfig(c *d44ConfigWorld, args ...string) (string, error) {
	bin, err := overlayBin()
	if err != nil {
		return "", err
	}
	cmd := exec.Command(bin, append([]string{"config", "repo"}, args...)...)
	cmd.Env = append(os.Environ(),
		"DRAIVER_CONFIG="+c.configPath,
		"DRAIVER_DATA="+c.dataRoot,
		"XDG_CACHE_HOME="+c.cacheDir,
		"DRAIVER_ACTOR=agent:acceptance",
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func d44WhenConfigSetOverlay(w *World, sr *StepRun) error {
	c, err := d44Config(w)
	if err != nil {
		return err
	}
	c.runOut, c.runErr = d44RunConfig(c, c.repoPath, "--overlay", c.overlayRepo)
	if c.runErr != nil {
		return fmt.Errorf("config repo --overlay failed: %v\n%s", c.runErr, c.runOut)
	}
	return nil
}

func d44ThenConfigRecordsOverlay(w *World, sr *StepRun) error {
	c, err := d44Config(w)
	if err != nil {
		return err
	}
	cfg, err := config.Load(c.configPath)
	if err != nil {
		return err
	}
	if got := cfg.RepoSettings(c.repoPath).Overlay; got != c.overlayRepo {
		return fmt.Errorf("config.json overlay = %q, want %q", got, c.overlayRepo)
	}
	return nil
}

// d44ThenConfigByteForByte proves the CLI write equals the web panel's write: a SECOND
// fresh config gets the identical update the panel makes (config.SetRepoSettings with
// the trimmed overlay) and the two config.json files must be byte-for-byte equal.
func d44ThenConfigByteForByte(w *World, sr *StepRun) error {
	c, err := d44Config(w)
	if err != nil {
		return err
	}
	panelCfg := filepath.Join(mustTemp(w, "d44-panelcfg-"), "config.json")
	overlay := strings.TrimSpace(c.overlayRepo)
	if _, err := config.SetRepoSettings(panelCfg, c.repoPath, config.RepoSettingsUpdate{Overlay: &overlay}); err != nil {
		return err
	}
	cliBytes, err := os.ReadFile(c.configPath)
	if err != nil {
		return err
	}
	panelBytes, err := os.ReadFile(panelCfg)
	if err != nil {
		return err
	}
	if string(cliBytes) != string(panelBytes) {
		return fmt.Errorf("CLI and panel writes differ:\nCLI:   %s\nPANEL: %s", cliBytes, panelBytes)
	}
	attachText(sr, "config-overlay-parity", "CLI `config repo --overlay` == web panel write:\n\n"+string(cliBytes))
	return nil
}

func d44WhenConfigClearOverlay(w *World, sr *StepRun) error {
	c, err := d44Config(w)
	if err != nil {
		return err
	}
	c.runOut, c.runErr = d44RunConfig(c, c.repoPath, "--overlay", "")
	if c.runErr != nil {
		return fmt.Errorf("config repo --overlay \"\" failed: %v\n%s", c.runErr, c.runOut)
	}
	return nil
}

func d44ThenConfigOverlayCleared(w *World, sr *StepRun) error {
	c, err := d44Config(w)
	if err != nil {
		return err
	}
	cfg, err := config.Load(c.configPath)
	if err != nil {
		return err
	}
	if got := cfg.RepoSettings(c.repoPath).Overlay; got != "" {
		return fmt.Errorf("overlay should be cleared, got %q", got)
	}
	return nil
}

func d44WhenConfigRead(w *World, sr *StepRun) error {
	c, err := d44Config(w)
	if err != nil {
		return err
	}
	c.runOut, c.runErr = d44RunConfig(c, c.repoPath)
	if c.runErr != nil {
		return fmt.Errorf("config repo (read) failed: %v\n%s", c.runErr, c.runOut)
	}
	return nil
}

func d44ThenReadFormPrintsOverlay(w *World, sr *StepRun) error {
	c, err := d44Config(w)
	if err != nil {
		return err
	}
	for _, label := range []string{"default remote", "default branch", "default test rung", "overlay"} {
		if !strings.Contains(c.runOut, label) {
			return fmt.Errorf("read form should print the %q row, got:\n%s", label, c.runOut)
		}
	}
	attachText(sr, "config-repo-read-form", c.runOut)
	return nil
}

// --- Scenario B: overlay-only promotion from the attempt-page Check button ---

// d44OverlayPyramid declares a promotion for environment `dev` whose qa is green ONLY
// when the overlay execution contract is threaded through: DRAIVER_OVERLAY_DIR must be
// set (proving the executor cut + wired the overlay, not nil extraEnv) and the
// DRAIVER_PROMOTE_* contract must be present. If promote.Run still ignored overlays it
// would return StatusNoPromotion; if it ran with nil extraEnv the qa would fail.
const d44OverlayPyramid = `environments:
  - name: dev
    healthchecks:
      - name: ready
        script: "true"
levels:
  - name: unit
    run: "true"
promotions:
  - environment: dev
    base: main
    qa: test -n "$DRAIVER_OVERLAY_DIR" && test "$DRAIVER_PROMOTE_ENV" = dev && test "$DRAIVER_PROMOTE_BASE" = main
`

// d44Board is the live overlay-promotion board: the codebase board (no in-repo pyramid),
// the overlay repo supplying the pyramid, its HEAD, and the codebase HEAD the result binds.
type d44Board struct {
	*board
	overlayRepo   string
	overlayCommit string
	head          string
}

func currentD44Board(w *World) (*d44Board, error) {
	b, _ := w.get("d44board").(*d44Board)
	if b == nil {
		return nil, fmt.Errorf("no overlay-promotion board set up in scope")
	}
	return b, nil
}

func d44GivenOverlayPromotionBoard(w *World, sr *StepRun) error {
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("git not on PATH")
	}
	ctx := context.Background()

	// One cache base shared by the managed codebase worktree seeded here and the overlay
	// worktree promote.Run cuts in-process — mirroring the overlay feature's discipline.
	cache := mustTemp(w, "d44b-cache-")
	prevCache, hadCache := os.LookupEnv("XDG_CACHE_HOME")
	os.Setenv("XDG_CACHE_HOME", cache)
	w.defer_(func() {
		if hadCache {
			os.Setenv("XDG_CACHE_HOME", prevCache)
		} else {
			os.Unsetenv("XDG_CACHE_HOME")
		}
	})

	// Codebase repo + managed worktree carrying NO .test-pyramid.yaml.
	repoDir := mustTemp(w, "d44b-repo-")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
		{"commit", "-q", "--allow-empty", "-m", "init"},
	} {
		if err := gitc(repoDir, args...); err != nil {
			return err
		}
	}
	m, err := worktree.NewManager(repoDir)
	if err != nil {
		return err
	}
	wt, err := m.Create(ctx, worktree.Spec{Key: worktree.Key{Ticket: d44Ticket, Attempt: d44Att}})
	if err != nil {
		return err
	}
	head, err := worktree.HeadSHA(ctx, wt.Path)
	if err != nil {
		return err
	}

	// Overlay repo supplying the pyramid (committed on its main branch).
	overlayRepo := mustTemp(w, "d44b-overlay-")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
	} {
		if err := gitc(overlayRepo, args...); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(overlayRepo, ".test-pyramid.yaml"), []byte(d44OverlayPyramid), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "overlay pyramid"}} {
		if err := gitc(overlayRepo, args...); err != nil {
			return err
		}
	}
	overlayCommit, err := worktree.HeadSHA(ctx, overlayRepo)
	if err != nil {
		return err
	}

	// Data root + a Review attempt pointing at the codebase repo.
	dataDir := mustTemp(w, "d44b-data-")
	root := store.Root{Dir: dataDir}
	if err := root.EnsureAttemptDirs(d44Ticket, d44Att); err != nil {
		return err
	}
	spec := "---\nid: " + d44Ticket + "\ntitle: drv-044 overlay promotion\n---\n\n# drv-044 overlay promotion\n\n## Acceptance criteria\n\n- An overlay-only promotion goes green from the button.\n"
	if err := os.WriteFile(root.SpecPath(d44Ticket), []byte(spec), 0o644); err != nil {
		return err
	}
	meta := "---\nid: " + d44Att + "\nticket: " + d44Ticket + "\nrepo: " + repoDir + "\nbase: main\n---\n\n"
	if err := os.WriteFile(root.AttemptMetaPath(d44Ticket, d44Att), []byte(meta), 0o644); err != nil {
		return err
	}
	for _, e := range []event.Event{
		{Type: "created", Actor: "agent:acceptance", Body: "start"},
		{Type: "review", Actor: "agent:acceptance", Body: "Claiming review."},
	} {
		if _, err := ticketlog.Append(root, d44Ticket, d44Att, e); err != nil {
			return err
		}
	}

	// Isolated operator config registering the explicit per-repo overlay — the SAME
	// config the in-process web server (gate + executor + provenance) reads via
	// $DRAIVER_CONFIG. Set in-process with a restore, mirroring XDG_CACHE_HOME above.
	cfgPath := filepath.Join(mustTemp(w, "d44b-cfg-"), "config.json")
	if _, err := config.SetRepoSettings(cfgPath, repoDir, config.RepoSettingsUpdate{Overlay: &overlayRepo}); err != nil {
		return err
	}
	prevCfg, hadCfg := os.LookupEnv("DRAIVER_CONFIG")
	os.Setenv("DRAIVER_CONFIG", cfgPath)
	w.defer_(func() {
		if hadCfg {
			os.Setenv("DRAIVER_CONFIG", prevCfg)
		} else {
			os.Unsetenv("DRAIVER_CONFIG")
		}
	})

	srv, err := web.New(root)
	if err != nil {
		return err
	}
	ts := httptest.NewServer(srv.Handler())
	w.defer_(ts.Close)

	b := &d44Board{
		board:         &board{root: root, wd: repoDir, srv: ts},
		overlayRepo:   overlayRepo,
		overlayCommit: overlayCommit,
		head:          head,
	}
	w.set("board", b.board)
	w.set("d44board", b)
	return nil
}

// d44WhenClickCheck POSTs the promote action exactly as the attempt-page Check button
// does (an htmx POST to the handler). A same-origin request with no Origin header passes
// the guard, so this drives the real handlePromote → promote.Run end to end.
func d44WhenClickCheck(w *World, sr *StepRun) error {
	b, err := currentD44Board(w)
	if err != nil {
		return err
	}
	resp, err := http.Post(b.board.url("/ticket/"+d44Ticket+"/"+d44Att+"/promote/dev"), "application/x-www-form-urlencoded", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	body := string(raw)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("POST promote/dev: status %d\n%s", resp.StatusCode, body)
	}
	w.set("d44resp", body)
	return nil
}

func d44ThenRealPass(w *World, sr *StepRun) error {
	body, _ := w.get("d44resp").(string)
	// The OOB promote-result banner carries the executor status. A real overlay run that
	// passed renders data-status="ok"; the bug this ticket fixes rendered "no-promotion".
	if strings.Contains(body, `data-status="no-promotion"`) {
		return fmt.Errorf("overlay promotion returned StatusNoPromotion — the executor still ignores overlays:\n%s", body)
	}
	if !strings.Contains(body, `data-status="ok"`) {
		return fmt.Errorf("expected a green (ok) promote result, got:\n%s", body)
	}
	return nil
}

func d44ThenResultRecorded(w *World, sr *StepRun) error {
	b, err := currentD44Board(w)
	if err != nil {
		return err
	}
	a, err := project.LoadAttempt(b.root, d44Ticket, d44Att)
	if err != nil {
		return err
	}
	ps := project.DerivePromotions(a.Events)["dev"]
	if ps.Outcome != "pass" {
		return fmt.Errorf("promotion-result for dev = %q, want a recorded pass", ps.Outcome)
	}
	if ps.Commit != b.head {
		return fmt.Errorf("promotion-result commit = %q, want the reviewed codebase HEAD %q (commit-bound)", ps.Commit, b.head)
	}
	return nil
}

func d44ThenProvenanceRecorded(w *World, sr *StepRun) error {
	b, err := currentD44Board(w)
	if err != nil {
		return err
	}
	meta, err := attempt.LoadMeta(b.root, d44Ticket, d44Att)
	if err != nil {
		return err
	}
	if meta.Overlay != b.overlayRepo {
		return fmt.Errorf("provenance overlay = %q, want the overlay repo %q", meta.Overlay, b.overlayRepo)
	}
	if meta.OverlayCommit != b.overlayCommit {
		return fmt.Errorf("provenance overlay_commit = %q, want the overlay HEAD %q used", meta.OverlayCommit, b.overlayCommit)
	}
	return nil
}

// d44ThenPromotedAtHead asserts the refreshed attempt page renders the overlay promotion
// as promoted-at-HEAD and captures the Playwright screenshot of the Check button's real
// result (the Promotions tab revealed), the corroborating artefact drv-044 owes.
func d44ThenPromotedAtHead(w *World, sr *StepRun) error {
	b, err := currentD44Board(w)
	if err != nil {
		return err
	}
	body, err := promGetBody(b.board.url("/ticket/" + d44Ticket + "/" + d44Att))
	if err != nil {
		return err
	}
	if !strings.Contains(body, `data-promote-class="ok"`) {
		return fmt.Errorf("attempt page should show the dev promotion as promoted @ HEAD (ok), got no ok panel:\n%s", body)
	}
	// The screenshot is the corroborating artefact, captured under the bdd `acceptance`
	// rung where a browser is a hard precondition. On a browserless box (the unit rung, a
	// freshly-cut worktree before `npm install`) the real assertions above have already
	// run; attach the rendered HTML instead of failing, mirroring how the unit-rung
	// guard skips the UI capture rather than the whole feature.
	if !webshot.Available() {
		attachText(sr, "overlay-promotion-check-result", "webshot unavailable — promoted @ HEAD panel rendered:\n\n"+body)
		return nil
	}
	tab := `[data-testid="attempt-tab-promote"]`
	return shoot(w, sr, "/ticket/"+d44Ticket+"/"+d44Att, "overlay-promotion-check-result", true, tab, tab)
}
