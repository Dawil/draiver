package acceptance

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/config"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/overlay"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
	"github.com/Dawil/draiver/internal/worktree"
)

// steps_overlay.go realises drv-041's acceptance criteria
// (features/test-pyramid/overlay.feature): a test-pyramid overlay is a separate,
// developer-owned repo that supplies a codebase's pyramid via settings only, with zero
// version-control footprint on the codebase. Unlike the drv-025 gate steps — which fold
// the gate in-process — these scenarios exercise the REAL `draiver test` executor end to
// end, because the thing under test (resolution order, cwd = the codebase worktree, the
// DRAIVER_CODEBASE_DIR / DRAIVER_OVERLAY_DIR execution contract, provenance recording)
// lives in cmd/ and is only observable by running the binary. So each scenario stands up
// a real codebase worktree + (optionally) a real overlay repo, shells a freshly-built
// `draiver` binary against an isolated data root / config / cache, and asserts the
// side effects (a proof file the overlay's `run` writes, the attempt provenance, the
// overlay's untouched main). The steps register into the shared registry from init().

const (
	ovTicket = "OVL-1"
	ovAtt    = "0001"
)

func init() {
	steps := map[string]StepFunc{
		"a codebase worktree with no in-repo pyramid":                                         ovGivenCodebaseNoPyramid,
		"a codebase worktree with an in-repo pyramid":                                         ovGivenCodebaseInRepoPyramid,
		"an overlay registered for it whose pyramid records the run environment":              ovGivenOverlayRegistered,
		"no overlay registered for it":                                                        ovGivenNoOverlay,
		"a global overlay_dir is configured":                                                  ovGivenGlobalOverlayDir,
		"draiver test runs for the attempt":                                                   ovWhenTestRuns,
		"draiver test create-overlay runs for the attempt":                                    ovWhenCreateOverlayRuns,
		"the overlay pyramid ran with cwd the codebase and both overlay env vars exported":    ovThenOverlayRanWithEnv,
		"the attempt provenance records the overlay repo and the commit used":                 ovThenProvenanceRecorded,
		"the overlay's main branch is left untouched":                                         ovThenOverlayMainUntouched,
		"the in-repo pyramid ran and the overlay was never used":                              ovThenInRepoRan,
		"draiver test succeeds reporting nothing to test":                                     ovThenNoOp,
		"an overlay repo is scaffolded under the overlay_dir and registered for the codebase": ovThenScaffolded,
		"re-running create-overlay refuses and writes nothing":                                ovThenCreateOverlayRefuses,
	}
	for text, fn := range steps {
		registry[text] = fn
	}
}

// ovWorld is the scenario-scoped overlay fixture: an isolated data root / config / cache,
// a real codebase source repo + its managed attempt worktree (the cwd `draiver test`
// runs in), and — once registered — a real overlay repo and the commit its main sat at
// before the run (so "main left untouched" is checkable). The last `draiver` invocation's
// combined output and error are captured for the Then steps.
type ovWorld struct {
	dataRoot          string
	configPath        string
	cacheDir          string
	repoDir           string // codebase source repo (the attempt's recorded `repo`)
	checkout          string // codebase attempt worktree (the cwd for `draiver test`)
	overlayDir        string // global overlay_dir folder (create-overlay scenario)
	overlayRepo       string // registered overlay repo path
	overlayMainBefore string

	runOut string
	runErr error
}

// overlayBin builds the draiver binary once per acceptance run and returns its path,
// mirroring the integration rung's build-once discipline (never `go run`, which would
// remap escalate's exit-3). The acceptance rung's environment healthcheck already proves
// the Go toolchain is present, so this build is safe to reach for here.
var overlayBin = sync.OnceValues(ovBuildDraiver)

func ovBuildDraiver() (string, error) {
	root, err := ovRepoRoot()
	if err != nil {
		return "", err
	}
	bin := filepath.Join(root, "features", "_artifacts", ".bin", "draiver-overlay")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		return "", err
	}
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("build draiver: %w\n%s", err, stderr.String())
	}
	return bin, nil
}

// ovRepoRoot walks up from this source file to the module root (the directory holding
// go.mod), independent of the working directory the runner happens to use.
func ovRepoRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("cannot resolve repo root: runtime.Caller failed")
	}
	// file is <root>/internal/acceptance/steps_overlay.go
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..")), nil
}

func ovGet(w *World) (*ovWorld, error) {
	o, _ := w.get("ovl").(*ovWorld)
	if o == nil {
		return nil, fmt.Errorf("no overlay scenario set up in scope")
	}
	return o, nil
}

