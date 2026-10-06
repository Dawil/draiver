package web

import (
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
)

// notesOf returns an attempt's `note` events, for asserting a git verb recorded its
// durable trail exactly once and with the expected body.
func notesOf(t *testing.T, root store.Root, id, att string) []event.Event {
	t.Helper()
	events, err := ticketlog.Read(root, id, att)
	if err != nil {
		t.Fatal(err)
	}
	var out []event.Event
	for _, e := range events {
		if e.Type == "note" {
			out = append(out, e)
		}
	}
	return out
}

// TestGitControlsGating pins the drv-013 Git section's preconditions: it shows for
// a Running OR Review attempt that records both a base and a repo (push/sync are
// useful throughout the attempt's life), is absent without a base/repo, and is
// absent once the attempt is Done (its checkout may already be reclaimed).
func TestGitControlsGating(t *testing.T) {
	root := seedBoard(t)
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	// Review attempt with no base/repo → no Git section at all.
	noMeta := get(t, h, "/ticket/PROJ-2/0001").Body.String()
	if strings.Contains(noMeta, `data-testid="git-controls"`) {
		t.Errorf("no Git section without a base/repo")
	}

	// Review attempt WITH base+repo → the section, both buttons, the result region,
	// and both routes.
	writeAttemptMeta(t, root, "PROJ-2", "0001", "/tmp/repo", "main")
	review := get(t, h, "/ticket/PROJ-2/0001").Body.String()
	for _, want := range []string{
		`data-testid="git-controls"`,
		`data-testid="action-git-push"`,
		`data-testid="action-git-sync"`,
		`data-testid="git-result"`,
		`/git/push`,
		`/git/sync`,
		`pull remote main in`,
	} {
		if !strings.Contains(review, want) {
			t.Errorf("Review attempt with base/repo missing %q", want)
		}
	}

	// Running attempt WITH base+repo → the Git section still shows (push/sync are
	// not Review-gated).
	writeAttemptMeta(t, root, "PROJ-3", "0001", "/tmp/repo", "main")
	running := get(t, h, "/ticket/PROJ-3/0001").Body.String()
	if !strings.Contains(running, `data-testid="git-controls"`) {
		t.Errorf("a Running attempt with base/repo should show the Git section")
	}
	// The old "Merged elsewhere — close & pull base" button is gone for good: Sync
	// now owns pulling the remote's base down (drv-013 #12).
	if strings.Contains(review, `data-testid="action-merge-remote"`) {
		t.Errorf("the merge-remote button must no longer render")
	}

	// A Done attempt with base+repo → no Git section.
	seedReviewTicket(t, root, "PROJ-7", "0001", "/tmp/repo", "main")
	if _, err := ticketlog.Append(root, "PROJ-7", "0001", event.Event{Type: "done", Actor: "human:test", Body: "closed"}); err != nil {
		t.Fatal(err)
	}
	done := get(t, h, "/ticket/PROJ-7/0001").Body.String()
	if strings.Contains(done, `data-testid="git-controls"`) {
		t.Errorf("a Done attempt must not show the Git section (its checkout may be reclaimed)")
	}
}

