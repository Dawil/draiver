package web

import (
	"context"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/pyramid"
	"github.com/Dawil/draiver/internal/worktree"
)

// pyramidVM drives the board/detail "pyramid-badge" partial: the read-only
// projection of an attempt's recorded test-pyramid verification (drv-009). It
// surfaces the `test-result` events drvctl-048 logs and drvctl-049's review gate
// folds — otherwise a passing rung is invisible on the board. It is the UI twin of
// pyramid.HighestAtHEAD: the same "highest logged rung whose commit == current
// HEAD" fold, rendered rather than gated.
//
// A nil *pyramidVM renders nothing. The badge appears only once there is a recorded
// result to surface: a repo with no .test-pyramid.yaml, and a declared pyramid with
// no logged result yet, both project to nil — the two spec "no pyramid (render
// nothing)" cases. It renders in exactly the two states the spec distinguishes from
// nothing: green-at-HEAD and stale.
type pyramidVM struct {
	// Rungs is the pyramid's levels in climb order (base→top). Each carries whether
	// it is Reached — at or below the highest logged rung — since `test --log RUNG`
	// climbs base→RUNG and logs one result naming RUNG, so a logged rung implies
	// every rung beneath it passed too.
	Rungs []pyramidRungVM
	// Stale is true when a result was logged but none at the current HEAD: the tree
	// moved on since it was recorded, so the green no longer describes HEAD. Reached
	// rungs are then shown dimmed rather than green, and StaleAt names the commit the
	// last result was recorded at.
	Stale bool
	// Dirty is true when a result names the current HEAD but the worktree holds
	// uncommitted changes: re-running `draiver test` might no longer pass, so the
	// recorded green no longer describes the tree (drvweb-021 #27). Like Stale it
	// renders dimmed rather than green; unlike Stale, HEAD has not moved — only the
	// working tree diverged. Stale and Dirty are mutually exclusive: Dirty is set
	// only on the otherwise-green-at-HEAD path.
	Dirty bool
	// Head is the short current-HEAD SHA (read live), and StaleAt the short SHA the
	// highest stale result was recorded at (empty unless Stale) — both for the badge
	// tooltip / label.
	Head    string
	StaleAt string
	// Label is the human-facing summary used as the badge title/aria-label, built
	// server-side so a screen reader gets the same story the chips tell.
	Label string
}

// pyramidRungVM is one rung of the rendered ladder.
type pyramidRungVM struct {
	Name    string
	Reached bool // at or below the highest logged rung (green when !Stale, dimmed when Stale)
}

// shortSHA trims a commit oid to a readable 12-char prefix for the badge tooltip.
// It is duplicated from land.ShortSHA rather than imported, to keep the read-only
// web server free of the daemon's reconcile package (land pulls it in) — the same
// decoupling dot.go makes by mirroring reconcile's constants.
func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// projectPyramid folds a loaded pyramid, an attempt's events, and the live HEAD
// into the badge VM. It is the pure, I/O-free core of the projection (the Server
// method below does the git/worktree reads), so it is exhaustively unit-testable.
//
// It returns nil — render nothing — when there is no pyramid, when nothing has been
// logged, or when every logged rung names a level absent from the current pyramid
// (a renamed/removed rung): in all three there is no recorded verification of a
// known rung to surface. Otherwise it reports green-at-HEAD (a result whose commit
// equals head, in a clean tree), dirty (such a result but the worktree has
// uncommitted changes), or stale (a result at an older commit). dirty is an I/O fact
// like head; the caller reads it live so this fold stays pure and testable.
func projectPyramid(p *pyramid.Pyramid, events []event.Event, head string, dirty bool) *pyramidVM {
	if p == nil {
		return nil
	}

	// Map the attempt's test-result events onto the pyramid's neutral Result pair,
	// exactly as reviewGate does: only green results are ever logged (drvctl-048), so
	// there is no failing/partial state to reconcile.
	var results []pyramid.Result
	for _, e := range events {
		if e.Type == "test-result" {
			results = append(results, pyramid.Result{Rung: e.Rung, Commit: e.Commit})
		}
	}
	if len(results) == 0 {
		return nil
	}

	index := make(map[string]int, len(p.Levels))
	for i, lv := range p.Levels {
		index[lv.Name] = i
	}

	vm := &pyramidVM{Head: shortSHA(head)}
	for _, lv := range p.Levels {
		vm.Rungs = append(vm.Rungs, pyramidRungVM{Name: lv.Name})
	}

	// Green-at-HEAD: the highest rung whose result names the current HEAD. Everything
	// at or below it is green, since a logged rung implies the climb beneath it.
	if highest, ok := p.HighestAtHEAD(results, head); ok {
		hi := index[highest.Name]
		for i := range vm.Rungs {
			vm.Rungs[i].Reached = i <= hi
		}
		if dirty {
			// A result names HEAD, but the working tree has uncommitted changes:
			// re-running `draiver test` might no longer pass, so the recorded green no
			// longer describes the tree. Show the rungs it reached, dimmed, and say
			// why — never green (drvweb-021 #27).
			vm.Dirty = true
			vm.Label = "test pyramid: " + highest.Name + " was green at HEAD " + vm.Head +
				", but the worktree has uncommitted changes — re-run `draiver test`"
			return vm
		}
		vm.Label = "test pyramid: green through " + highest.Name + " at HEAD " + vm.Head
		return vm
	}

	// No result at HEAD, but one exists at an older commit: stale. Show the highest
	// rung ever logged (latest result wins for the SHA, since events are seq-ordered)
	// dimmed, so the board shows what was verified and that it no longer describes the
	// tip. A result naming a rung outside this pyramid is ignored (as in the fold).
	best := -1
	var atSHA string
	for _, r := range results {
		if i, ok := index[r.Rung]; ok && i >= best {
			best = i
			atSHA = r.Commit
		}
	}
	if best < 0 {
		return nil
	}
	vm.Stale = true
	vm.StaleAt = shortSHA(atSHA)
	for i := range vm.Rungs {
		vm.Rungs[i].Reached = i <= best
	}
	vm.Label = "test pyramid: " + p.Levels[best].Name + " was green at " + vm.StaleAt +
		", now stale — HEAD is " + vm.Head + "; re-run `draiver test --log`"
	return vm
}

