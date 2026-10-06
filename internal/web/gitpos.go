package web

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/Dawil/draiver/internal/config"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/worktree"
)

// gitpos.go adds the drv-022 Git-controls position indicator: the attempt's current
// checked-out local branch and how far ahead/behind the primary remote that branch
// is. It is a read-only, best-effort projection built exactly like the drv-021
// pyramid badge — the live git reads sit behind an injectable seam (s.gitPos,
// defaulting to gatherGitPosition) so a test drives the render off canned state, and
// every failure degrades to "unknown" rather than breaking the panel or the Sync
// POST.

// gitPositionState is the live git-derived input the position line projects over:
// the current local branch (BranchOK false on a detached HEAD or an unreadable
// repo) and the ahead/behind counts versus the primary remote (CountOK false when
// there is no resolvable remote, no upstream tracking ref, or any git error). It is
// the seam between the I/O half (reading git) and the pure fold, mirroring
// pyramidState.
type gitPositionState struct {
	Branch   string
	BranchOK bool
	Ahead    int
	Behind   int
	CountOK  bool
}

// gitPositionVM drives the "git-position" line in the Git-controls panel. It always
// renders (the panel states where the checkout stands even when the read failed):
// Branch falls back to "unknown" when the branch could not be read, and the
// ahead/behind count is omitted (HasCount false) when it could not be computed.
type gitPositionVM struct {
	Branch    string
	HasBranch bool
	Ahead     int
	Behind    int
	HasCount  bool
	// Summary is the human ahead/behind phrase ("up to date", "ahead 2 · behind 3"),
	// built server-side so it is identical in the panel and to a screen reader. Empty
	// when HasCount is false.
	Summary string
}

// projectPosition folds a gathered git state into the view model. It is the pure,
// I/O-free core (the Server method below does the git reads), so it is exhaustively
// unit-testable. It never returns nil: the line always renders, degrading to
// "unknown" / an omitted count rather than vanishing.
func projectPosition(st gitPositionState) *gitPositionVM {
	vm := &gitPositionVM{Branch: "unknown"}
	if st.BranchOK {
		vm.Branch = st.Branch
		vm.HasBranch = true
	}
	if st.CountOK {
		vm.HasCount = true
		vm.Ahead = st.Ahead
		vm.Behind = st.Behind
		vm.Summary = formatAheadBehind(st.Ahead, st.Behind)
	}
	return vm
}

// formatAheadBehind renders the ahead/behind phrase: an exactly-even branch reads
// "up to date"; otherwise both counts are shown ("ahead 2 · behind 0") so the panel
// always states the full relationship to the remote.
func formatAheadBehind(ahead, behind int) string {
	if ahead == 0 && behind == 0 {
		return "up to date"
	}
	return fmt.Sprintf("ahead %d · behind %d", ahead, behind)
}

// gitPosition is the FuncMap entry the Git-controls panel calls. It gathers the
// attempt's live git state (behind s.gitPos) and folds it into the line's VM. Like
// pyramidBadge it is best-effort presentation with no write path, and it is invoked
// only where the template references it — the gated git panel, which the live 3s
// poll never re-renders — so the git reads it does are off the poll path.
func (s *Server) gitPosition(a project.Attempt) *gitPositionVM {
	return projectPosition(s.gitPos(context.Background(), a))
}

// gatherGitPosition is the default s.gitPos: it reads the current branch and the
// ahead/behind counts from the attempt's live checkout, falling back to the base
// repo once the worktree is reclaimed. Every read is best-effort — a missing repo,
// a detached HEAD, no resolvable remote, or no upstream ref each leave the matching
// field unset rather than erroring, so the position line always renders.
func (s *Server) gatherGitPosition(ctx context.Context, a project.Attempt) gitPositionState {
	var st gitPositionState
	if a.Repo == "" {
		return st
	}
	wm, err := worktree.NewManager(a.Repo)
	if err != nil {
		return st
	}
	key := worktree.Key{Ticket: a.Ticket, Attempt: a.ID}
	// Prefer the live worktree (the attempt branch is checked out there); fall back to
	// the base repo once the daemon has reclaimed the worktree.
	path := a.Repo
	if wt, ok, lerr := wm.Locate(ctx, key); lerr == nil && ok {
		path = wt.Path
	}
	branch, ok, err := worktree.HeadBranch(ctx, path)
	if err != nil || !ok {
		return st // detached HEAD or unreadable — branch stays "unknown"
	}
	st.Branch = branch
	st.BranchOK = true
	remote, ok := primaryRemote(ctx, wm)
	if !ok {
		return st // no resolvable primary remote — omit the count
	}
	if ahead, behind, ok := worktree.AheadBehind(ctx, path, remote, branch); ok {
		st.Ahead, st.Behind, st.CountOK = ahead, behind, true
	}
	return st
}

// primaryRemote resolves the git remote the position is measured against, mirroring
// the Sync verb's rule (land.resolveRemote): the sole remote wins outright, else the
// config's primary_remote (when it names a real remote). Several remotes with no
// primary_remote, no remote at all, or any read error yields ok=false — the count is
// then omitted, never guessed.
func primaryRemote(ctx context.Context, wm *worktree.Manager) (string, bool) {
	remotes, err := wm.Remotes(ctx)
	if err != nil || len(remotes) == 0 {
		return "", false
	}
	if len(remotes) == 1 {
		return remotes[0], true
	}
	cfg, err := config.Load("")
	if err != nil {
		return "", false
	}
	if pr := strings.TrimSpace(cfg.PrimaryRemote); pr != "" && slices.Contains(remotes, pr) {
		return pr, true
	}
	return "", false
}
