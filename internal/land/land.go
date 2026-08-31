// Package land is the in-process orchestration of `ctl merge`'s external twin —
// reconciling an attempt from a remote after its change landed via a PR merged on
// the forge. It sits BESIDE internal/repo (the data-folder gateway) over the same
// primitives: it drives internal/worktree's read-only git (fetch + containment
// check + best-effort local fast-forward) and, when the branch is proven contained
// upstream, records the single durable write — the `done` event — THROUGH repo, so
// there is still exactly one write path shared by the CLI verb and the webui.
//
// Both callers reach the same operation: `cmd/ctl_land.go`'s `merge --remote` verb
// is a thin wrapper (flag parsing, the escalate/no-escalate disposition, stdout)
// and internal/web's handler calls MergeRemote directly instead of shelling the
// CLI. The git/session/config machinery lives here rather than in repo so the
// clean data-folder gateway is not burdened with worktree and process concerns.
package land

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/config"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/reconcile"
	"github.com/Dawil/draiver/internal/repo"
	"github.com/Dawil/draiver/internal/session"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/worktree"
)

// PlainError marks a merge-remote failure the CLI must surface as a plain nonzero
// exit rather than route through its escalate/no-escalate disposition: a
// remote/config resolution problem, or a post-land bookkeeping error — not a land
// failure worth escalating. The webui treats every error identically (500), so it
// ignores the tag. Callers test for it with errors.As.
type PlainError struct{ Err error }

func (e *PlainError) Error() string { return e.Err.Error() }
func (e *PlainError) Unwrap() error { return e.Err }

func plain(err error) error {
	if err == nil {
		return nil
	}
	return &PlainError{Err: err}
}

// Handle is the resolved target of a land: the attempt, its derived state, the
// base branch it lands into, and the worktree Manager for its repo. Load resolves
// it once; the CLI's local merge/sync share it too.
type Handle struct {
	Root    store.Root
	Ticket  string
	Attempt string
	Att     project.Attempt
	Base    string
	Repo    string
	WM      *worktree.Manager
	Key     worktree.Key
}

// Load resolves the target attempt's provenance and derived state and builds the
// worktree Manager for its repo — the shared preamble of every land. It fails
// early if the attempt records no base (nothing to land into) or no repo (no
// worktree to land from).
func Load(root store.Root, ticket, att string) (*Handle, error) {
	a, err := project.LoadAttempt(root, ticket, att)
	if err != nil {
		return nil, err
	}
	meta, err := attempt.LoadMeta(root, ticket, att)
	if err != nil {
		return nil, err
	}
	base := strings.TrimSpace(meta.Base)
	if base == "" {
		return nil, fmt.Errorf("%s/%s records no base branch to land into; recreate the attempt with --base, or add a `base:` to its attempt.md", ticket, att)
	}
	repoPath := strings.TrimSpace(meta.Repo)
	if repoPath == "" {
		return nil, fmt.Errorf("%s/%s records no repo, so it has no worktree to land from", ticket, att)
	}
	wm, err := worktree.NewManager(repoPath)
	if err != nil {
		return nil, fmt.Errorf("%s/%s repo %q is missing or not a git working tree: %w", ticket, att, repoPath, err)
	}
	return &Handle{
		Root: root, Ticket: ticket, Attempt: att, Att: a, Base: base, Repo: repoPath, WM: wm,
		Key: worktree.Key{Ticket: ticket, Attempt: att},
	}, nil
}

// RemoteResult reports a successful remote reconcile: the worktree outcome (the
// containment proof and the best-effort local pull) and the recorded `done` event.
type RemoteResult struct {
	Merge worktree.RemoteMerge
	Done  event.Event
}

// MergeRemote reconciles ticket/att from a remote after an external PR merge and
// records `done` through the gateway. remote selects which git remote: "" means
// the sole remote, or — with several — the config's primary_remote; a NAME picks
// that remote (verified). configPath is the supervisor config consulted only for
// primary_remote when several remotes exist. gw carries the acting identity the
// `done` is stamped with (human:$USER for the CLI, the server's actor for web).
//
// It fetches the recorded base (read-only, never pushes), verifies the attempt
// branch is contained upstream — the load-bearing proof the PR really landed — and
// only then records `done` and best-effort fast-forwards the local base. Nothing
// is recorded when the branch is not contained. Errors from remote/config
// resolution and the post-land `done` append are wrapped in *PlainError; the gate
// and containment failures are returned bare so the CLI can apply its
// escalate/no-escalate disposition.
func MergeRemote(ctx context.Context, gw *repo.Repo, ticket, att, remote, configPath string) (RemoteResult, error) {
	h, err := Load(gw.Root(), ticket, att)
	if err != nil {
		return RemoteResult{}, plain(err)
	}
	resolved, err := h.resolveRemote(ctx, remote, configPath)
	if err != nil {
		return RemoteResult{}, plain(err)
	}
	if err := h.gateForRemoteLand(ctx); err != nil {
		return RemoteResult{}, err
	}

	rm, err := h.WM.MergeRemote(ctx, h.Key, resolved, h.Base)
	if errors.Is(err, worktree.ErrNotContainedUpstream) {
		return RemoteResult{Merge: rm}, fmt.Errorf(
			"%s is not contained in %s — the PR was not merged into %s (or not yet fetched); recording nothing",
			rm.Branch, rm.Ref, h.Base)
	}
	if err != nil {
		return RemoteResult{Merge: rm}, err
	}

	// The branch is proven contained in the remote base — the code landed, just via
	// the remote this time. Record `done` so control state follows reality; a failure
	// here is post-git bookkeeping, surfaced plainly rather than escalated.
	body := fmt.Sprintf("`ctl merge --remote=%s`: %s is contained in %s (tip %s) — the change landed via an external PR merge; recording done.",
		resolved, rm.Branch, rm.Ref, ShortSHA(rm.RemoteTip))
	e, err := gw.Done(ticket, att, body)
	if err != nil {
		return RemoteResult{Merge: rm}, plain(err)
	}
	return RemoteResult{Merge: rm, Done: e}, nil
}

