package brief

// Branch state (drv-064): a short block of facts read from git — not from the log —
// about what the attempt's branch already holds and how far its base has moved. A
// resumed agent otherwise re-derives this by re-reading diffs (tens of thousands of
// tokens in drv-045/046), or gets it wrong with a two-dot diff against a moved base
// (drv-047). Agents' notes can be stale or wrong; git at brief time is not.
//
// The block is split in two: gatherBranchState reads raw facts from the attempt's
// live worktree, and renderBranchState applies the caps and formats them. The caps
// keep the block bounded (at most maxBranchStateLines lines) whatever the branch
// holds, so it can never bloat the brief.

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/worktree"
)

// Caps on each list in the block. With the fixed header/footer lines the worst case
// is maxBranchStateLines; TestRenderBranchStateWorstCaseIsBounded holds it there.
const (
	maxDiffFiles        = 14
	maxStatusLines      = 6
	maxBaseCommits      = 5
	maxBranchStateLines = 40
)

// gitTimeout bounds every git read the block makes, so a wedged repo cannot hang a
// cold-start: the block is skipped instead.
const gitTimeout = 10 * time.Second

// diffFile is one line of `git diff --numstat`: a changed path and its line counts.
// Binary files have no line counts (Binary is set).
type diffFile struct {
	Path     string
	Add, Del int
	Binary   bool
}

// branchState is the raw git facts the block renders. Base fields are empty when
// the attempt records no base or the base ref does not resolve; the block then
// shows the branch and uncommitted changes only.
type branchState struct {
	Branch    string // short branch name; empty on a detached HEAD
	Head      string // abbreviated HEAD oid
	Base      string // base branch name (from attempt.md)
	BaseTip   string // abbreviated base oid; empty when the base does not resolve
	MergeBase string // abbreviated merge-base oid; empty when there is none

	Diff   []diffFile // `git diff --numstat <merge-base>...HEAD`, uncapped
	Status []string   // `git status --porcelain` lines, uncapped

	BaseAhead   int      // commits on base since the merge-base
	BaseCommits []string // `%h %s` of the newest of them, at most maxBaseCommits
}

// gatherBranchState is the hook Build calls. It is a package variable so tests can
// stub the git read out; the default reads the attempt's live worktree.
var gatherBranchState = gatherFromWorktree

