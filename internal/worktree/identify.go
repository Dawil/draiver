package worktree

// identify.go holds the path-addressed, Manager-less git helpers a command that
// is *standing inside* an attempt checkout needs — `draiver test`, which the
// agent runs from its own worktree (drvctl-048). Unlike the Manager (keyed on an
// attempt and deriving the checkout path from the managed base), these take the
// checkout path directly: the worktree the caller already occupies is the code
// under test, its HEAD the commit a passing result is attributed to, and its
// cleanliness the guard `test --log` refuses a dirty tree on. They reuse the same
// plain `git -C <path>` shell-out the Manager's land helpers use, so there is one
// idiom for reading a worktree's git state.

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Identify resolves the attempt a checkout belongs to from the branch it has
// checked out — draiver/<ticket>/<attempt> — so a command run from inside a
// managed worktree knows which attempt to attribute its writes to without being
// told. It errors when path is on a detached HEAD or a branch outside the managed
// draiver/ namespace (nothing to attribute), naming what it found.
func Identify(ctx context.Context, path string) (Key, error) {
	branch, ok, err := HeadBranch(ctx, path)
	if err != nil {
		return Key{}, err
	}
	if !ok {
		return Key{}, fmt.Errorf("worktree: %s is on a detached HEAD, not a draiver attempt branch", path)
	}
	k := keyFromBranch(branch)
	if k == (Key{}) {
		return Key{}, fmt.Errorf("worktree: branch %q of %s is not a draiver/<ticket>/<attempt> branch — run from an attempt worktree", branch, path)
	}
	return k, nil
}

// HeadSHA returns the full commit oid HEAD resolves to in the checkout at path —
// the commit a passing `test --log` result is attributed to.
func HeadSHA(ctx context.Context, path string) (string, error) {
	out, err := gitOutput(ctx, path, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("worktree: head sha of %s: %w", path, err)
	}
	return strings.TrimSpace(out), nil
}

// DirtyAt reports whether the checkout at path holds uncommitted or untracked
// changes — the guard `test --log` refuses on, so a recorded result names a
// commit that fully captures the tree that was tested. It mirrors the Manager's
// dirtyAt: `git status --porcelain`, which lists untracked files by default.
func DirtyAt(ctx context.Context, path string) (bool, error) {
	out, err := gitOutput(ctx, path, "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("worktree: status %s: %w", path, err)
	}
	return strings.TrimSpace(out) != "", nil
}

// AheadBehind reports how many commits the local branch is ahead of and behind its
// counterpart on remote — the tracking ref refs/remotes/<remote>/<branch> — using a
// purely local rev-list (it never fetches, so it reflects the last-known remote
// state, the same best-effort read the Git-controls position indicator wants).
//
// ok is false — the caller shows "unknown" / omits the count — when the comparison
// can't be made: the tracking ref is missing (the branch was never pushed, "no
// upstream"), or any git error. It is the path-addressed sibling of HeadSHA/DirtyAt,
// read live behind the web layer's injectable git reader.
func AheadBehind(ctx context.Context, path, remote, branch string) (ahead, behind int, ok bool) {
	ref := remote + "/" + branch
	// The tracking ref must exist, or there is no upstream to compare against.
	if _, err := gitOutput(ctx, path, "rev-parse", "--verify", "--quiet", "refs/remotes/"+ref); err != nil {
		return 0, 0, false
	}
	// `--left-right --count A...B` prints "<left>\t<right>": left = commits reachable
	// from the tracking ref but not the branch (behind), right = the reverse (ahead).
	out, err := gitOutput(ctx, path, "rev-list", "--left-right", "--count", ref+"..."+branch)
	if err != nil {
		return 0, 0, false
	}
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return 0, 0, false
	}
	b, berr := strconv.Atoi(fields[0])
	a, aerr := strconv.Atoi(fields[1])
	if berr != nil || aerr != nil {
		return 0, 0, false
	}
	return a, b, true
}

// gitOutput runs `git -C dir args...` and returns trimmed-nothing stdout, folding
// stderr into the error for a legible message. It is the package-level sibling of
// the Manager's gitIn, for the standalone path-addressed helpers above that a
// worktree-standing command uses without constructing a Manager.
func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("%w: %s", err, msg)
		}
		return "", err
	}
	return stdout.String(), nil
}