// ovSeedCodebase stands up the isolated data root, config, cache, and a codebase source
// repo with its managed attempt worktree. inRepoPyramid, when non-empty, is committed
// into the worktree as .test-pyramid.yaml (the in-repo-wins case); empty leaves the
// codebase pyramid-less. XDG_CACHE_HOME is redirected in-process (restored on cleanup)
// so the worktree this seeds and the overlay worktree the shelled binary later cuts
// share one cache base.
func ovSeedCodebase(w *World, inRepoPyramid string) (*ovWorld, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, fmt.Errorf("git not on PATH")
	}
	ctx := context.Background()
	o := &ovWorld{}

	o.cacheDir = mustTemp(w, "ovl-cache-")
	prev, had := os.LookupEnv("XDG_CACHE_HOME")
	os.Setenv("XDG_CACHE_HOME", o.cacheDir)
	w.defer_(func() {
		if had {
			os.Setenv("XDG_CACHE_HOME", prev)
		} else {
			os.Unsetenv("XDG_CACHE_HOME")
		}
	})

	o.repoDir = mustTemp(w, "ovl-repo-")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
		{"commit", "-q", "--allow-empty", "-m", "init"},
	} {
		if err := gitc(o.repoDir, args...); err != nil {
			return nil, err
		}
	}

	m, err := worktree.NewManager(o.repoDir)
	if err != nil {
		return nil, err
	}
	wt, err := m.Create(ctx, worktree.Spec{Key: worktree.Key{Ticket: ovTicket, Attempt: ovAtt}})
	if err != nil {
		return nil, err
	}
	o.checkout = wt.Path
	if inRepoPyramid != "" {
		if err := os.WriteFile(filepath.Join(o.checkout, ".test-pyramid.yaml"), []byte(inRepoPyramid), 0o644); err != nil {
			return nil, err
		}
		for _, args := range [][]string{{"add", ".test-pyramid.yaml"}, {"commit", "-q", "-m", "in-repo pyramid"}} {
			if err := gitc(o.checkout, args...); err != nil {
				return nil, err
			}
		}
	}

	o.dataRoot = mustTemp(w, "ovl-data-")
	root := store.Root{Dir: o.dataRoot}
	if err := root.EnsureAttemptDirs(ovTicket, ovAtt); err != nil {
		return nil, err
	}
	spec := "---\nid: " + ovTicket + "\ntitle: drv-041 overlay\n---\n\n# drv-041 overlay\n\n## Acceptance criteria\n\n- An overlay supplies the pyramid when the codebase has none.\n"
	if err := os.WriteFile(root.SpecPath(ovTicket), []byte(spec), 0o644); err != nil {
		return nil, err
	}
	meta := "---\nid: " + ovAtt + "\nticket: " + ovTicket + "\nrepo: " + o.repoDir + "\nbase: main\n---\n\n"
	if err := os.WriteFile(root.AttemptMetaPath(ovTicket, ovAtt), []byte(meta), 0o644); err != nil {
		return nil, err
	}
	if _, err := ticketlog.Append(root, ovTicket, ovAtt, event.Event{Type: "created", Actor: "agent:acceptance", Body: "start"}); err != nil {
		return nil, err
	}

	o.configPath = filepath.Join(mustTemp(w, "ovl-cfg-"), "config.json")
	w.set("ovl", o)
	return o, nil
}

func mustTemp(w *World, prefix string) string {
	dir, err := os.MkdirTemp("", prefix)
	if err != nil {
		panic(err)
	}
	w.defer_(func() { os.RemoveAll(dir) })
	return dir
}

func ovGivenCodebaseNoPyramid(w *World, sr *StepRun) error {
	_, err := ovSeedCodebase(w, "")
	return err
}

// ovInRepoYAML's run writes a proof into the cwd (the codebase worktree) and references
// NO overlay env vars — a local pyramid is byte-for-byte the pre-overlay behaviour.
const ovInRepoYAML = `levels:
  - name: unit
    run: 'echo in-repo > "$PWD/inrepo-proof"'
`

func ovGivenCodebaseInRepoPyramid(w *World, sr *StepRun) error {
	_, err := ovSeedCodebase(w, ovInRepoYAML)
	return err
}

// ovOverlayYAML's run records the execution contract: it writes the two exported env
// vars into a proof file anchored at $DRAIVER_CODEBASE_DIR (the cwd), so the Then step
// can assert cwd = the codebase and both vars are exported with the right values.
const ovOverlayYAML = `levels:
  - name: unit
    run: 'printf "%s|%s" "$DRAIVER_CODEBASE_DIR" "$DRAIVER_OVERLAY_DIR" > "$DRAIVER_CODEBASE_DIR/overlay-proof"'
`

