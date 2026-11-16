package reviewenv_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/overlay"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/pyramid"
	"github.com/Dawil/draiver/internal/reviewenv"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/worktree"
)

// drv-053: review envs honour the drv-041 overlay execution contract.

// overlayPyramidYAML declares a review env whose scripts live in the OVERLAY and are
// reached only through $DRAIVER_OVERLAY_DIR. up records the env it saw into a proof
// file under markers, and creates a marker the healthcheck probes; down removes it.
func overlayPyramidYAML(hc string) string {
	return `environments:
  - name: review
    up: sh "$DRAIVER_OVERLAY_DIR/review-env/up.sh"
    down: sh "$DRAIVER_OVERLAY_DIR/review-env/down.sh"
    healthchecks:
      - name: ` + hc + `
        script: sh "$DRAIVER_OVERLAY_DIR/review-env/` + hc + `.sh"
    url: http://localhost:${DRAIVER_REVIEW_PORT}/
levels:
  - name: unit
    run: "true"
`
}

const markerVar = "MARKERS"

// newOverlayRepo builds an overlay repo whose main carries the pyramid + scripts, all
// keyed off $MARKERS (exported into the test process) so the proof lands somewhere
// the test can read.
func newOverlayRepo(t *testing.T, markers string) string {
	t.Helper()
	t.Setenv(markerVar, markers)
	ov := t.TempDir()
	git(t, ov, "init", "-q", "-b", "main")
	git(t, ov, "config", "user.email", "t@t")
	git(t, ov, "config", "user.name", "t")
	writeOverlayFiles(t, ov, "ready", "")
	git(t, ov, "add", "-A")
	git(t, ov, "commit", "-q", "-m", "overlay main")
	return ov
}

// writeOverlayFiles writes the overlay pyramid (healthcheck named hc) and scripts.
// upTail is appended to up.sh (e.g. "exit 1" to make up fail AFTER creating state).
func writeOverlayFiles(t *testing.T, dir, hc, upTail string) {
	t.Helper()
	files := map[string]string{
		".test-pyramid.yaml": overlayPyramidYAML(hc),
		"review-env/up.sh": `printf '%s|%s|%s' "$DRAIVER_CODEBASE_DIR" "$DRAIVER_OVERLAY_DIR" "$PWD" > "$MARKERS/up-env"
touch "$MARKERS/live.$DRAIVER_REVIEW_INSTANCE"
` + upTail + "\n",
		"review-env/down.sh": `printf '%s|%s' "$DRAIVER_CODEBASE_DIR" "$DRAIVER_OVERLAY_DIR" > "$MARKERS/down-env"
echo "down ran"
rm -f "$MARKERS/live.$DRAIVER_REVIEW_INSTANCE"
`,
		"review-env/" + hc + ".sh": `test -n "$DRAIVER_OVERLAY_DIR" && test -f "$MARKERS/live.$DRAIVER_REVIEW_INSTANCE"
`,
	}
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

type relayLine struct {
	up     bool
	out    string
	failed bool
}

// overlaySetup seeds a codebase attempt (no in-repo pyramid) plus an overlay repo, and
// a Manager whose review worktrees land under base and whose relay is captured.
func overlaySetup(t *testing.T) (a project.Attempt, m *reviewenv.Manager, ov, markers, base string, relayed *[]relayLine) {
	repo := newRepo(t)
	root := store.Root{Dir: t.TempDir()}
	a = seedAttempt(t, root, repo, "DRV-53", "0001")
	markers = t.TempDir()
	ov = newOverlayRepo(t, markers)
	base = t.TempDir()
	relayed = &[]relayLine{}
	m = reviewenv.New(reviewenv.Options{Root: root, Base: base, Relay: func(_ project.Attempt, up bool, out string, failed bool) {
		*relayed = append(*relayed, relayLine{up, out, failed})
	}})
	return
}

// checkoutsUnder lists every git checkout (a dir holding a .git file) under base.
func checkoutsUnder(t *testing.T, base string) []string {
	t.Helper()
	var out []string
	filepath.Walk(base, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && info.Name() == ".git" {
			out = append(out, filepath.Dir(p))
		}
		return nil
	})
	return out
}

func revision(t *testing.T, ov string, key worktree.Key) overlay.Revision {
	t.Helper()
	rev, err := overlay.ReviewRevision(context.Background(), overlay.Location{Repo: ov, Via: "explicit"}, key)
	if err != nil {
		t.Fatal(err)
	}
	return rev
}

