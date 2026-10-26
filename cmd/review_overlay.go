package cmd

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/config"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/overlay"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/worktree"
)

// review_overlay.go implements drv-041's review surfacing: a test-pyramid overlay is a
// SEPARATE developer-owned repo, so a codebase review that omitted the overlay change
// would silently regress on the next run (the two repos are coupled even though they
// live apart). `draiver review` therefore surfaces the overlay alongside the codebase
// change — but ADVISORY-but-loud: it never gates the codebase claim (a failure here is
// warned about, never returned), it only makes the coupling visible.
//
// It surfaces three things when the attempt has an overlay with unmerged work:
//   - a SECOND compare URL (when the overlay repo has a review remote) or, local-only,
//     a graceful degrade to the branch name + a local-diff hint — never an error;
//   - a LOUD warning that an unmerged overlay change exists and the codebase will
//     regress without it;
//   - a quiet nudge if the overlay repo's main has uncommitted changes (the owner may
//     have legitimate WIP — a heads-up, not a gate).
//
// The compare link (when buildable and host-valid) is returned to attach to the review
// event; everything else is printed. The codebase review proceeds regardless.

// overlayReviewLinks resolves the attempt's overlay, prints the advisory surfacing, and
// returns any links to attach to the review event (the overlay compare URL, rel
// "overlay-compare"). It is strictly best-effort: every failure path returns nil links
// (and at most a warning) so the overlay can never gate or break the codebase review.
func overlayReviewLinks(ctx context.Context, cmd *cobra.Command, root store.Root, ticket, att string) []event.Link {
	out := cmd.OutOrStdout()
	errw := cmd.ErrOrStderr()

	_, overlayRepo := resolveAttemptOverlay(ctx, root, ticket, att)
	if overlayRepo == "" {
		return nil // no overlay for this attempt — nothing to surface
	}

	om, err := worktree.NewManager(overlayRepo)
	if err != nil {
		// A recorded/resolved overlay that is no longer a usable repo: a heads-up, not a
		// failure — the codebase review still stands.
		fmt.Fprintf(errw, "warning: overlay repo %s is not resolvable, skipping overlay surfacing: %v\n", overlayRepo, err)
		return nil
	}
	key := worktree.Key{Ticket: ticket, Attempt: att}
	branchSHA, ok, err := om.BranchSHA(ctx, key)
	if err != nil || !ok {
		// No overlay attempt branch — the attempt never cut an overlay worktree, so there
		// is no overlay change to surface. (A run that only *read* the overlay still cuts
		// a branch; truly none means nothing happened on the overlay.)
		return nil
	}

	branch := attemptBranch(ticket, att)
	mainBranch := overlayDefaultBranch(ctx, overlayRepo)
	unmerged, aheadErr := overlayUnmerged(ctx, overlayRepo, mainBranch, branch)

	// The compare URL / degrade line, built against the overlay's review remote.
	var links []event.Link
	compareURL, remote := overlayCompareURL(ctx, overlayRepo, mainBranch, branch)
	if compareURL != "" {
		// Only attach a link that will survive the append-time validation, so an overlay
		// link can never turn a review refusal into a hard error (drv-041: advisory).
		cfg, _ := config.Load("")
		link := event.Link{Rel: "overlay-compare", Href: compareURL}
		if event.ValidateLink(link, cfg.ReviewLinkHosts) == nil {
			links = append(links, link)
			fmt.Fprintf(out, "overlay change — second compare URL (%s): %s\n", remote, compareURL)
		} else {
			fmt.Fprintf(out, "overlay change on branch %q in %s (compare URL host not allow-listed; diff locally: `git -C %s diff %s..%s`)\n",
				branch, overlayRepo, overlayRepo, mainBranch, branch)
		}
	} else {
		// Local-only overlay (no review remote): degrade gracefully to the branch name and
		// a local-diff hint — not an error.
		fmt.Fprintf(out, "overlay change on branch %q in %s (local-only overlay, no review remote; diff locally: `git -C %s diff %s..%s`)\n",
			branch, overlayRepo, overlayRepo, mainBranch, branch)
	}

	// The loud coupling warning: an unmerged overlay change means the codebase regresses
	// on the next run without it. aheadErr/!unmerged both fall back to a conservative
	// heads-up rather than silence, since the branch demonstrably diverged from main
	// (its SHA was found above).
	if unmerged || aheadErr != nil || branchSHA != "" {
		fmt.Fprintf(errw, "WARNING: this attempt has an UNMERGED overlay change (%s in %s). It does NOT gate this codebase review, but the codebase will regress on the next `draiver test` run until the overlay change lands too — review and merge them together.\n",
			branch, overlayRepo)
	}

	// Dirty overlay main: the owner may have legitimate WIP — a nudge, not a gate.
	if dirty, err := worktree.DirtyAt(ctx, overlayRepo); err == nil && dirty {
		fmt.Fprintf(errw, "note: the overlay repo %s has uncommitted changes on its working tree — that is fine if it is your WIP, just a heads-up that an overlay run reflects the committed tree.\n", overlayRepo)
	}

	return links
}