// gatherFromWorktree locates the attempt's managed worktree and reads its branch
// state. ok is false — and the brief omits the block — when the attempt names no
// repo, the repo is not a git tree, no worktree exists for the attempt (never
// created, or reclaimed), or HEAD cannot be read. Every failure is a quiet skip: the
// block is an aid, never a reason for the brief to fail.
func gatherFromWorktree(a project.Attempt) (branchState, bool) {
	if strings.TrimSpace(a.Repo) == "" {
		return branchState{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	wm, err := worktree.NewManager(a.Repo)
	if err != nil {
		return branchState{}, false
	}
	wt, ok, err := wm.Locate(ctx, worktree.Key{Ticket: a.Ticket, Attempt: a.ID})
	if err != nil || !ok || wt.Prunable {
		return branchState{}, false
	}
	return readBranchState(ctx, wt.Path, strings.TrimSpace(a.Base))
}

// readBranchState reads the block's facts from the checkout at dir, measured
// against the local base branch. It uses the local ref, not a remote-tracking one:
// that is what the worktree was cut from and what `git diff <base>...HEAD` in the
// worktree compares against, so the numbers match what the agent will see.
func readBranchState(ctx context.Context, dir, base string) (branchState, bool) {
	git := func(args ...string) (string, error) {
		return gitIn(ctx, dir, args...)
	}
	head, err := git("rev-parse", "--short", "HEAD")
	if err != nil {
		return branchState{}, false
	}
	st := branchState{Head: strings.TrimSpace(head), Base: base}
	if b, err := git("symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
		st.Branch = strings.TrimSpace(b)
	}
	if out, err := git("status", "--porcelain"); err == nil {
		st.Status = nonEmptyLines(out)
	}
	if base == "" {
		return st, true
	}
	tip, err := git("rev-parse", "--verify", "--quiet", "--short", base+"^{commit}")
	if err != nil {
		return st, true
	}
	st.BaseTip = strings.TrimSpace(tip)
	mb, err := git("merge-base", "HEAD", base)
	if err != nil {
		return st, true // unrelated histories: no merge-base to measure from
	}
	mbFull := strings.TrimSpace(mb)
	if short, err := git("rev-parse", "--short", mbFull); err == nil {
		st.MergeBase = strings.TrimSpace(short)
	} else {
		st.MergeBase = mbFull
	}
	if out, err := git("diff", "--numstat", mbFull+"...HEAD"); err == nil {
		st.Diff = parseNumstat(out)
	}
	if out, err := git("rev-list", "--count", mbFull+".."+base); err == nil {
		st.BaseAhead, _ = strconv.Atoi(strings.TrimSpace(out))
	}
	if st.BaseAhead > 0 {
		if out, err := git("log", "--no-decorate", "--format=%h %s", "-n", strconv.Itoa(maxBaseCommits), mbFull+".."+base); err == nil {
			st.BaseCommits = nonEmptyLines(out)
		}
	}
	return st, true
}

// parseNumstat parses `git diff --numstat` output. Binary files report "-" for both
// counts.
func parseNumstat(out string) []diffFile {
	var files []diffFile
	for _, line := range nonEmptyLines(out) {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		f := diffFile{Path: parts[2]}
		if parts[0] == "-" && parts[1] == "-" {
			f.Binary = true
		} else {
			f.Add, _ = strconv.Atoi(parts[0])
			f.Del, _ = strconv.Atoi(parts[1])
		}
		files = append(files, f)
	}
	return files
}

// renderBranchState formats the block, applying every cap. Its output never
// exceeds maxBranchStateLines lines.
func renderBranchState(st branchState) string {
	var b strings.Builder
	b.WriteString("## Branch state (read from git when this brief was built)\n\n")

	branch := "detached HEAD"
	if st.Branch != "" {
		branch = "`" + st.Branch + "`"
	}
	fmt.Fprintf(&b, "Branch %s @ `%s`", branch, st.Head)
	switch {
	case st.Base == "":
		b.WriteString(" · no base branch recorded for this attempt")
	case st.BaseTip == "":
		fmt.Fprintf(&b, " · base `%s` does not resolve here", st.Base)
	case st.MergeBase == "":
		fmt.Fprintf(&b, " · base `%s` @ `%s` · no merge-base (unrelated histories)", st.Base, st.BaseTip)
	default:
		fmt.Fprintf(&b, " · base `%s` @ `%s` · merge-base `%s`", st.Base, st.BaseTip, st.MergeBase)
	}
	b.WriteString("\n\n")

	if st.MergeBase != "" {
		if len(st.Diff) == 0 {
			fmt.Fprintf(&b, "Committed on this branch since the merge-base (`git diff --stat %s...HEAD`): nothing yet.\n\n", st.MergeBase)
		} else {
			add, del := 0, 0
			for _, f := range st.Diff {
				add += f.Add
				del += f.Del
			}
			fmt.Fprintf(&b, "Committed on this branch since the merge-base (`git diff --stat %s...HEAD`): %s, +%d −%d\n",
				st.MergeBase, plural(len(st.Diff), "file"), add, del)
			for i, f := range st.Diff {
				if i == maxDiffFiles {
					fmt.Fprintf(&b, "- …and %d more\n", len(st.Diff)-maxDiffFiles)
					break
				}
				if f.Binary {
					fmt.Fprintf(&b, "- `%s` (binary)\n", f.Path)
				} else {
					fmt.Fprintf(&b, "- `%s` +%d −%d\n", f.Path, f.Add, f.Del)
				}
			}
			b.WriteString("\n")
		}
	}

	if len(st.Status) == 0 {
		b.WriteString("Uncommitted changes (`git status --short`): none — the tree is clean.\n\n")
	} else {
		fmt.Fprintf(&b, "Uncommitted changes (`git status --short`): %s\n", plural(len(st.Status), "path"))
		for i, l := range st.Status {
			if i == maxStatusLines {
				fmt.Fprintf(&b, "- …and %d more\n", len(st.Status)-maxStatusLines)
				break
			}
			fmt.Fprintf(&b, "- `%s`\n", l)
		}
		b.WriteString("\n")
	}

	if st.MergeBase != "" {
		if st.BaseAhead == 0 {
			fmt.Fprintf(&b, "Base `%s` has no new commits since your merge-base.\n\n", st.Base)
		} else {
			fmt.Fprintf(&b, "Base `%s` has %s since your merge-base (not on your branch):\n", st.Base, plural(st.BaseAhead, "commit"))
			shown := st.BaseCommits
			if len(shown) > maxBaseCommits {
				shown = shown[:maxBaseCommits]
			}
			for _, c := range shown {
				fmt.Fprintf(&b, "- %s\n", c)
			}
			if rest := st.BaseAhead - len(shown); rest > 0 {
				fmt.Fprintf(&b, "- …and %d more\n", rest)
			}
			b.WriteString("\n")
		}
	}

	base := st.Base
	if base == "" {
		base = "<base>"
	}
	fmt.Fprintf(&b, "Compare with three dots: `git diff %s...HEAD` shows only your branch's changes; "+
		"two dots would also show everything that landed on `%s` since your cut, as if you had deleted it.\n\n", base, base)
	return b.String()
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, strings.TrimRight(l, "\r"))
		}
	}
	return out
}

// gitIn runs `git -C dir args...` and returns stdout, folding stderr into the error.
func gitIn(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("%w: %s", err, msg)
		}
		return "", err
	}
	return stdout.String(), nil
}
