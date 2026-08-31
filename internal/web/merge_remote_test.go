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

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
	"github.com/Dawil/draiver/internal/worktree"
)

// writeAttemptMeta writes a minimal attempt.md so an attempt loads with a recorded
// repo+base — the gate the "Merged elsewhere" button renders behind. No git is
// touched; this only exercises the template gating, not the shell-out.
func writeAttemptMeta(t *testing.T, root store.Root, id, att, repo, base string) {
	t.Helper()
	fm := "---\nid: " + att + "\nticket: " + id + "\nrepo: " + repo + "\nbase: " + base + "\n---\n\n"
	if err := os.WriteFile(root.AttemptMetaPath(id, att), []byte(fm), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestMergeRemoteButtonGatedOnBaseAndRepo pins the template gating: the third
// review action shows only for a Review attempt that records both a base and a
// repo (without them there is nothing to fetch/ff, so it degrades to plain Done),
// and never on a non-Review attempt.
func TestMergeRemoteButtonGatedOnBaseAndRepo(t *testing.T) {
	root := seedBoard(t)
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	// PROJ-2/0001 is Review but records no base/repo → no merge-remote button,
	// though the plain Decision/Done actions still render.
	noMeta := get(t, h, "/ticket/PROJ-2/0001").Body.String()
	if strings.Contains(noMeta, `data-testid="action-merge-remote"`) {
		t.Errorf("a Review attempt without base/repo must not show the merge-remote button")
	}
	if strings.Contains(noMeta, `data-testid="merge-remote-result"`) {
		t.Errorf("no result region without the button")
	}
	if !strings.Contains(noMeta, `data-testid="action-done"`) {
		t.Errorf("the plain Done action should still render")
	}

	// Give it a base and repo → the button, its result region, and the base name
	// in the label all appear.
	writeAttemptMeta(t, root, "PROJ-2", "0001", "/tmp/repo", "main")
	withMeta := get(t, h, "/ticket/PROJ-2/0001").Body.String()
	for _, want := range []string{
		`data-testid="action-merge-remote"`,
		`data-testid="merge-remote-result"`,
		`close &amp; pull main`,
		`/merge-remote`,
	} {
		if !strings.Contains(withMeta, want) {
			t.Errorf("Review attempt with base/repo missing %q", want)
		}
	}

	// A Running attempt with base/repo still shows no review actions at all.
	writeAttemptMeta(t, root, "PROJ-3", "0001", "/tmp/repo", "main")
	running := get(t, h, "/ticket/PROJ-3/0001").Body.String()
	if strings.Contains(running, `data-testid="action-merge-remote"`) {
		t.Errorf("a non-Review attempt must not show the merge-remote button")
	}
}

// --- integration helpers (require git) ---

func gitInWeb(t *testing.T, dir string, args ...string) {
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

// seedReviewTicket seeds a Review attempt on disk without the CLI: it writes the
// ticket spec, an attempt.md recording repo+base, and the created→review events —
// the in-process equivalent of `draiver new --repo … && draiver review …`, the
// setup the web layer itself never performs (drv-008: the web tests no longer
// build or spawn a binary).
func seedReviewTicket(t *testing.T, root store.Root, id, att, repo, base string) {
	t.Helper()
	if err := root.EnsureAttemptDirs(id, att); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root.SpecPath(id), []byte("---\nid: "+id+"\ntitle: T\n---\n\n# T\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeAttemptMeta(t, root, id, att, repo, base)
	if _, err := ticketlog.Append(root, id, att, event.Event{Type: "created", Actor: "human:test", Body: "start"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ticketlog.Append(root, id, att, event.Event{Type: "review", Actor: "human:test", Body: "ready"}); err != nil {
		t.Fatal(err)
	}
}

func headOfWeb(t *testing.T, dir, ref string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", ref).Output()
	if err != nil {
		t.Fatalf("rev-parse %s in %s: %v", ref, dir, err)
	}
	return strings.TrimSpace(string(out))
}

func stateOfWeb(t *testing.T, root store.Root) project.State {
	t.Helper()
	a, err := project.LoadAttempt(root, "PROJ-1", "0001")
	if err != nil {
		t.Fatalf("LoadAttempt: %v", err)
	}
	return a.State
}

// remoteReviewAttempt builds a Review attempt targeting a real git repo whose
// `origin` remote mirrors main; with merged=true it also pushes the per-attempt
// branch tip onto origin/main, simulating a PR merged on the forge. It mirrors the
// cmd-package fixture of the same name, so the web tests drive `ctl merge --remote`
// end-to-end through the shell-out.
func remoteReviewAttempt(t *testing.T, merged bool) (root store.Root, repo, checkout, branch string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	// Keep the daemon's managed checkouts off the real cache.
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	repo = t.TempDir()
	gitInWeb(t, repo, "init", "-q", "-b", "main")
	gitInWeb(t, repo, "config", "user.email", "t@t")
	gitInWeb(t, repo, "config", "user.name", "t")
	gitInWeb(t, repo, "commit", "-q", "--allow-empty", "-m", "init")

	data := t.TempDir()
	root = store.Root{Dir: data}
	// Seed a Review attempt recording repo+base=main, in-process (no CLI binary).
	seedReviewTicket(t, root, "PROJ-1", "0001", repo, "main")

	// Materialize the branch + worktree the daemon would cut and put a commit on it.
	m, err := worktree.NewManager(repo)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	wt, err := m.Create(context.Background(), worktree.Spec{Key: worktree.Key{Ticket: "PROJ-1", Attempt: "0001"}})
	if err != nil {
		t.Fatalf("Create worktree: %v", err)
	}
	checkout, branch = wt.Path, wt.Branch
	if err := os.WriteFile(filepath.Join(checkout, "feat.txt"), []byte("feature"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInWeb(t, checkout, "add", "feat.txt")
	gitInWeb(t, checkout, "commit", "-q", "-m", "feat")

	bare := t.TempDir()
	gitInWeb(t, bare, "init", "-q", "--bare", "-b", "main")
	gitInWeb(t, repo, "remote", "add", "origin", bare)
	gitInWeb(t, repo, "push", "-q", "origin", "main")
	if merged {
		gitInWeb(t, repo, "push", "-q", "origin", branch+":refs/heads/main")
	}
	return root, repo, checkout, branch
}

// TestMergeRemoteButtonClosesAndPulls is the happy path end-to-end: the change
// landed via an external PR, so the button records `done` and fast-forwards local
// main, and the response carries both the flipped-to-Done fragment and the verb's
// combined report.
func TestMergeRemoteButtonClosesAndPulls(t *testing.T) {
	root, repo, checkout, _ := remoteReviewAttempt(t, true)
	featTip := headOfWeb(t, checkout, "HEAD")

	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	// The button renders for this Review attempt (it records base+repo).
	if page := get(t, h, "/ticket/PROJ-1/0001").Body.String(); !strings.Contains(page, `data-testid="action-merge-remote"`) {
		t.Fatalf("Review attempt with base+repo should show the merge-remote button")
	}

	rr := post(t, h, "/ticket/PROJ-1/0001/merge-remote")
	if rr.Code != 200 {
		t.Fatalf("POST merge-remote = %d, want 200\n%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()

	if got := stateOfWeb(t, root); got != project.Done {
		t.Errorf("after merge-remote, state = %q, want Done", got)
	}
	// The OOB badge in the response flips to Done.
	if !strings.Contains(body, "state-Done") {
		t.Errorf("live fragment badge should flip to Done\n%s", body)
	}
	// The OOB result banner carries the verb's closed + pull message.
	if !strings.Contains(body, `data-testid="merge-remote-result"`) {
		t.Errorf("response missing the result banner\n%s", body)
	}
	if !strings.Contains(body, "recorded done") || !strings.Contains(body, "fast-forwarded local main") {
		t.Errorf("result banner missing the closed/pull message\n%s", body)
	}
	// The local base actually fast-forwarded to the feature tip.
	if got := headOfWeb(t, repo, "main"); got != featTip {
		t.Errorf("local main = %s, want feature tip %s (pulled)", got, featTip)
	}
}

// TestMergeRemoteButtonReportsPullWarning proves the web-layer contract that a
// zero-exit-with-warning is a success to display, not an error: a dirty local base
// skips the best-effort ff, but the ticket still closes and the warning is shown.
func TestMergeRemoteButtonReportsPullWarning(t *testing.T) {
	root, repo, _, _ := remoteReviewAttempt(t, true)
	if err := os.WriteFile(filepath.Join(repo, "scratch.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	rr := post(t, s.Handler(), "/ticket/PROJ-1/0001/merge-remote")
	if rr.Code != 200 {
		t.Fatalf("POST merge-remote = %d, want 200 (a pull warning is not an error)\n%s", rr.Code, rr.Body.String())
	}
	if got := stateOfWeb(t, root); got != project.Done {
		t.Errorf("state = %q, want Done despite the dirty-base pull warning", got)
	}
	if body := rr.Body.String(); !strings.Contains(body, "warning") {
		t.Errorf("result banner should surface the pull warning\n%s", body)
	}
}

// TestMergeRemoteButtonNotContainedFails pins that a failed containment check (the
// PR was not actually merged) is a real error: with the web's human actor it exits
// nonzero, so the handler 500s and records nothing.
func TestMergeRemoteButtonNotContainedFails(t *testing.T) {
	root, _, _, _ := remoteReviewAttempt(t, false) // never merged upstream
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	rr := post(t, s.Handler(), "/ticket/PROJ-1/0001/merge-remote")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("POST merge-remote (not contained) = %d, want 500\n%s", rr.Code, rr.Body.String())
	}
	if got := stateOfWeb(t, root); got != project.Review {
		t.Errorf("state = %q, want Review (nothing recorded on a refused remote merge)", got)
	}
}

// TestMergeRemoteButtonGuards covers the state-changing route's guards: a
// cross-origin POST is refused (and records nothing) and an unknown attempt 404s.
func TestMergeRemoteButtonGuards(t *testing.T) {
	root, _, _, _ := remoteReviewAttempt(t, true)
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/ticket/PROJ-1/0001/merge-remote", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("cross-origin POST = %d, want 403", rr.Code)
	}
	if got := stateOfWeb(t, root); got != project.Review {
		t.Errorf("a blocked POST changed state to %q", got)
	}

	if rr := post(t, h, "/ticket/PROJ-1/9999/merge-remote"); rr.Code != 404 {
		t.Errorf("unknown attempt = %d, want 404", rr.Code)
	}
}
