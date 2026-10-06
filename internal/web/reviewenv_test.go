package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/config"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/pyramid"
	"github.com/Dawil/draiver/internal/reviewenv"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
	"github.com/Dawil/draiver/internal/worktree"
)

// reviewReadyPyramid is a pyramid carrying one review-ready environment (up + down +
// a healthcheck) named "review", backed by a marker file namespaced by the injected
// instance+port — the container-free stand-in the package tests also use. Its URL
// template encodes the allocated port, so a green launch reveals a distinct link.
func reviewReadyPyramid(markerDir string) *pyramid.Pyramid {
	marker := filepath.Join(markerDir, "$"+reviewenv.EnvInstance+".$"+reviewenv.EnvPort)
	return &pyramid.Pyramid{
		Environments: []pyramid.Environment{{
			Name:         "review",
			Up:           "touch " + marker,
			Down:         "rm -f " + marker,
			Healthchecks: []pyramid.Healthcheck{{Name: "marker", Script: "test -f " + marker}},
			URL:          "http://127.0.0.1:${" + reviewenv.EnvPort + "}/",
		}},
	}
}

// reviewReadyParamPyramid is a review-ready "review" env that ALSO declares a settable
// ENV_NAME param (default "review"), backed by a marker file named by the resolved
// ENV_NAME — so a launch with a custom value writes a differently-named marker, proving
// the web-settable override (resolution #45) reached the scripts.
func reviewReadyParamPyramid(markerDir string) *pyramid.Pyramid {
	marker := filepath.Join(markerDir, "$ENV_NAME")
	return &pyramid.Pyramid{
		Environments: []pyramid.Environment{{
			Name:         "review",
			Up:           "touch " + marker,
			Down:         "rm -f " + marker,
			Healthchecks: []pyramid.Healthcheck{{Name: "marker", Script: "test -f " + marker}},
			URL:          "http://127.0.0.1:${" + reviewenv.EnvPort + "}/",
			Params:       []pyramid.Param{{Name: "ENV_NAME", Default: "review"}},
		}},
	}
}

// weakPyramid carries an environment named "review" that is NOT review-ready (no down,
// no healthcheck) — the misconfiguration the panel must flag loudly.
func weakPyramid() *pyramid.Pyramid {
	return &pyramid.Pyramid{
		Environments: []pyramid.Environment{{Name: "review", Up: "true"}},
	}
}