func envOf(t *testing.T, rev overlay.Revision) *pyramid.Environment {
	t.Helper()
	p, err := overlay.PyramidAt(context.Background(), rev)
	if err != nil || p == nil {
		t.Fatalf("PyramidAt: p=%v err=%v", p, err)
	}
	env, ok := p.EnvironmentByName("review")
	if !ok {
		t.Fatal("no review env in overlay pyramid")
	}
	return env
}

func scriptEnvMap(rec reviewenv.Record) map[string]string {
	out := map[string]string{}
	for _, kv := range rec.ScriptEnv {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			out[kv[:i]] = kv[i+1:]
		}
	}
	return out
}

func TestOverlayLaunchExportsContractAndTeardownReplaysIt(t *testing.T) {
	a, m, ov, markers, _, _ := overlaySetup(t)
	rev := revision(t, ov, worktree.Key{Ticket: a.Ticket, Attempt: a.ID})
	if rev.Ref != "HEAD" {
		t.Fatalf("no overlay branch yet: ref = %q, want HEAD fallback", rev.Ref)
	}

	rec, err := m.Launch(context.Background(), a, envOf(t, rev), nil, &rev)
	if err != nil || rec.State != reviewenv.Up {
		t.Fatalf("Launch: state=%q err=%v msg=%q", rec.State, err, rec.Message)
	}
	if rec.OverlayRepo != ov || rec.OverlayCommit != rev.Commit || rec.OverlayWorktree == "" {
		t.Fatalf("overlay not recorded: %+v", rec)
	}
	// The overlay checkout is detached at exactly the pinned commit.
	if head, err := worktree.HeadSHA(context.Background(), rec.OverlayWorktree); err != nil || head != rev.Commit {
		t.Fatalf("overlay worktree HEAD = %q (%v), want %q", head, err, rev.Commit)
	}
	vars := scriptEnvMap(rec)
	if vars[overlay.EnvCodebaseDir] != rec.Worktree || vars[overlay.EnvOverlayDir] != rec.OverlayWorktree {
		t.Fatalf("ScriptEnv lacks the overlay contract: %v", rec.ScriptEnv)
	}
	proof, _ := os.ReadFile(filepath.Join(markers, "up-env"))
	want := rec.Worktree + "|" + rec.OverlayWorktree + "|" + rec.Worktree
	if string(proof) != want {
		t.Fatalf("up saw %q, want CODEBASE|OVERLAY|PWD = %q", proof, want)
	}

	// Probe replays the overlay env (the healthcheck needs $DRAIVER_OVERLAY_DIR).
	if p, err := m.Probe(context.Background(), a, "review"); err != nil || p.State != reviewenv.Up {
		t.Fatalf("Probe: state=%q err=%v msg=%q", p.State, err, p.Message)
	}

	down, err := m.Teardown(context.Background(), a, "review", "button")
	if err != nil || down.State != reviewenv.Down {
		t.Fatalf("Teardown: state=%q err=%v msg=%q", down.State, err, down.Message)
	}
	dproof, _ := os.ReadFile(filepath.Join(markers, "down-env"))
	if string(dproof) != rec.Worktree+"|"+rec.OverlayWorktree {
		t.Fatalf("down saw %q, want the same overlay env as up", dproof)
	}
	for _, p := range []string{rec.Worktree, rec.OverlayWorktree} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("confirmed teardown left worktree %s", p)
		}
	}
}