// resolveAttemptOverlay finds the overlay repo an attempt is associated with: the
// provenance recorded by a `draiver test` run (the authoritative "what the run used")
// first, else a fresh resolution from settings (so review surfaces even before any test
// ran). It returns the codebase repo path and the overlay repo path ("" when none).
func resolveAttemptOverlay(ctx context.Context, root store.Root, ticket, att string) (repoPath, overlayRepo string) {
	meta, err := attempt.LoadMeta(root, ticket, att)
	if err != nil {
		return "", ""
	}
	repoPath = strings.TrimSpace(meta.Repo)
	if strings.TrimSpace(meta.Overlay) != "" {
		return repoPath, strings.TrimSpace(meta.Overlay)
	}
	if repoPath == "" {
		return "", ""
	}
	cfg, err := config.Load("")
	if err != nil {
		return repoPath, ""
	}
	res := cfg.RepoSettings(repoPath)
	loc, ok, err := overlay.Resolve(repoPath, res.Overlay, cfg.OverlayDir)
	if err != nil || !ok {
		return repoPath, ""
	}
	return repoPath, loc.Repo
}

// overlayDefaultBranch is the overlay repo's default branch — the repo is operated in
// place and checked out on it, so its current HEAD branch is the baseline the attempt
// branch was cut from. Falls back to "main" on a detached/odd checkout so a compare URL
// and diff hint still read sensibly.
func overlayDefaultBranch(ctx context.Context, overlayRepo string) string {
	if b, ok, err := worktree.HeadBranch(ctx, overlayRepo); err == nil && ok && b != "" {
		return b
	}
	return "main"
}

// overlayUnmerged reports whether the attempt branch carries commits the overlay's main
// does not (`git rev-list --count main..branch` > 0). An error (e.g. the branch is not
// yet visible) is returned so the caller can fall back to a conservative heads-up.
func overlayUnmerged(ctx context.Context, overlayRepo, mainBranch, branch string) (bool, error) {
	rev := strings.TrimSpace(mainBranch) + ".." + branch
	n, err := overlayGitOut(ctx, overlayRepo, "rev-list", "--count", rev)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(n) != "0" && strings.TrimSpace(n) != "", nil
}

// overlayCompareURL builds a web compare URL for the overlay change against its review
// remote, mirroring the compare URL the codebase review uses. It returns ("", "") when
// the overlay repo has no usable review remote (local-only) — the caller degrades
// gracefully. The remote is the overlay repo's resolved DefaultRemote/PrimaryRemote, or
// its sole remote when exactly one exists; an ambiguous multi-remote with no configured
// primary is left to degrade rather than guessed.
func overlayCompareURL(ctx context.Context, overlayRepo, mainBranch, branch string) (url, remote string) {
	cfg, err := config.Load("")
	if err == nil {
		remote = strings.TrimSpace(cfg.RepoSettings(overlayRepo).Remote)
	}
	om, err := worktree.NewManager(overlayRepo)
	if err != nil {
		return "", ""
	}
	remotes, err := om.Remotes(ctx)
	if err != nil || len(remotes) == 0 {
		return "", "" // local-only overlay
	}
	if remote == "" {
		if len(remotes) == 1 {
			remote = remotes[0]
		} else {
			return "", "" // ambiguous; do not guess — degrade
		}
	} else {
		found := false
		for _, r := range remotes {
			if r == remote {
				found = true
				break
			}
		}
		if !found {
			return "", ""
		}
	}

	raw, err := overlayGitOut(ctx, overlayRepo, "remote", "get-url", remote)
	if err != nil {
		return "", ""
	}
	base := webBaseFromRemoteURL(strings.TrimSpace(raw))
	if base == "" {
		return "", ""
	}
	return fmt.Sprintf("%s/compare/%s...%s", base, mainBranch, branch), remote
}

// webBaseFromRemoteURL normalises a git remote URL to its https web base, stripping a
// trailing ".git": an https/http URL is used as-is, the scp-like `git@host:owner/repo`
// and `ssh://git@host/owner/repo` forms are rewritten to `https://host/owner/repo`. An
// unrecognised form yields "" so the caller degrades rather than emitting a bogus link.
func webBaseFromRemoteURL(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimSuffix(raw, ".git")
	switch {
	case strings.HasPrefix(raw, "https://") || strings.HasPrefix(raw, "http://"):
		return raw
	case strings.HasPrefix(raw, "ssh://"):
		rest := strings.TrimPrefix(raw, "ssh://")
		if at := strings.Index(rest, "@"); at >= 0 {
			rest = rest[at+1:]
		}
		return "https://" + rest
	case strings.HasPrefix(raw, "git@"):
		// scp-like: git@host:owner/repo → https://host/owner/repo
		rest := strings.TrimPrefix(raw, "git@")
		host, path, ok := strings.Cut(rest, ":")
		if !ok {
			return ""
		}
		return "https://" + host + "/" + path
	default:
		return ""
	}
}

// attemptBranch is the feature-branch name draiver cuts for an attempt in any repo it
// manages — codebase or overlay — matching worktree.Key's internal branch() convention
// (`draiver/<ticket>/<attempt>`). It is built here rather than exported from worktree
// because review is TICKET-addressed and reasons about the branch name without a Key in
// hand.
func attemptBranch(ticket, att string) string {
	return "draiver/" + ticket + "/" + att
}

// overlayGitOut runs a read-only git command in the overlay repo and returns its stdout.
func overlayGitOut(ctx context.Context, dir string, args ...string) (string, error) {
	c := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	outb, err := c.Output()
	if err != nil {
		return "", err
	}
	return string(outb), nil
}
