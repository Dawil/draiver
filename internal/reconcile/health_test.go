package reconcile_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/reconcile"
	"github.com/Dawil/draiver/internal/session"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// gitIn runs a git command in dir with a deterministic identity, failing the test
// on error — the primitive the health tests set up a worktree clash with.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// readHealth parses an attempt's session/ctl.jsonl into its health transitions. A
// missing file (no trouble ever recorded) reads as an empty slice, not an error.
func readHealth(t *testing.T, root store.Root, ticket, att string) []reconcile.HealthEvent {
	t.Helper()
	data, err := os.ReadFile(root.SessionCtlLogPath(ticket, att))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read ctl.jsonl: %v", err)
	}
	var out []reconcile.HealthEvent
	for _, ln := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if ln == "" {
			continue
		}
		var e reconcile.HealthEvent
		if err := json.Unmarshal([]byte(ln), &e); err != nil {
			t.Fatalf("bad health line %q: %v", ln, err)
		}
		out = append(out, e)
	}
	return out
}

// TestHealthWorktreeClashEdgeTriggered is the drvctl-027 core: an attempt whose
// branch is already checked out in another worktree fails admit every tick, and
// that wedge is made observable on the attempt's ctl.jsonl as edge-triggered
// transitions — one error-start when the trouble begins (NOT one line per failed
// tick), and one error-end when it clears. This is the exact silent-wedge symptom
// the ticket exists to kill (drvctl-025/0002 sat wedged with nothing surfaced).
func TestHealthWorktreeClashEdgeTriggered(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	att := w.newTicket(t, "PROJ")

	// Wedge the attempt: create its branch and check it out in a *second* worktree
	// of the same repo, so the reconciler's own `git worktree add <branch>` is
	// refused ("already checked out") — the founding incident, reproduced.
	branch := "draiver/PROJ/" + att
	other := filepath.Join(t.TempDir(), "other")
	gitIn(t, w.repo, "worktree", "add", "-b", branch, other, "HEAD")

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	// Two failing ticks: the clash refuses admit both times, but edge-triggering
	// emits the start exactly once — not one line per tick.
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("a per-attempt admit failure must not fail the tick: %v", err)
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if n := f.count(); n != 0 {
		t.Fatalf("a wedged attempt must not spawn a session: %d adapters made", n)
	}

	got := readHealth(t, w.root, "PROJ", att)
	if len(got) != 1 {
		t.Fatalf("want exactly one edge (an error-start), got %d: %+v", len(got), got)
	}
	if got[0].Event != "start" || got[0].Class != reconcile.ClassWorktree {
		t.Fatalf("want a worktree-clash start, got %+v", got[0])
	}
	if got[0].TS == "" || !strings.Contains(got[0].Message, "worktree") {
		t.Fatalf("start must carry a timestamp and the underlying error: %+v", got[0])
	}

	// Clear the clash: free the branch by removing the other worktree. The next tick
	// admits cleanly, so the class clears — an error-end closes the pair.
	gitIn(t, w.repo, "worktree", "remove", other)
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("recovery tick: %v", err)
	}
	waitFor(t, "attempt admitted after the clash cleared", func() bool {
		sess, err := session.Open(w.root, "PROJ", att)
		if err != nil {
			return false
		}
		defer sess.Close()
		id, err := sess.ReadIdentity()
		return err == nil && id.SessionID != ""
	})

	got = readHealth(t, w.root, "PROJ", att)
	if len(got) != 2 {
		t.Fatalf("want a start then an end, got %d: %+v", len(got), got)
	}
	if got[1].Event != "end" || got[1].Class != reconcile.ClassWorktree {
		t.Fatalf("second edge must be a worktree-clash end, got %+v", got[1])
	}
}

// TestHealthCatchAllForBadRepo: an admit failure that is not a worktree clash (here
// a repo path that is not a git working tree, so the worktree Manager cannot even be
// opened) still logs a transition — under the admit-failed catch-all — so gotcha
// #2's rule holds: a real wedge is never silently stuck, whatever its cause.
func TestHealthCatchAllForBadRepo(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	badRepo := t.TempDir() // a plain dir, not a git repository
	att := w.newTicketOnRepo(t, "PROJ-BAD", badRepo)

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("a per-attempt admit failure must not fail the tick: %v", err)
	}

	got := readHealth(t, w.root, "PROJ-BAD", att)
	if len(got) != 1 || got[0].Event != "start" || got[0].Class != reconcile.ClassAdmit {
		t.Fatalf("want a single admit-failed start, got %+v", got)
	}
}

// TestHealthSweepClosesUndesired: an attempt wedged while desired, then removed
// from the desired set (disabled/blocked/retired), gets its open error class closed
// out on the next tick — so the record (and the webui red dot) resolves rather than
// stranding on a lone error-start for an attempt no longer supervised.
func TestHealthSweepClosesUndesired(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	badRepo := t.TempDir()
	att := w.newTicketOnRepo(t, "PROJ-BAD", badRepo)

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if got := readHealth(t, w.root, "PROJ-BAD", att); len(got) != 1 {
		t.Fatalf("want one start after the wedge, got %+v", got)
	}

	// Drop it out of the desired set (disable supervision), then tick: the sweep
	// closes the still-open class.
	if _, err := ticketlog.Append(w.root, "PROJ-BAD", att, event.Event{Type: "disable", Actor: "agent:x", Body: "disabled"}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick after disable: %v", err)
	}
	got := readHealth(t, w.root, "PROJ-BAD", att)
	if len(got) != 2 || got[1].Event != "end" {
		t.Fatalf("want the class closed out on leaving desired, got %+v", got)
	}
}