// TestGitPushButtonSendsBranch is the happy path: the attempt branch is not yet on
// the remote, so the button pushes it there (git only, no forge API), records a
// `note`, leaves the attempt non-terminal, and returns the report banner.
func TestGitPushButtonSendsBranch(t *testing.T) {
	root, repo, checkout, branch := remoteReviewAttempt(t, false) // origin has main, not the branch
	featTip := headOfWeb(t, checkout, "HEAD")

	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	rr := post(t, s.Handler(), "/ticket/PROJ-1/0001/git/push")
	if rr.Code != 200 {
		t.Fatalf("POST git/push = %d, want 200\n%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `data-testid="git-result"`) {
		t.Errorf("response missing the git-result banner\n%s", body)
	}
	if !strings.Contains(body, "pushed "+branch) {
		t.Errorf("banner missing the push report\n%s", body)
	}
	// Non-terminal: push does not close the attempt.
	if got := stateOfWeb(t, root); got != project.Review {
		t.Errorf("after push, state = %q, want Review (push is not terminal)", got)
	}
	// A durable note records the push.
	notes := notesOf(t, root, "PROJ-1", "0001")
	if len(notes) != 1 || !strings.Contains(notes[0].Body, "Pushed") {
		t.Errorf("want one `note` recording the push, got %+v", notes)
	}
	// The branch really reached the remote at the feature tip.
	out, err := exec.Command("git", "-C", repo, "ls-remote", "origin", "refs/heads/"+branch).Output()
	if err != nil {
		t.Fatalf("ls-remote: %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(out)), featTip) {
		t.Errorf("remote branch = %q, want tip %s", strings.TrimSpace(string(out)), featTip)
	}
}

// TestGitSyncButtonBackMerges is the happy path for Sync: the base has advanced
// past the branch, so the button back-merges it in (making the branch ff-landable),
// records a `note`, and leaves the attempt non-terminal.
func TestGitSyncButtonBackMerges(t *testing.T) {
	root, repo, _, _ := remoteReviewAttempt(t, false)
	// Advance local main past the branch so there is something to sync in.
	gitInWeb(t, repo, "commit", "-q", "--allow-empty", "-m", "base moved")

	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	rr := post(t, s.Handler(), "/ticket/PROJ-1/0001/git/sync")
	if rr.Code != 200 {
		t.Fatalf("POST git/sync = %d, want 200\n%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `data-testid="git-result"`) || !strings.Contains(body, "ff-landable") {
		t.Errorf("banner missing the sync report\n%s", body)
	}
	if got := stateOfWeb(t, root); got != project.Review {
		t.Errorf("after sync, state = %q, want Review (sync is not terminal)", got)
	}
	notes := notesOf(t, root, "PROJ-1", "0001")
	if len(notes) != 1 || !strings.Contains(notes[0].Body, "Synced") {
		t.Errorf("want one `note` recording the sync, got %+v", notes)
	}
}

// TestGitSyncButtonPullsRemoteIntoLocalBase is the drv-013 #12 fix: the remote's
// base has moved ahead of the local base (a PR merged on the forge), and Sync pulls
// it down — fast-forwarding the local base to the remote tip — instead of the old,
// misleading "already up to date with main". merged=true leaves origin/main at the
// feature tip while local main sits at the initial commit.
func TestGitSyncButtonPullsRemoteIntoLocalBase(t *testing.T) {
	root, repo, checkout, _ := remoteReviewAttempt(t, true)
	remoteTip := headOfWeb(t, checkout, "HEAD") // origin/main == the pushed feature tip
	if got := headOfWeb(t, repo, "main"); got == remoteTip {
		t.Fatalf("precondition: local main should be behind the remote, both at %s", got)
	}

	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	rr := post(t, s.Handler(), "/ticket/PROJ-1/0001/git/sync")
	if rr.Code != 200 {
		t.Fatalf("POST git/sync = %d, want 200\n%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `data-testid="git-result"`) || !strings.Contains(body, "fast-forwarded") {
		t.Errorf("banner should report the local base fast-forwarding to the remote\n%s", body)
	}
	// The #12 fix: local main actually advanced to the remote tip.
	if got := headOfWeb(t, repo, "main"); got != remoteTip {
		t.Errorf("local main = %s, want remote tip %s (pulled)", got, remoteTip)
	}
	if got := stateOfWeb(t, root); got != project.Review {
		t.Errorf("after sync, state = %q, want Review (sync is not terminal)", got)
	}
	notes := notesOf(t, root, "PROJ-1", "0001")
	if len(notes) != 1 || !strings.Contains(notes[0].Body, "Pulled") {
		t.Errorf("want one `note` recording the pull, got %+v", notes)
	}
}

// TestGitSyncNoopRendersBanner is drv-022's no-op outcome: nothing to pull and
// nothing to back-merge records no note, but Sync still renders an explicit
// "already up to date" success banner (kind noop) rather than a blank/edgeless
// panel — the fix for the empty-report case.
func TestGitSyncNoopRendersBanner(t *testing.T) {
	root, _, _, _ := remoteReviewAttempt(t, false) // origin/main == local main; branch already contains main
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	rr := post(t, s.Handler(), "/ticket/PROJ-1/0001/git/sync")
	if rr.Code != 200 {
		t.Fatalf("POST git/sync = %d, want 200\n%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `data-testid="git-result"`) || !strings.Contains(body, `data-kind="noop"`) {
		t.Errorf("no-op should render a kind=noop banner\n%s", body)
	}
	if !strings.Contains(body, "Already up to date") {
		t.Errorf("no-op banner should state 'Already up to date'\n%s", body)
	}
	// A no-op moves nothing, so it records no note.
	if notes := notesOf(t, root, "PROJ-1", "0001"); len(notes) != 0 {
		t.Errorf("a no-op sync recorded %d note(s), want 0", len(notes))
	}
}

// TestGitSyncFailureRendersBanner is drv-022's core fix: a failing sync (here a
// dirty checkout, which the back-merge refuses) renders a failure banner carrying
// the error at HTTP 200 — swapped into the panel via htmx — instead of the old bare
// 500 from s.fail that left the panel blank. The attempt stays non-terminal and
// nothing is recorded.
func TestGitSyncFailureRendersBanner(t *testing.T) {
	root, _, checkout, _ := remoteReviewAttempt(t, false)
	// Dirty the attempt's checkout so worktree.Sync refuses the back-merge
	// (ErrCheckoutDirty) — a realistic operational failure of the Sync control.
	if err := os.WriteFile(filepath.Join(checkout, "scratch.txt"), []byte("wip"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	rr := post(t, s.Handler(), "/ticket/PROJ-1/0001/git/sync")
	if rr.Code != 200 {
		t.Fatalf("POST git/sync = %d, want 200 (the failure must be a banner, not a 500)\n%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `data-testid="git-result"`) || !strings.Contains(body, `data-kind="fail"`) {
		t.Errorf("a failing sync should render a kind=fail banner\n%s", body)
	}
	if !strings.Contains(body, "Sync failed") || !strings.Contains(body, "uncommitted changes") {
		t.Errorf("failure banner should carry the error text\n%s", body)
	}
	// Non-terminal and non-recording: a failed sync leaves the attempt as it was.
	if got := stateOfWeb(t, root); got != project.Review {
		t.Errorf("after a failed sync, state = %q, want Review", got)
	}
	if notes := notesOf(t, root, "PROJ-1", "0001"); len(notes) != 0 {
		t.Errorf("a failed sync recorded %d note(s), want 0", len(notes))
	}
}

// TestGitControlsGuards covers the state-changing routes' guards: a cross-origin
// POST is refused and records nothing, and an unknown attempt 404s — for both verbs.
func TestGitControlsGuards(t *testing.T) {
	root, _, _, _ := remoteReviewAttempt(t, false)
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	for _, verb := range []string{"push", "sync"} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/ticket/PROJ-1/0001/git/"+verb, nil)
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Errorf("cross-origin POST git/%s = %d, want 403", verb, rr.Code)
		}
		if rr := post(t, h, "/ticket/PROJ-1/9999/git/"+verb); rr.Code != 404 {
			t.Errorf("unknown attempt git/%s = %d, want 404", verb, rr.Code)
		}
	}
	// Nothing was recorded by any blocked call.
	if notes := notesOf(t, root, "PROJ-1", "0001"); len(notes) != 0 {
		t.Errorf("a blocked POST recorded %d note(s), want 0", len(notes))
	}
}