func ovGivenOverlayRegistered(w *World, sr *StepRun) error {
	o, err := ovGet(w)
	if err != nil {
		return err
	}
	o.overlayRepo = mustTemp(w, "ovl-overlay-")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
	} {
		if err := gitc(o.overlayRepo, args...); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(o.overlayRepo, ".test-pyramid.yaml"), []byte(ovOverlayYAML), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "overlay pyramid"}} {
		if err := gitc(o.overlayRepo, args...); err != nil {
			return err
		}
	}
	sha, err := worktree.HeadSHA(context.Background(), o.overlayRepo)
	if err != nil {
		return err
	}
	o.overlayMainBefore = sha
	// Register the explicit per-codebase overlay in the isolated config the binary reads.
	if _, err := config.SetRepoSettings(o.configPath, o.repoDir, config.RepoSettingsUpdate{Overlay: &o.overlayRepo}); err != nil {
		return err
	}
	return nil
}

func ovGivenNoOverlay(w *World, sr *StepRun) error {
	_, err := ovGet(w)
	return err // nothing registered — the codebase resolves to no overlay
}

func ovGivenGlobalOverlayDir(w *World, sr *StepRun) error {
	o, err := ovGet(w)
	if err != nil {
		return err
	}
	o.overlayDir = mustTemp(w, "ovl-dir-")
	if _, err := config.SetOverlayDir(o.configPath, o.overlayDir); err != nil {
		return err
	}
	return nil
}

// ovChildEnv builds the shelled binary's environment: the parent environment with the
// four draiver knobs replaced so the run targets the scenario's isolated data root,
// config, cache, and actor — never the developer's real ~/.draiver.
func ovChildEnv(o *ovWorld) []string {
	drop := map[string]bool{
		"DRAIVER_DATA": true, "DRAIVER_CONFIG": true, "XDG_CACHE_HOME": true,
		"DRAIVER_ACTOR": true, "DRAIVER_ATTEMPT": true,
	}
	var env []string
	for _, e := range os.Environ() {
		if i := strings.IndexByte(e, '='); i >= 0 && drop[e[:i]] {
			continue
		}
		env = append(env, e)
	}
	return append(env,
		"DRAIVER_DATA="+o.dataRoot,
		"DRAIVER_CONFIG="+o.configPath,
		"XDG_CACHE_HOME="+o.cacheDir,
		"DRAIVER_ACTOR=agent:acceptance",
		"DRAIVER_ATTEMPT="+ovAtt,
	)
}