func TestOverlayLaunchUsesAttemptOverlayBranchNotMain(t *testing.T) {
	a, m, ov, _, _, _ := overlaySetup(t)
	key := worktree.Key{Ticket: a.Ticket, Attempt: a.ID}
	// The agent's overlay branch renames the healthcheck; main keeps "ready". Uncommitted
	// WIP on overlay main must not leak either.
	git(t, ov, "checkout", "-q", "-b", "draiver/"+key.Ticket+"/"+key.Attempt)
	writeOverlayFiles(t, ov, "branch-ready", "")
	git(t, ov, "add", "-A")
	git(t, ov, "commit", "-q", "-m", "overlay branch edit")
	git(t, ov, "checkout", "-q", "main")
	if err := os.WriteFile(filepath.Join(ov, ".test-pyramid.yaml"), []byte("garbage: [\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rev := revision(t, ov, key)
	if rev.Ref != "draiver/DRV-53/0001" {
		t.Fatalf("ref = %q, want the attempt overlay branch", rev.Ref)
	}
	env := envOf(t, rev)
	if env.Healthchecks[0].Name != "branch-ready" {
		t.Fatalf("definition read from %q, want the overlay branch's", env.Healthchecks[0].Name)
	}
	rec, err := m.Launch(context.Background(), a, env, nil, &rev)
	if err != nil || rec.State != reviewenv.Up {
		t.Fatalf("Launch: state=%q err=%v msg=%q", rec.State, err, rec.Message)
	}
	if len(rec.Healthchecks) != 1 || rec.Healthchecks[0].Name != "branch-ready" {
		t.Fatalf("recorded healthchecks %+v, want the overlay branch's", rec.Healthchecks)
	}
	if _, err := m.Teardown(context.Background(), a, "review", "button"); err != nil {
		t.Fatal(err)
	}
}

func TestOverlayFailedUpRunsDownAndRemovesBothWorktrees(t *testing.T) {
	a, m, ov, markers, base, relayed := overlaySetup(t)
	// up creates state, then fails.
	writeOverlayFiles(t, ov, "ready", "exit 3")
	git(t, ov, "add", "-A")
	git(t, ov, "commit", "-q", "-m", "failing up")
	rev := revision(t, ov, worktree.Key{Ticket: a.Ticket, Attempt: a.ID})

	rec, err := m.Launch(context.Background(), a, envOf(t, rev), nil, &rev)
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != reviewenv.Failed || !strings.Contains(rec.Message, "up failed") {
		t.Fatalf("state=%q msg=%q, want failed up", rec.State, rec.Message)
	}
	if _, err := os.Stat(filepath.Join(markers, "down-env")); err != nil {
		t.Fatal("a failed up did not run down")
	}
	if _, err := os.Stat(filepath.Join(markers, "live."+rec.Instance)); !os.IsNotExist(err) {
		t.Fatal("the half-started env leaked past the failed launch")
	}
	if rec.Worktree != "" || rec.OverlayWorktree != "" {
		t.Fatalf("failed record still names worktrees: %+v", rec)
	}
	if left := checkoutsUnder(t, base); len(left) != 0 {
		t.Fatalf("worktrees left behind under the review base: %v", left)
	}
	// down's output was relayed into System Logs (drv-048) as a Launch-class line.
	sawDown := false
	for _, r := range *relayed {
		if r.up && strings.Contains(r.out, "down ran") {
			sawDown = true
		}
	}
	if !sawDown {
		t.Fatalf("the failed-up down output was not relayed: %+v", *relayed)
	}
}

func TestInRepoLaunchGetsNoOverlayVars(t *testing.T) {
	repo := newRepo(t)
	root := store.Root{Dir: t.TempDir()}
	a := seedAttempt(t, root, repo, "DRV-53", "0002")
	m := reviewenv.New(reviewenv.Options{Root: root, Base: t.TempDir()})
	markers := t.TempDir()
	env := markerEnv(markers)
	env.Up = `printf '%s|%s' "${DRAIVER_CODEBASE_DIR-unset}" "${DRAIVER_OVERLAY_DIR-unset}" > ` + filepath.Join(markers, "seen") + ` && ` + env.Up
	rec, err := m.Launch(context.Background(), a, env, nil, nil)
	if err != nil || rec.State != reviewenv.Up {
		t.Fatalf("Launch: state=%q err=%v msg=%q", rec.State, err, rec.Message)
	}
	defer m.Teardown(context.Background(), a, "review", "test")
	vars := scriptEnvMap(rec)
	if len(vars) != 2 || vars[reviewenv.EnvInstance] == "" || vars[reviewenv.EnvPort] == "" {
		t.Fatalf("in-repo ScriptEnv = %v, want only the DRAIVER_REVIEW_* vars", rec.ScriptEnv)
	}
	if rec.OverlayRepo != "" || rec.OverlayWorktree != "" || rec.OverlayCommit != "" {
		t.Fatalf("in-repo record names an overlay: %+v", rec)
	}
	if seen, _ := os.ReadFile(filepath.Join(markers, "seen")); os.Getenv(overlay.EnvCodebaseDir) == "" && string(seen) != "unset|unset" {
		t.Fatalf("in-repo up saw overlay vars: %q", seen)
	}
}

func TestOverlayParamCannotShadowContractVars(t *testing.T) {
	a, m, ov, _, _, _ := overlaySetup(t)
	// The overlay's env declares a param named like a contract var: it must never shadow
	// the injected value, whatever its default or the operator's override.
	py := overlayPyramidYAML("ready")
	py = strings.Replace(py, "    url:", "    params:\n      - name: DRAIVER_OVERLAY_DIR\n        default: /declared\n    url:", 1)
	if err := os.WriteFile(filepath.Join(ov, ".test-pyramid.yaml"), []byte(py), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, ov, "commit", "-q", "-am", "param named like a contract var")
	rev := revision(t, ov, worktree.Key{Ticket: a.Ticket, Attempt: a.ID})
	rec, err := m.Launch(context.Background(), a, envOf(t, rev), map[string]string{overlay.EnvOverlayDir: "/hack"}, &rev)
	if err != nil || rec.State != reviewenv.Up {
		t.Fatalf("Launch: state=%q err=%v msg=%q", rec.State, err, rec.Message)
	}
	defer m.Teardown(context.Background(), a, "review", "test")
	if got := scriptEnvMap(rec)[overlay.EnvOverlayDir]; got != rec.OverlayWorktree {
		t.Fatalf("DRAIVER_OVERLAY_DIR = %q, want the overlay checkout %q", got, rec.OverlayWorktree)
	}
}

// probeEnv is an in-repo env whose healthcheck EXECUTES a probe script by path, so
// deleting that script makes the probe a deterministic command-not-found (exit 127)
// under any POSIX sh.
func probeEnv(t *testing.T, dir string) *pyramid.Environment {
	t.Helper()
	probe := filepath.Join(dir, "probe.sh")
	if err := os.WriteFile(probe, []byte("#!/bin/sh\ntest -f "+filepath.Join(dir, "live")+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &pyramid.Environment{
		Name:         "review",
		Up:           "touch " + filepath.Join(dir, "live"),
		Down:         "true", // a down that does nothing — the env stays live
		Healthchecks: []pyramid.Healthcheck{{Name: "probe", Script: probe}},
		URL:          "http://localhost:${" + reviewenv.EnvPort + "}/",
	}
}

func TestTeardownMissingProbeIsNotConfirmedDown(t *testing.T) {
	repo := newRepo(t)
	root := store.Root{Dir: t.TempDir()}
	a := seedAttempt(t, root, repo, "DRV-53", "0003")
	m := reviewenv.New(reviewenv.Options{Root: root, Base: t.TempDir()})
	dir := t.TempDir()
	rec, err := m.Launch(context.Background(), a, probeEnv(t, dir), nil, nil)
	if err != nil || rec.State != reviewenv.Up {
		t.Fatalf("Launch: state=%q err=%v msg=%q", rec.State, err, rec.Message)
	}
	// The probe vanishes; the service (the "live" marker) is still up. Before drv-053 the
	// 127 read as "red ⇒ confirmed down" and hid the leak.
	os.Remove(filepath.Join(dir, "probe.sh"))
	down, err := m.Teardown(context.Background(), a, "review", "button")
	if err != nil {
		t.Fatal(err)
	}
	if down.State != reviewenv.TeardownFailed || !strings.Contains(down.Message, "could not run") {
		t.Fatalf("state=%q msg=%q, want teardown-failed (inconclusive probe)", down.State, down.Message)
	}
	if _, err := os.Stat(down.Worktree); err != nil {
		t.Fatalf("an unconfirmed teardown must keep the worktree: %v", err)
	}
}

func TestTeardownVanishedCheckoutIsNotConfirmedDown(t *testing.T) {
	repo := newRepo(t)
	root := store.Root{Dir: t.TempDir()}
	a := seedAttempt(t, root, repo, "DRV-53", "0004")
	m := reviewenv.New(reviewenv.Options{Root: root, Base: t.TempDir()})
	rec, err := m.Launch(context.Background(), a, probeEnv(t, t.TempDir()), nil, nil)
	if err != nil || rec.State != reviewenv.Up {
		t.Fatalf("Launch: state=%q err=%v msg=%q", rec.State, err, rec.Message)
	}
	// The review checkout (the probes' cwd) is gone: the probe cannot even start.
	if err := os.RemoveAll(rec.Worktree); err != nil {
		t.Fatal(err)
	}
	down, err := m.Teardown(context.Background(), a, "review", "button")
	if err != nil {
		t.Fatal(err)
	}
	if down.State != reviewenv.TeardownFailed {
		t.Fatalf("state=%q msg=%q, want teardown-failed when the probe could not start", down.State, down.Message)
	}
}
