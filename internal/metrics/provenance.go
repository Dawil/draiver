package metrics

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// gitTimeout bounds every git call metrics makes, so a wedged repo cannot hang the
// verb or the supervisor's bring-up.
const gitTimeout = 20 * time.Second

func git(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// Toplevel resolves a path inside a git working tree to the canonical toplevel of
// the repository's MAIN working tree (symlinks resolved). A path inside a linked
// worktree, such as an attempt's checkout, resolves to the repo it was cut from, so
// an attempt and its source repo agree on identity.
func Toplevel(path string) (string, error) {
	top, err := git(path, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	if common, err := git(path, "rev-parse", "--path-format=absolute", "--git-common-dir"); err == nil {
		if filepath.Base(common) == ".git" {
			return canonical(filepath.Dir(common)), nil
		}
	}
	return canonical(top), nil
}

// canonical returns an absolute, symlink-resolved, cleaned path, falling back to
// the cleaned absolute path when it cannot be resolved (e.g. it no longer exists).
func canonical(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return filepath.Clean(p)
}

// RepoID is the path-independent identity of the codebase at path: the normalized
// `origin` remote URL when there is one, else the canonical git toplevel. Two local
// clones of one remote therefore share a RepoID.
func RepoID(path string) (string, error) {
	top, err := Toplevel(path)
	if err != nil {
		return "", err
	}
	if url, err := git(top, "remote", "get-url", "origin"); err == nil && url != "" {
		return NormalizeRemote(url), nil
	}
	return top, nil
}

var scpLike = regexp.MustCompile(`^(?:[^@/]+@)?([^:/]+):(.+)$`)

// NormalizeRemote canonicalizes a git remote URL so the https, ssh and scp-like
// spellings of one repository compare equal: `host/owner/repo`, with scheme,
// credentials, port-less ssh user, a trailing `.git` and slashes removed, and the
// host lower-cased. A local-path remote is returned canonicalized.
func NormalizeRemote(url string) string {
	u := strings.TrimSpace(url)
	switch {
	case strings.Contains(u, "://"):
		u = u[strings.Index(u, "://")+3:]
		if at := strings.Index(u, "@"); at >= 0 && at < strings.IndexAny(u+"/", "/") {
			u = u[at+1:]
		}
	case filepath.IsAbs(u) || strings.HasPrefix(u, "."):
		return canonical(u)
	default:
		if m := scpLike.FindStringSubmatch(u); m != nil {
			u = m[1] + "/" + m[2]
		}
	}
	u = strings.TrimSuffix(strings.TrimRight(u, "/"), ".git")
	if i := strings.Index(u, "/"); i > 0 {
		u = strings.ToLower(u[:i]) + u[i:]
	}
	return u
}

// HeadSHA returns the commit checked out at dir.
func HeadSHA(dir string) (string, error) { return git(dir, "rev-parse", "HEAD") }

// AttemptBranch is the branch draiver cuts an attempt's worktree on.
func AttemptBranch(ticket, att string) string { return "draiver/" + ticket + "/" + att }

// InferBaseCommit infers an attempt's base commit after the fact: the merge-base of
// the attempt branch and its base branch. It needs both to still exist in repo.
func InferBaseCommit(repo, ticket, att, base string) (string, error) {
	if base == "" {
		base = "HEAD"
	}
	return git(repo, "merge-base", AttemptBranch(ticket, att), base)
}

// FinalDiff returns the files changed on the attempt branch relative to
// baseCommit, and the total changed line count (added + deleted).
func FinalDiff(repo, ticket, att, baseCommit string) (Diff, error) {
	if repo == "" || baseCommit == "" {
		return Diff{}, fmt.Errorf("metrics: final diff needs a repo and a base commit")
	}
	out, err := git(repo, "diff", "--numstat", "--no-renames", baseCommit, AttemptBranch(ticket, att), "--")
	if err != nil {
		return Diff{}, err
	}
	d := Diff{Available: true, Files: map[string]bool{}}
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(line, "\t", 3)
		if len(f) != 3 {
			continue
		}
		d.Files[f[2]] = true
		a, _ := strconv.Atoi(f[0]) // "-" for binary files counts as 0
		r, _ := strconv.Atoi(f[1])
		d.Lines += a + r
	}
	return d, nil
}

// DocsAtHead lists the agent-facing doc files (IsDoc) tracked at the repo's HEAD.
func DocsAtHead(repo string) ([]string, error) {
	out, err := git(repo, "ls-tree", "-r", "--name-only", "HEAD")
	if err != nil {
		return nil, err
	}
	var docs []string
	for _, f := range strings.Split(out, "\n") {
		if f != "" && IsDoc(f) {
			docs = append(docs, f)
		}
	}
	return docs, nil
}

// CaptureCut records the provenance of a freshly cut worktree at dir: the commit it
// was cut from (its HEAD before any agent work) and the codebase identity. The
// supervisor calls it once, when it first cuts the attempt's branch.
func CaptureCut(dir string) (baseCommit, repoID string, err error) {
	baseCommit, err = HeadSHA(dir)
	if err != nil {
		return "", "", err
	}
	repoID, err = RepoID(dir)
	return baseCommit, repoID, err
}