func ovRunDraiver(o *ovWorld, args ...string) (string, error) {
	bin, err := overlayBin()
	if err != nil {
		return "", err
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = o.checkout
	cmd.Env = ovChildEnv(o)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func ovWhenTestRuns(w *World, sr *StepRun) error {
	o, err := ovGet(w)
	if err != nil {
		return err
	}
	o.runOut, o.runErr = ovRunDraiver(o, "test")
	return nil
}

func ovWhenCreateOverlayRuns(w *World, sr *StepRun) error {
	o, err := ovGet(w)
	if err != nil {
		return err
	}
	o.runOut, o.runErr = ovRunDraiver(o, "test", "create-overlay")
	return nil
}

func ovThenOverlayRanWithEnv(w *World, sr *StepRun) error {
	o, err := ovGet(w)
	if err != nil {
		return err
	}
	if o.runErr != nil {
		return fmt.Errorf("draiver test failed: %v\n%s", o.runErr, o.runOut)
	}
	proof, err := os.ReadFile(filepath.Join(o.checkout, "overlay-proof"))
	if err != nil {
		return fmt.Errorf("overlay proof not written to the codebase cwd: %v\noutput:\n%s", err, o.runOut)
	}
	parts := strings.SplitN(strings.TrimSpace(string(proof)), "|", 2)
	if len(parts) != 2 {
		return fmt.Errorf("overlay proof malformed: %q", string(proof))
	}
	codebaseDir, overlayDir := parts[0], parts[1]
	if codebaseDir != o.checkout {
		return fmt.Errorf("DRAIVER_CODEBASE_DIR = %q, want the codebase worktree %q (cwd anchor)", codebaseDir, o.checkout)
	}
	if overlayDir == "" {
		return fmt.Errorf("DRAIVER_OVERLAY_DIR was empty — overlay env var not exported")
	}
	if !strings.HasPrefix(overlayDir, o.cacheDir) {
		return fmt.Errorf("DRAIVER_OVERLAY_DIR = %q, want a cut overlay worktree under the cache %q", overlayDir, o.cacheDir)
	}
	attachText(sr, "overlay-run-env", "DRAIVER_CODEBASE_DIR="+codebaseDir+"\nDRAIVER_OVERLAY_DIR="+overlayDir+"\n\n"+o.runOut)
	return nil
}

func ovThenProvenanceRecorded(w *World, sr *StepRun) error {
	o, err := ovGet(w)
	if err != nil {
		return err
	}
	meta, err := attempt.LoadMeta(store.Root{Dir: o.dataRoot}, ovTicket, ovAtt)
	if err != nil {
		return err
	}
	if meta.Overlay != o.overlayRepo {
		return fmt.Errorf("provenance overlay = %q, want %q", meta.Overlay, o.overlayRepo)
	}
	if strings.TrimSpace(meta.OverlayCommit) == "" {
		return fmt.Errorf("provenance overlay_commit is empty, want the exact commit used")
	}
	if meta.OverlayCommit != o.overlayMainBefore {
		return fmt.Errorf("provenance overlay_commit = %q, want the overlay HEAD %q it was cut from", meta.OverlayCommit, o.overlayMainBefore)
	}
	return nil
}

func ovThenOverlayMainUntouched(w *World, sr *StepRun) error {
	o, err := ovGet(w)
	if err != nil {
		return err
	}
	now, err := worktree.HeadSHA(context.Background(), o.overlayRepo)
	if err != nil {
		return err
	}
	if now != o.overlayMainBefore {
		return fmt.Errorf("overlay main moved: %q → %q (edits must land on the attempt branch, never main)", o.overlayMainBefore, now)
	}
	// The overlay's own tree must not carry the proof — it was written into the codebase.
	if _, err := os.Stat(filepath.Join(o.overlayRepo, "overlay-proof")); err == nil {
		return fmt.Errorf("overlay repo main tree was written to; the run must operate against the codebase cwd")
	}
	return nil
}

func ovThenInRepoRan(w *World, sr *StepRun) error {
	o, err := ovGet(w)
	if err != nil {
		return err
	}
	if o.runErr != nil {
		return fmt.Errorf("draiver test failed: %v\n%s", o.runErr, o.runOut)
	}
	if _, err := os.Stat(filepath.Join(o.checkout, "inrepo-proof")); err != nil {
		return fmt.Errorf("in-repo pyramid did not run (no inrepo-proof): %v\noutput:\n%s", err, o.runOut)
	}
	if _, err := os.Stat(filepath.Join(o.checkout, "overlay-proof")); err == nil {
		return fmt.Errorf("overlay ran even though the codebase has an in-repo pyramid (in-repo must win)")
	}
	meta, err := attempt.LoadMeta(store.Root{Dir: o.dataRoot}, ovTicket, ovAtt)
	if err != nil {
		return err
	}
	if strings.TrimSpace(meta.Overlay) != "" {
		return fmt.Errorf("provenance recorded an overlay %q though the in-repo pyramid won", meta.Overlay)
	}
	attachText(sr, "in-repo-wins", "in-repo pyramid ran; overlay ignored\n\n"+o.runOut)
	return nil
}

func ovThenNoOp(w *World, sr *StepRun) error {
	o, err := ovGet(w)
	if err != nil {
		return err
	}
	if o.runErr != nil {
		return fmt.Errorf("draiver test should be a successful no-op but failed: %v\n%s", o.runErr, o.runOut)
	}
	if !strings.Contains(o.runOut, "nothing to test") {
		return fmt.Errorf("expected a 'nothing to test' no-op, got:\n%s", o.runOut)
	}
	attachText(sr, "no-op", o.runOut)
	return nil
}

func ovThenScaffolded(w *World, sr *StepRun) error {
	o, err := ovGet(w)
	if err != nil {
		return err
	}
	if o.runErr != nil {
		return fmt.Errorf("create-overlay failed: %v\n%s", o.runErr, o.runOut)
	}
	target := filepath.Join(o.overlayDir, filepath.Base(o.repoDir))
	if isRepo, err := overlay.IsGitRepo(target); err != nil || !isRepo {
		return fmt.Errorf("expected a scaffolded git repo at %s (isRepo=%v err=%v)", target, isRepo, err)
	}
	if _, err := os.Stat(filepath.Join(target, ".test-pyramid.yaml")); err != nil {
		return fmt.Errorf("scaffolded overlay has no .test-pyramid.yaml: %v", err)
	}
	cfg, err := config.Load(o.configPath)
	if err != nil {
		return err
	}
	if got := cfg.RepoSettings(o.repoDir).Overlay; got != target {
		return fmt.Errorf("per-codebase overlay not registered: got %q, want %q", got, target)
	}
	attachText(sr, "create-overlay", "scaffolded + registered overlay at "+target+"\n\n"+o.runOut)
	return nil
}

func ovThenCreateOverlayRefuses(w *World, sr *StepRun) error {
	o, err := ovGet(w)
	if err != nil {
		return err
	}
	out, rerr := ovRunDraiver(o, "test", "create-overlay")
	if rerr == nil {
		return fmt.Errorf("re-running create-overlay should refuse (non-zero exit), but succeeded:\n%s", out)
	}
	if !strings.Contains(out, "already exists") {
		return fmt.Errorf("refusal should explain an overlay already exists, got:\n%s", out)
	}
	return nil
}
