package web

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

// gitrefs.go is the subprocess-free ref reader behind the pyramid cache (drvweb-030).
// The cache must decide "did this attempt's git state change?" for ~200 cards every
// 3s poll; doing that with `git rev-parse` per card is exactly the cost the cache
// exists to remove. So the cheap check resolves refs straight from the files git's
// default "files" ref backend keeps: a loose ref under <common-dir>/refs/..., else the
// <common-dir>/packed-refs table (parsed once per change of the file), following
// symbolic refs ("ref: refs/heads/main") a few levels deep.
//
// It never writes and never takes git's locks: loose refs are replaced by an atomic
// rename, so a read sees the old or the new SHA, never a torn one. Anything it does
// not understand — a reftable repo, a malformed ref file — is errRefsUnsupported /
// an error, and the caller bypasses the cache for that read (the uncached gatherer
// stays the authority), so a gap here can cost speed but never correctness.

// errRefsUnsupported reports a ref store this reader does not parse (reftable).
var errRefsUnsupported = errors.New("gitrefs: unsupported ref storage")

// gitDirs is where a repo's refs live: the common dir (shared refs, packed-refs,
// the worktrees/ admin dir) and the repo path's own git dir (its HEAD). For a
// non-linked checkout the two are the same directory.
type gitDirs struct {
	Common string
	GitDir string
}

// readGitDirs resolves gitDirs for any path inside a working tree — one subprocess,
// which the cache memoizes per repo snapshot rather than paying per card.
func readGitDirs(ctx context.Context, repo string) (gitDirs, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "rev-parse", "--path-format=absolute", "--git-common-dir", "--git-dir")
	out, err := cmd.Output()
	if err != nil {
		return gitDirs{}, fmt.Errorf("gitrefs: rev-parse %s: %w", repo, err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 {
		return gitDirs{}, fmt.Errorf("gitrefs: rev-parse %s: unexpected output %q", repo, out)
	}
	return gitDirs{Common: filepath.Clean(lines[0]), GitDir: filepath.Clean(lines[1])}, nil
}

// refReader resolves refs from disk, memoizing each common dir's parsed packed-refs
// keyed on the file's identity (inode/size/mtime), so the table is re-parsed only
// when git rewrites it. It is safe for concurrent use.
type refReader struct {
	mu     sync.Mutex
	packed map[string]*packedRefs // by common dir
}

type packedRefs struct {
	info fs.FileInfo // nil when the file is absent
	refs map[string]string
}

func newRefReader() *refReader { return &refReader{packed: map[string]*packedRefs{}} }

// resolve returns the commit a ref names. found is false when the ref does not exist
// (no loose file, no packed-refs line) — a benign "no such branch". ref is either
// "HEAD" (read from dirs.GitDir) or a full "refs/..." name.
func (r *refReader) resolve(dirs gitDirs, ref string) (sha string, found bool, err error) {
	if _, err := os.Stat(filepath.Join(dirs.Common, "reftable")); err == nil {
		return "", false, errRefsUnsupported
	}
	for range 5 { // bounded symref chain, like git's own depth limit
		var path string
		if ref == "HEAD" {
			path = filepath.Join(dirs.GitDir, "HEAD")
		} else {
			if !strings.HasPrefix(ref, "refs/") || strings.Contains(ref, "..") {
				return "", false, fmt.Errorf("gitrefs: refusing ref %q", ref)
			}
			path = filepath.Join(dirs.Common, filepath.FromSlash(ref))
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			if !absentRef(rerr) {
				return "", false, fmt.Errorf("gitrefs: read %s: %w", path, rerr)
			}
			if ref == "HEAD" {
				return "", false, fmt.Errorf("gitrefs: %s has no HEAD", dirs.GitDir)
			}
			return r.lookupPacked(dirs.Common, ref)
		}
		val := strings.TrimSpace(string(data))
		if target, ok := strings.CutPrefix(val, "ref:"); ok {
			ref = strings.TrimSpace(target)
			continue
		}
		if !isHexSHA(val) {
			return "", false, fmt.Errorf("gitrefs: %s holds %q, not an object id", path, val)
		}
		return val, true, nil
	}
	return "", false, fmt.Errorf("gitrefs: symbolic ref chain too deep at %q", ref)
}

// absentRef reports whether a loose-ref read failed only because there is no loose
// ref at that path: it does not exist, a parent is a file, or the path is a directory
// (a ref namespace, e.g. refs/heads/draiver/T when asked for the branch draiver/T).
func absentRef(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) || errors.Is(err, syscall.EISDIR)
}

// lookupPacked consults <common>/packed-refs, re-parsing it only when the file has
// changed since the last parse.
func (r *refReader) lookupPacked(common, ref string) (string, bool, error) {
	path := filepath.Join(common, "packed-refs")
	info, err := os.Stat(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", false, fmt.Errorf("gitrefs: stat %s: %w", path, err)
	}
	if err != nil {
		info = nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	pr := r.packed[common]
	if pr == nil || !sameFileState(pr.info, info) {
		pr = &packedRefs{info: info, refs: map[string]string{}}
		if info != nil {
			data, err := os.ReadFile(path)
			if err != nil {
				return "", false, fmt.Errorf("gitrefs: read %s: %w", path, err)
			}
			pr.refs = parsePackedRefs(data)
		}
		r.packed[common] = pr
	}
	sha, ok := pr.refs[ref]
	return sha, ok, nil
}

// parsePackedRefs reads "<sha> <ref>" lines, skipping the "# pack-refs" header and
// "^<sha>" peeled-tag lines.
func parsePackedRefs(data []byte) map[string]string {
	refs := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line[0] == '#' || line[0] == '^' {
			continue
		}
		sha, name, ok := strings.Cut(line, " ")
		if ok && isHexSHA(sha) {
			refs[name] = sha
		}
	}
	return refs
}

// sameFileState reports whether two stats describe the same, unmodified file: the
// same inode (git replaces files by rename, so a rewrite gets a new one), size, and
// mtime. Two absent files (nil, nil) are the same state.
func sameFileState(a, b fs.FileInfo) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

// isHexSHA reports whether s is a full SHA-1 or SHA-256 object id.
func isHexSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