// pyramidBadge is the FuncMap entry the card and detail templates call: it resolves
// the attempt's live checkout, loads its pyramid, reads live HEAD, and folds its
// test-result log into the badge VM. It is the read-only, no-write-path projection
// drvweb-021 adds over drvctl-048/049 — HEAD is read live (`git rev-parse HEAD`),
// nothing is stored.
//
// Like sessionDot, it is best-effort presentation: any resolution or I/O failure —
// no recorded repo, a repo that is not a git base, no live worktree, a malformed
// .test-pyramid.yaml, a git read error — yields a nil badge (render nothing) rather
// than breaking the board, which renders many cards and must survive one attempt's
// broken checkout. The badge is additive: it only ever appears on positive proof of
// a recorded result, never fails a render.
func (s *Server) pyramidBadge(a project.Attempt) *pyramidVM {
	if a.Repo == "" {
		return nil
	}
	ctx := context.Background()
	wm, err := worktree.NewManager(a.Repo)
	if err != nil {
		return nil
	}
	key := worktree.Key{Ticket: a.Ticket, Attempt: a.ID}
	wt, ok, err := wm.Locate(ctx, key)
	if err != nil {
		return nil
	}
	if ok {
		// Live checkout: read the pyramid, HEAD, and cleanliness straight from the
		// worktree. Dirtiness is read live — the same `git status --porcelain` the
		// `test --log` gate uses — so the badge only stays green while re-running
		// `draiver test` would still pass (drvweb-021 #27).
		p, err := pyramid.Load(wt.Path)
		if err != nil {
			return nil
		}
		head, err := worktree.HeadSHA(ctx, wt.Path)
		if err != nil {
			return nil
		}
		dirty, err := worktree.DirtyAt(ctx, wt.Path)
		if err != nil {
			return nil
		}
		return projectPyramid(p, a.Events, head, dirty)
	}
	// No live checkout — the attempt retired into Review and the daemon reclaimed
	// its worktree, but the branch survives. Read the same two facts from the base
	// repo via the branch ref, so the badge stays visible exactly where a reviewer
	// wants it. Still live (git's current ref), still no stored HEAD SHA.
	return s.branchPyramid(ctx, wm, key, a.Events)
}

// branchPyramid is pyramidBadge's fallback when no live worktree exists: it folds
// the attempt's test-result log against its branch tip and the .test-pyramid.yaml
// committed on that branch, read from the base repo without a checkout. A missing
// branch, a missing .test-pyramid.yaml, or any git/parse error yields nil (render
// nothing) — the same best-effort degradation as the live path.
//
// It projects with dirty=false: a committed branch tip has no working tree to be
// dirty. A checkout the daemon keeps warm while dirty is still found by Locate, so
// it takes the live path above where DirtyAt observes it — this fallback is only
// reached once the worktree is gone.
func (s *Server) branchPyramid(ctx context.Context, wm *worktree.Manager, key worktree.Key, events []event.Event) *pyramidVM {
	head, ok, err := wm.BranchSHA(ctx, key)
	if err != nil || !ok {
		return nil
	}
	data, ok, err := wm.FileAtBranch(ctx, key, pyramid.FileName)
	if err != nil || !ok {
		return nil
	}
	p, err := pyramid.Parse(data)
	if err != nil {
		return nil
	}
	return projectPyramid(p, events, head, false)
}