// FormatRemoteReport renders the human report lines for a successful remote
// reconcile: the close-and-done line plus the best-effort local pull outcome (a
// move, a no-op, or a skip warning). The CLI prints them to stdout and the webui
// joins them into its result banner, so both surfaces carry the same text.
func FormatRemoteReport(ticket, att string, res RemoteResult) []string {
	rm := res.Merge
	lines := []string{
		fmt.Sprintf("closed %s/%s: %s is contained in %s and recorded done (seq %d)",
			ticket, att, rm.Branch, rm.Ref, res.Done.Seq),
	}
	switch p := rm.Pull; {
	case p.Moved:
		lines = append(lines, fmt.Sprintf("fast-forwarded local %s to %s (tip %s)", rm.Base, rm.Ref, ShortSHA(p.Tip)))
	case p.Already:
		lines = append(lines, fmt.Sprintf("local %s already up to date with %s", rm.Base, rm.Ref))
	case p.Skipped != "":
		lines = append(lines, fmt.Sprintf("warning: left local %s unchanged — %s", rm.Base, p.Skipped))
	}
	return lines
}

// resolveRemote picks the git remote to reconcile from, mirroring review's rule:
// a NAME wins (verified against the repo's remotes); bare ("") uses the sole
// remote regardless, else the config's primary_remote, and with several remotes
// and no primary it refuses rather than guess.
func (h *Handle) resolveRemote(ctx context.Context, remote, configPath string) (string, error) {
	remotes, err := h.WM.Remotes(ctx)
	if err != nil {
		return "", err
	}
	if len(remotes) == 0 {
		return "", fmt.Errorf("%s/%s repo has no git remote configured; add one (git remote add) before `ctl merge --remote`", h.Ticket, h.Attempt)
	}
	if name := strings.TrimSpace(remote); name != "" {
		if !slices.Contains(remotes, name) {
			return "", fmt.Errorf("no git remote named %q in %s/%s (configured: %s)", name, h.Ticket, h.Attempt, strings.Join(remotes, ", "))
		}
		return name, nil
	}
	if len(remotes) == 1 {
		return remotes[0], nil
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return "", err
	}
	if pr := strings.TrimSpace(cfg.PrimaryRemote); pr != "" {
		if !slices.Contains(remotes, pr) {
			return "", fmt.Errorf("configured primary_remote %q is not a remote of %s/%s (configured: %s)", pr, h.Ticket, h.Attempt, strings.Join(remotes, ", "))
		}
		return pr, nil
	}
	return "", fmt.Errorf("%s/%s repo has several remotes (%s) and no primary_remote configured; pass --remote=NAME or set primary_remote in the config", h.Ticket, h.Attempt, strings.Join(remotes, ", "))
}

// gateForRemoteLand enforces the remote-land preconditions: the attempt must be in
// Review (a remote land is still the Review → Done transition) and stopped (no live
// session racing the close). Unlike a local land it does not require a clean base
// checkout — the local fast-forward is best-effort, so a dirty base skips the pull
// with a warning rather than blocking the close.
func (h *Handle) gateForRemoteLand(ctx context.Context) error {
	if h.Att.State != project.Review {
		return fmt.Errorf("merge --remote records the Review → Done transition, but %s/%s is %s — claim `review` first, or use the plain `done` verb for a docs-only ticket", h.Ticket, h.Attempt, h.Att.State)
	}
	live, pid, err := SessionLive(h.Root, h.Ticket, h.Attempt)
	if err != nil {
		return err
	}
	if live {
		return fmt.Errorf("%s/%s has a live session (pid %d); stop it with `ctl stop` before landing", h.Ticket, h.Attempt, pid)
	}
	return nil
}

// SessionLive reports whether the attempt has a live recorded session process —
// the "stopped" half of the land gate. A missing session, or a recorded pid that
// is no longer alive, counts as stopped.
func SessionLive(root store.Root, ticket, att string) (bool, int, error) {
	sess, err := session.Open(root, ticket, att)
	if err != nil {
		return false, 0, err
	}
	defer sess.Close()
	id, err := sess.ReadIdentity()
	if err != nil {
		// A never-started (or reaped-and-cleared) attempt has no identity file; a
		// wrapped fs.ErrNotExist means "no session", which counts as stopped.
		if errors.Is(err, fs.ErrNotExist) {
			return false, 0, nil
		}
		return false, 0, err
	}
	if id.PID == 0 {
		return false, 0, nil
	}
	return reconcile.OSProc{}.Alive(id.PID), id.PID, nil
}

// ShortSHA trims a commit oid to a readable 12-char prefix for log bodies.
func ShortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
