package reviewenv_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/pyramid"
	"github.com/Dawil/draiver/internal/reviewenv"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
	"github.com/Dawil/draiver/internal/worktree"
)

// --- fixtures ------------------------------------------------------------

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

// seedAttempt builds a real git repo with a Review attempt and the coding
// worktree/branch the daemon would cut (so the reviewed commit resolves), and returns
// the loaded attempt. Mirrors internal/web/git_fixtures_test.go's recipe.
func seedAttempt(t *testing.T, root store.Root, repo, id, att string) project.Attempt {
	t.Helper()
	if err := root.EnsureAttemptDirs(id, att); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root.SpecPath(id), []byte("---\nid: "+id+"\ntitle: T\n---\n\n# T\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fm := "---\nid: " + att + "\nticket: " + id + "\nrepo: " + repo + "\nbase: main\n---\n\n"
	if err := os.WriteFile(root.AttemptMetaPath(id, att), []byte(fm), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ticketlog.Append(root, id, att, event.Event{Type: "created", Actor: "human:test", Body: "start"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ticketlog.Append(root, id, att, event.Event{Type: "review", Actor: "human:test", Body: "ready"}); err != nil {
		t.Fatal(err)
	}

	// Cut the coding worktree + per-attempt branch and put a commit on it, so the
	// review env has a reviewed commit to check out.
	m, err := worktree.NewManager(repo)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	wt, err := m.Create(context.Background(), worktree.Spec{Key: worktree.Key{Ticket: id, Attempt: att}})
	if err != nil {
		t.Fatalf("Create worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, att+".txt"), []byte("feature"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, wt.Path, "add", ".")
	git(t, wt.Path, "commit", "-q", "-m", "feat")

	a, err := project.LoadAttempt(root, id, att)
	if err != nil {
		t.Fatalf("LoadAttempt: %v", err)
	}
	return a
}

// newRepo returns an initialized git repo with one commit on main.
func newRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir()) // keep managed coding worktrees off the real cache
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	git(t, repo, "config", "user.email", "t@t")
	git(t, repo, "config", "user.name", "t")
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	return repo
}

// markerEnv is a review environment backed by a marker file under dir: `up` creates
// it (namespaced by the injected instance+port), the healthcheck probes for it, and
// `down` removes it. It is the deterministic, container-free stand-in for a real
// service standing up and tearing down.
func markerEnv(dir string) *pyramid.Environment {
	marker := filepath.Join(dir, "$"+reviewenv.EnvInstance+".$"+reviewenv.EnvPort)
	return &pyramid.Environment{
		Name:         "review",
		Up:           "touch " + marker,
		Down:         "rm -f " + marker,
		Healthchecks: []pyramid.Healthcheck{{Name: "marker", Script: "test -f " + marker}},
		URL:          "http://localhost:${" + reviewenv.EnvPort + "}/",
	}
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

// --- tests ---------------------------------------------------------------

func TestLaunchRevealsURLOnGreen(t *testing.T) {
	repo := newRepo(t)
	root := store.Root{Dir: t.TempDir()}
	a := seedAttempt(t, root, repo, "DRV-1", "0001")
	markers := t.TempDir()

	m := reviewenv.New(reviewenv.Options{Root: root, Base: t.TempDir()})
	rec, err := m.Launch(context.Background(), a, markerEnv(markers), nil)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if rec.State != reviewenv.Up {
		t.Fatalf("state = %q, want up; message=%q", rec.State, rec.Message)
	}
	if rec.Port == 0 {
		t.Fatal("no port allocated")
	}
	wantURL := "http://localhost:" + strconv.Itoa(rec.Port) + "/"
	if rec.URL != wantURL {
		t.Fatalf("url = %q, want %q", rec.URL, wantURL)
	}
	// The worktree exists and is a detached checkout at the reviewed commit.
	if _, err := os.Stat(rec.Worktree); err != nil {
		t.Fatalf("review worktree missing: %v", err)
	}
	if rec.Commit == "" {
		t.Fatal("no reviewed commit recorded")
	}
	// A reload sees the same persisted Up record (rebuildable from disk alone).
	got, ok, err := m.Load("DRV-1", "0001")
	if err != nil || !ok || got.State != reviewenv.Up || got.URL != wantURL {
		t.Fatalf("reload: ok=%v state=%q url=%q err=%v", ok, got.State, got.URL, err)
	}
}

func TestLaunchFailsOnRedHealthcheck(t *testing.T) {
	repo := newRepo(t)
	root := store.Root{Dir: t.TempDir()}
	a := seedAttempt(t, root, repo, "DRV-1", "0001")

	env := &pyramid.Environment{
		Name:         "review",
		Up:           "true", // up succeeds but never makes the service ready
		Down:         "true",
		Healthchecks: []pyramid.Healthcheck{{Name: "never", Script: "false"}},
		URL:          "http://localhost:${" + reviewenv.EnvPort + "}/",
	}
	m := reviewenv.New(reviewenv.Options{Root: root, Base: t.TempDir()})
	rec, err := m.Launch(context.Background(), a, env, nil)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if rec.State != reviewenv.Failed {
		t.Fatalf("state = %q, want failed", rec.State)
	}
	if rec.URL != "" {
		t.Fatalf("failed launch revealed a url: %q", rec.URL)
	}
	if !strings.Contains(rec.Message, "never") {
		t.Fatalf("message does not name the red probe: %q", rec.Message)
	}
	// A failed launch leaves nothing half-live: the worktree is torn back down.
	if rec.Worktree != "" {
		if _, err := os.Stat(rec.Worktree); !os.IsNotExist(err) {
			t.Fatalf("failed launch leaked a worktree at %s", rec.Worktree)
		}
	}
}

func TestConcurrentEnvsGetDistinctPorts(t *testing.T) {
	repo := newRepo(t)
	root := store.Root{Dir: t.TempDir()}
	a1 := seedAttempt(t, root, repo, "DRV-1", "0001")
	a2 := seedAttempt(t, root, repo, "DRV-2", "0001")
	markers := t.TempDir()
	m := reviewenv.New(reviewenv.Options{Root: root, Base: t.TempDir()})

	r1, err := m.Launch(context.Background(), a1, markerEnv(markers), nil)
	if err != nil || r1.State != reviewenv.Up {
		t.Fatalf("launch a1: state=%q err=%v msg=%q", r1.State, err, r1.Message)
	}
	r2, err := m.Launch(context.Background(), a2, markerEnv(markers), nil)
	if err != nil || r2.State != reviewenv.Up {
		t.Fatalf("launch a2: state=%q err=%v msg=%q", r2.State, err, r2.Message)
	}
	if r1.Port == r2.Port {
		t.Fatalf("two concurrent review envs collided on port %d", r1.Port)
	}
	if r1.Instance == r2.Instance {
		t.Fatalf("two attempts got the same instance slug %q", r1.Instance)
	}
	if r1.URL == r2.URL {
		t.Fatalf("two concurrent review envs produced the same url %q", r1.URL)
	}
	// Both are independently Up — one did not clobber the other's checkout/marker.
	if g1, _ := m.Probe(context.Background(), a1); g1.State != reviewenv.Up {
		t.Fatalf("a1 not up after a2 launched: %q", g1.State)
	}
	if g2, _ := m.Probe(context.Background(), a2); g2.State != reviewenv.Up {
		t.Fatalf("a2 not up: %q", g2.State)
	}
}

func TestTeardownConfirmedDown(t *testing.T) {
	repo := newRepo(t)
	root := store.Root{Dir: t.TempDir()}
	a := seedAttempt(t, root, repo, "DRV-1", "0001")
	markers := t.TempDir()
	m := reviewenv.New(reviewenv.Options{Root: root, Base: t.TempDir()})

	up, err := m.Launch(context.Background(), a, markerEnv(markers), nil)
	if err != nil || up.State != reviewenv.Up {
		t.Fatalf("launch: %q %v", up.State, err)
	}
	wt := up.Worktree

	rec, err := m.Teardown(context.Background(), a, "button")
	if err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if rec.State != reviewenv.Down {
		t.Fatalf("state = %q, want down; message=%q", rec.State, rec.Message)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("worktree not removed on confirmed teardown: %v", err)
	}
}

func TestTeardownLeakDetection(t *testing.T) {
	repo := newRepo(t)
	root := store.Root{Dir: t.TempDir()}
	a := seedAttempt(t, root, repo, "DRV-1", "0001")
	markers := t.TempDir()

	env := markerEnv(markers)
	env.Down = "true" // a down that does NOT actually tear the service down

	m := reviewenv.New(reviewenv.Options{Root: root, Base: t.TempDir()})
	up, err := m.Launch(context.Background(), a, env, nil)
	if err != nil || up.State != reviewenv.Up {
		t.Fatalf("launch: %q %v", up.State, err)
	}

	rec, err := m.Teardown(context.Background(), a, "button")
	if err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if rec.State != reviewenv.TeardownFailed {
		t.Fatalf("state = %q, want teardown-failed", rec.State)
	}
	if !strings.Contains(rec.Message, "leak") {
		t.Fatalf("leak not surfaced in message: %q", rec.Message)
	}
	// The worktree is kept so a human (or retry) can finish the job.
	if rec.Worktree == "" {
		t.Fatal("leak record dropped the worktree reference")
	}
}

func TestReaperTripsOnIdle(t *testing.T) {
	repo := newRepo(t)
	root := store.Root{Dir: t.TempDir()}
	a := seedAttempt(t, root, repo, "DRV-1", "0001")
	markers := t.TempDir()

	clk := &clock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	m := reviewenv.New(reviewenv.Options{
		Root:   root,
		Base:   t.TempDir(),
		MaxAge: 30 * time.Minute,
		Now:    clk.now,
	})
	up, err := m.Launch(context.Background(), a, markerEnv(markers), nil)
	if err != nil || up.State != reviewenv.Up {
		t.Fatalf("launch: %q %v", up.State, err)
	}

	// Not yet idle: a sweep leaves it running.
	clk.t = clk.t.Add(20 * time.Minute)
	if reaped, err := m.Sweep(context.Background()); err != nil || len(reaped) != 0 {
		t.Fatalf("premature reap: %v reaped=%v", err, reaped)
	}
	if rec, _, _ := m.Load("DRV-1", "0001"); rec.State != reviewenv.Up {
		t.Fatalf("env not up before idle window elapses: %q", rec.State)
	}

	// Past the window: the reaper tears it down.
	clk.t = clk.t.Add(20 * time.Minute)
	reaped, err := m.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(reaped) != 1 {
		t.Fatalf("idle env not reaped: %v", reaped)
	}
	if rec, _, _ := m.Load("DRV-1", "0001"); rec.State != reviewenv.Down {
		t.Fatalf("state after reap = %q, want down", rec.State)
	}
}

func TestProbeFlipsToUnhealthy(t *testing.T) {
	repo := newRepo(t)
	root := store.Root{Dir: t.TempDir()}
	a := seedAttempt(t, root, repo, "DRV-1", "0001")
	markers := t.TempDir()
	m := reviewenv.New(reviewenv.Options{Root: root, Base: t.TempDir()})

	up, err := m.Launch(context.Background(), a, markerEnv(markers), nil)
	if err != nil || up.State != reviewenv.Up {
		t.Fatalf("launch: %q %v", up.State, err)
	}
	// The service dies under it: drop every marker file.
	entries, _ := os.ReadDir(markers)
	for _, e := range entries {
		os.Remove(filepath.Join(markers, e.Name()))
	}
	rec, err := m.Probe(context.Background(), a)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if rec.State != reviewenv.Unhealthy {
		t.Fatalf("state = %q, want unhealthy", rec.State)
	}
}

// TestLaunchParamOverride pins the resolution-#45 parameterisation contract: a declared
// param resolves to the operator's launch-time override, the injected DRAIVER_REVIEW_*
// vars still win over both a param default and an override attempt, an override for an
// undeclared name is ignored, and the resolved value is captured in ScriptEnv for
// teardown replay.
func TestLaunchParamOverride(t *testing.T) {
	repo := newRepo(t)
	root := store.Root{Dir: t.TempDir()}
	a := seedAttempt(t, root, repo, "DRV-1", "0001")
	dir := t.TempDir()
	out := filepath.Join(dir, "params.txt")

	env := &pyramid.Environment{
		Name:         "review",
		Up:           "printf '%s\\n%s\\n' \"$ENV_NAME\" \"$" + reviewenv.EnvInstance + "\" > " + out,
		Down:         "true",
		Healthchecks: []pyramid.Healthcheck{{Name: "ok", Script: "test -f " + out}},
		URL:          "http://localhost:${" + reviewenv.EnvPort + "}/",
		Params: []pyramid.Param{
			{Name: "ENV_NAME", Default: "review"},
			{Name: reviewenv.EnvInstance, Default: "SHOULD-BE-IGNORED"}, // cannot shadow the injected var
		},
	}
	m := reviewenv.New(reviewenv.Options{Root: root, Base: t.TempDir()})
	rec, err := m.Launch(context.Background(), a, env, map[string]string{
		"ENV_NAME":            "staging",
		reviewenv.EnvInstance: "HACK", // override of an injected var: must be ignored
		"UNDECLARED":          "x",     // not a declared param: must be ignored
	})
	if err != nil || rec.State != reviewenv.Up {
		t.Fatalf("launch: state=%q err=%v msg=%q", rec.State, err, rec.Message)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read params: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("params file = %q, want two lines", string(data))
	}
	if lines[0] != "staging" {
		t.Errorf("ENV_NAME override = %q, want %q", lines[0], "staging")
	}
	// The injected instance won over both its param default and the override attempt.
	if lines[1] != rec.Instance {
		t.Errorf("injected %s = %q, want %q (not overridable)", reviewenv.EnvInstance, lines[1], rec.Instance)
	}
	// The resolved override is captured in ScriptEnv so teardown replays the same value.
	found := false
	for _, kv := range rec.ScriptEnv {
		if kv == "ENV_NAME=staging" {
			found = true
		}
		if kv == "UNDECLARED=x" {
			t.Errorf("an undeclared override leaked into the script env: %v", rec.ScriptEnv)
		}
	}
	if !found {
		t.Errorf("ScriptEnv did not capture ENV_NAME=staging: %v", rec.ScriptEnv)
	}
}

func TestLaunchRejectsNonReviewReadyEnv(t *testing.T) {
	repo := newRepo(t)
	root := store.Root{Dir: t.TempDir()}
	a := seedAttempt(t, root, repo, "DRV-1", "0001")

	// Missing down + healthcheck: a generic drv-012 env, not review-ready.
	env := &pyramid.Environment{Name: "weak", Up: "true"}
	m := reviewenv.New(reviewenv.Options{Root: root, Base: t.TempDir()})
	if _, err := m.Launch(context.Background(), a, env, nil); err == nil {
		t.Fatal("expected Launch to refuse a non-review-ready env")
	}
}