// serverWithReviewEnv seeds a Review attempt on repo, points the repo's
// reviewEnvironment config at `envName`, and returns a server whose git layer is the
// supplied stub — so the visibility gate renders off a canned pyramid with no real
// repo. A blank envName leaves reviewEnvironment unset.
func serverWithReviewEnv(t *testing.T, repo, envName string, git func(context.Context, project.Attempt) (pyramidState, bool), state project.State) *Server {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("DRAIVER_CONFIG", path)
	if envName != "" {
		if _, err := config.SetRepoSettings(path, repo, config.RepoSettingsUpdate{ReviewEnvironment: &envName}); err != nil {
			t.Fatal(err)
		}
	}
	root := store.Root{Dir: t.TempDir()}
	if err := root.EnsureAttemptDirs("PROJ-9", "0001"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root.SpecPath("PROJ-9"), []byte("---\nid: PROJ-9\ntitle: T\n---\n\n# T\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := attempt.WriteMeta(root, attempt.Meta{ID: "0001", Ticket: "PROJ-9", Repo: repo, Base: "main", Actor: "human:t"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ticketlog.Append(root, "PROJ-9", "0001", event.Event{Type: "created", Actor: "a", Body: "start"}); err != nil {
		t.Fatal(err)
	}
	// A "review" event puts the attempt in Review; anything else leaves it Running —
	// the gate's state axis.
	if state == project.Review {
		if _, err := ticketlog.Append(root, "PROJ-9", "0001", event.Event{Type: "review", Actor: "a", Body: "ready"}); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	s.pyramidGit = git
	return s
}

// TestReviewEnvVisibilityGate pins the spec's three-part visibility gate: the panel is
// offered only when the attempt is in Review, the repo names a reviewEnvironment, and
// that environment is review-ready. Every other combination renders no launcher.
func TestReviewEnvVisibilityGate(t *testing.T) {
	ready := mockGit(reviewReadyPyramid(t.TempDir()), "deadbeef", false, true)

	t.Run("review+reviewenv+review-ready shows the launcher", func(t *testing.T) {
		s := serverWithReviewEnv(t, "/mock/repo", "review", ready, project.Review)
		body := get(t, s.Handler(), "/ticket/PROJ-9/0001").Body.String()
		for _, want := range []string{
			`data-testid="review-env"`,
			`data-testid="review-env-name"`,
			`<code data-testid="review-env-name">review</code>`,
			`data-testid="action-review-env-up"`, // the Launch button
			`data-testid="review-env-state"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("review-ready panel missing %q", want)
			}
		}
		if strings.Contains(body, `data-testid="review-env-misconfigured"`) {
			t.Error("a review-ready env should not render the misconfiguration note")
		}
	})

	t.Run("not in review renders no panel", func(t *testing.T) {
		s := serverWithReviewEnv(t, "/mock/repo", "review", ready, project.Running)
		body := get(t, s.Handler(), "/ticket/PROJ-9/0001").Body.String()
		if strings.Contains(body, `data-testid="review-env"`) {
			t.Error("a non-Review attempt must not offer the review-env panel")
		}
	})

	t.Run("no reviewEnvironment set renders no panel", func(t *testing.T) {
		s := serverWithReviewEnv(t, "/mock/repo", "", ready, project.Review)
		body := get(t, s.Handler(), "/ticket/PROJ-9/0001").Body.String()
		if strings.Contains(body, `data-testid="review-env"`) {
			t.Error("with no reviewEnvironment set the panel must not appear")
		}
	})

	t.Run("reviewEnvironment names a non-review-ready env flags it", func(t *testing.T) {
		weak := mockGit(weakPyramid(), "deadbeef", false, true)
		s := serverWithReviewEnv(t, "/mock/repo", "review", weak, project.Review)
		body := get(t, s.Handler(), "/ticket/PROJ-9/0001").Body.String()
		if !strings.Contains(body, `data-testid="review-env-misconfigured"`) {
			t.Error("a non-review-ready reviewEnvironment should render the loud misconfiguration note")
		}
		if strings.Contains(body, `data-testid="action-review-env-up"`) {
			t.Error("a misconfigured env must not offer a Launch button")
		}
	})

	t.Run("reviewEnvironment names a missing env flags it", func(t *testing.T) {
		missing := mockGit(reviewReadyPyramid(t.TempDir()), "deadbeef", false, true)
		s := serverWithReviewEnv(t, "/mock/repo", "nope", missing, project.Review)
		body := get(t, s.Handler(), "/ticket/PROJ-9/0001").Body.String()
		if !strings.Contains(body, `data-testid="review-env-misconfigured"`) {
			t.Error("a reviewEnvironment naming no declared env should render the misconfiguration note")
		}
	})
}

// TestReviewEnvLaunchFormRendersParamInputs pins the resolution-#45 launch-form surface:
// a review env that declares an ENV_NAME param renders it as an editable input, pre-filled
// with the declared default, inside the launch form the Launch button includes.
func TestReviewEnvLaunchFormRendersParamInputs(t *testing.T) {
	ready := mockGit(reviewReadyParamPyramid(t.TempDir()), "deadbeef", false, true)
	s := serverWithReviewEnv(t, "/mock/repo", "review", ready, project.Review)
	body := get(t, s.Handler(), "/ticket/PROJ-9/0001").Body.String()
	for _, want := range []string{
		`data-testid="review-env-up-form"`,
		`data-testid="review-env-param-input-ENV_NAME"`,
		`name="param:ENV_NAME" value="review"`, // declared default pre-fills the input
	} {
		if !strings.Contains(body, want) {
			t.Errorf("launch form missing %q", want)
		}
	}
}

// TestReviewEnvUpThreadsParamOverride drives the whole web seam for a custom param: a
// POST /review-env/up carrying param:ENV_NAME=<custom> threads that value into the async
// Launch, and the up script (a marker named by the resolved ENV_NAME) proves the custom
// value reached the scripts — not the declared default.
func TestReviewEnvUpThreadsParamOverride(t *testing.T) {
	markers := t.TempDir()
	s, _, _ := seedReviewRepo(t, reviewReadyParamPyramid(markers))
	h := s.Handler()

	form := "param:ENV_NAME=custom-value"
	req := httptest.NewRequest(http.MethodPost, "/ticket/PROJ-9/0001/review-env/up", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("POST up = %d: %s", rr.Code, rr.Body.String())
	}

	// Poll status until the async launch goes up (URL revealed) or fails.
	var last string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		last = getReviewEnvStatus(t, h).Body.String()
		if strings.Contains(last, `data-testid="review-env-url"`) {
			break
		}
		if strings.Contains(last, "launch failed") {
			t.Fatalf("launch failed:\n%s", last)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(last, `data-testid="review-env-url"`) {
		t.Fatalf("review env never came up; last panel:\n%s", last)
	}

	// The up script created the marker named by the custom ENV_NAME — not the default.
	if _, err := os.Stat(filepath.Join(markers, "custom-value")); err != nil {
		t.Fatalf("custom ENV_NAME did not reach the up script: %v", err)
	}
	if _, err := os.Stat(filepath.Join(markers, "review")); !os.IsNotExist(err) {
		t.Errorf("the default ENV_NAME marker must not exist when the operator overrode it")
	}
	postReviewEnv(t, h, "down") // best-effort cleanup
}

// TestRepoSettingsReviewEnvFieldPersists pins the drv-020 repo-settings panel field:
// it renders as an input and a POST persists it to the per-repo config, keyed by the
// attempt's repo, like the other repo defaults.
func TestRepoSettingsReviewEnvFieldPersists(t *testing.T) {
	path := isolateConfig(t)
	root := seedBoard(t)
	writeAttemptMeta(t, root, "PROJ-3", "0001", "/tmp/repo", "main")

	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	// The field renders in the panel.
	body := get(t, h, "/ticket/PROJ-3/0001").Body.String()
	if !strings.Contains(body, `data-testid="repo-settings-review-env"`) {
		t.Fatal("repo-settings panel missing the review-env field")
	}

	// Posting it persists to the repo's config entry.
	form := "remote=&branch=&rung=&review_env=review"
	req := httptest.NewRequest(http.MethodPost, "/ticket/PROJ-3/0001/repo-settings", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("POST repo-settings = %d: %s", rr.Code, rr.Body.String())
	}
	if got := loadRepoSettings(t, path, "/tmp/repo").ReviewEnvironment; got != "review" {
		t.Fatalf("ReviewEnvironment persisted = %q, want %q", got, "review")
	}
	if !strings.Contains(rr.Body.String(), `value="review"`) {
		t.Error("the swapped-in panel should prefill the saved review env")
	}
}

// --- full HTTP lifecycle against a real repo -----------------------------

func gitCmd(t *testing.T, dir string, args ...string) {
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

// seedReviewRepo builds a real git repo with a Review attempt whose coding
// worktree/branch carries a commit — so the review-env launcher can resolve a reviewed
// commit and cut a detached review worktree at it. It returns the server (git layer
// pointed at the given pyramid), the root, and the loaded attempt.
func seedReviewRepo(t *testing.T, p *pyramid.Pyramid) (*Server, store.Root, project.Attempt) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir()) // keep managed + review worktrees off the real cache

	repo := t.TempDir()
	gitCmd(t, repo, "init", "-q", "-b", "main")
	gitCmd(t, repo, "config", "user.email", "t@t")
	gitCmd(t, repo, "config", "user.name", "t")
	gitCmd(t, repo, "commit", "-q", "--allow-empty", "-m", "init")

	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("DRAIVER_CONFIG", path)
	name := "review"
	if _, err := config.SetRepoSettings(path, repo, config.RepoSettingsUpdate{ReviewEnvironment: &name}); err != nil {
		t.Fatal(err)
	}

	root := store.Root{Dir: t.TempDir()}
	if err := root.EnsureAttemptDirs("PROJ-9", "0001"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root.SpecPath("PROJ-9"), []byte("---\nid: PROJ-9\ntitle: T\n---\n\n# T\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := attempt.WriteMeta(root, attempt.Meta{ID: "0001", Ticket: "PROJ-9", Repo: repo, Base: "main", Actor: "human:t"}); err != nil {
		t.Fatal(err)
	}
	ticketlog.Append(root, "PROJ-9", "0001", event.Event{Type: "created", Actor: "a", Body: "start"})
	ticketlog.Append(root, "PROJ-9", "0001", event.Event{Type: "review", Actor: "a", Body: "ready"})

	// Cut the coding worktree + per-attempt branch with a commit, so reviewedCommit
	// resolves (the land path the real daemon walks).
	wm, err := worktree.NewManager(repo)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := wm.Create(context.Background(), worktree.Spec{Key: worktree.Key{Ticket: "PROJ-9", Attempt: "0001"}})
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(wt.Path, "f.txt"), []byte("feature"), 0o644)
	gitCmd(t, wt.Path, "add", ".")
	gitCmd(t, wt.Path, "commit", "-q", "-m", "feat")

	a, err := project.LoadAttempt(root, "PROJ-9", "0001")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	// The gate reads the pyramid through pyramidGit; point it at the supplied one so
	// the test controls the environment without committing a .test-pyramid.yaml.
	s.pyramidGit = mockGit(p, "deadbeef", false, true)
	return s, root, a
}

func postReviewEnv(t *testing.T, h http.Handler, verb string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/ticket/PROJ-9/0001/review-env/"+verb, nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func getReviewEnvStatus(t *testing.T, h http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	return get(t, h, "/ticket/PROJ-9/0001/review-env/status")
}

// TestReviewEnvLifecycleOverHTTP drives the whole panel seam through real HTTP: launch
// (async → starting), poll status until the healthcheck goes green and the URL is
// revealed, then tear down (synchronous) and confirm the panel returns to down with no
// URL. This proves the webui wiring end-to-end over the already-unit-tested lifecycle.
func TestReviewEnvLifecycleOverHTTP(t *testing.T) {
	markers := t.TempDir()
	s, _, _ := seedReviewRepo(t, reviewReadyPyramid(markers))
	h := s.Handler()

	// Launch: async, so the immediate response is the starting state and self-polls.
	up := postReviewEnv(t, h, "up")
	if up.Code != 200 {
		t.Fatalf("POST up = %d: %s", up.Code, up.Body.String())
	}
	if !strings.Contains(up.Body.String(), `review-env/status`) {
		t.Error("starting panel should self-poll the status route")
	}

	// Poll status until up (URL revealed) or failed.
	var last string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		body := getReviewEnvStatus(t, h).Body.String()
		last = body
		if strings.Contains(body, `data-testid="review-env-url"`) {
			break
		}
		if strings.Contains(body, "launch failed") {
			t.Fatalf("launch failed:\n%s", body)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(last, `data-testid="review-env-url"`) {
		t.Fatalf("review env never revealed a URL; last panel:\n%s", last)
	}
	if !strings.Contains(last, `href="http://127.0.0.1:`) {
		t.Errorf("revealed URL is not the port-encoded link; panel:\n%s", last)
	}

	// Tear down: synchronous, returns the confirmed-down panel.
	down := postReviewEnv(t, h, "down")
	if down.Code != 200 {
		t.Fatalf("POST down = %d: %s", down.Code, down.Body.String())
	}
	db := down.Body.String()
	if strings.Contains(db, `data-testid="review-env-url"`) {
		t.Errorf("a torn-down env must not show a URL; panel:\n%s", db)
	}
	if !strings.Contains(db, `data-testid="action-review-env-up"`) {
		t.Errorf("a down env should offer Launch again; panel:\n%s", db)
	}
}

// TestReviewEnvUpRejectedWhenNotGated pins that a POST that bypasses the UI gate (an
// attempt whose repo configures no review-ready environment) is a 400, not a 500 — the
// server re-checks the gate the affordance renders behind.
func TestReviewEnvUpRejectedWhenNotGated(t *testing.T) {
	// reviewEnvironment points at a non-review-ready env.
	s := serverWithReviewEnv(t, "/mock/repo", "review", mockGit(weakPyramid(), "x", false, true), project.Review)
	rr := postReviewEnv(t, s.Handler(), "up")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("POST up on an ungated attempt = %d, want 400", rr.Code)
	}
}
