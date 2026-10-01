// Package bddartefact captures a BDD/acceptance run's evidence — the
// cucumber-JSON, embeddings (screenshots), and any declared script-output files —
// into an attempt's per-attempt artefacts/ store under a per-run key, and returns
// the artefacts-relative refs a test-result event attaches (drv-017).
//
// The evidence is reproducible, not version-controlled: it lives in the data root
// (not the code repo), referenced from the hash-chained log so provenance holds
// (`draiver audit`) and `brief` can surface it. Multiple regenerations of the same
// artefact coexist side by side — the per-run key is never clobbered — so a prior
// run is always recoverable.
//
// This package is deliberately runner-agnostic and decoupled from how a run is
// executed (drv-016) or rendered (drv-018): a caller hands it the files a run
// produced plus the run's context, and it owns only the on-disk layout under
// artefacts/ and the retention policy.
package bddartefact

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Prefix is the top-level namespace under an attempt's artefacts/ dir that every
// captured BDD run lives beneath, keeping runs cleanly separable from any other
// blob an attempt's log references.
const Prefix = "bdd"

// runMetaName is the per-run reproducibility record written into each run dir and
// referenced from the test-result event alongside the captured files.
const runMetaName = "run.json"

// RunContext is the reproducibility context recorded alongside a captured run:
// enough to make "re-run this and compare" meaningful. Commit and Runstamp key the
// run; Environment and Healthcheck record what it ran against (Healthcheck is empty
// until environments land in drv-012).
type RunContext struct {
	Rung        string `json:"rung"`
	Environment string `json:"environment"`
	Commit      string `json:"commit"`
	Runstamp    string `json:"runstamp"`
	Healthcheck string `json:"healthcheck,omitempty"`
	// Captured lists the run dir's captured entries (basenames), filled in by
	// Capture so run.json is a self-describing manifest of the run.
	Captured []string `json:"captured,omitempty"`
}

// Source is one artefact a run produced to capture: a file or a directory in the
// worktree (a directory — e.g. an embeddings/screenshots folder — is copied
// recursively). As overrides the destination basename under the run dir; empty
// takes the source's own basename.
type Source struct {
	Path string
	As   string
}

// Env returns the environment label for keying, defaulting a blank to "none" so a
// rung with no environment binding (pre-drv-012) still produces a stable key.
func (rc RunContext) Env() string {
	if strings.TrimSpace(rc.Environment) == "" {
		return "none"
	}
	return rc.Environment
}

// GroupKey is the artefacts-relative slash path grouping every run of one
// (rung, env): bdd/<rung>/<env>. Retention (Prune) operates over this group.
func (rc RunContext) GroupKey() string {
	return path.Join(Prefix, sanitize(rc.Rung), sanitize(rc.Env()))
}

// keyBase is the un-deduplicated per-run key: bdd/<rung>/<env>/<commit>/<runstamp>.
// Capture resolves it to a unique dir, never clobbering a prior run.
func (rc RunContext) keyBase() string {
	return path.Join(rc.GroupKey(), sanitize(rc.Commit), sanitize(rc.Runstamp))
}

// Capture copies sources into a fresh per-run directory under artefactsDir, writes
// run.json describing the run, and returns the run key actually used (an
// artefacts-relative slash path) plus the refs to attach to the test-result
// event's Artefacts: field — the captured entries and run.json, sorted.
//
// It never clobbers: if the computed key dir already exists, a numeric suffix is
// appended, so two runs at the same rung/env/commit/runstamp coexist side by side.
// A declared source that does not exist is an error — a BDD run is expected to
// produce the outputs it declares, so a missing one is surfaced, not swallowed.
func Capture(artefactsDir, worktree string, rc RunContext, sources []Source) (string, []string, error) {
	key, runDir, err := uniqueRunDir(artefactsDir, rc.keyBase())
	if err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return "", nil, fmt.Errorf("create run dir: %w", err)
	}

	var captured []string
	refs := []string{}
	for _, s := range sources {
		src := s.Path
		if !filepath.IsAbs(src) {
			src = filepath.Join(worktree, src)
		}
		dest := strings.TrimSpace(s.As)
		if dest == "" {
			dest = filepath.Base(src)
		}
		fi, err := os.Stat(src)
		if err != nil {
			return "", nil, fmt.Errorf("capture source %q: %w", s.Path, err)
		}
		if fi.IsDir() {
			err = copyTree(src, filepath.Join(runDir, dest))
		} else {
			err = copyFile(src, filepath.Join(runDir, dest))
		}
		if err != nil {
			return "", nil, fmt.Errorf("capture %q: %w", s.Path, err)
		}
		captured = append(captured, dest)
		refs = append(refs, path.Join(key, dest))
	}

	rc.Captured = captured
	if err := writeRunMeta(filepath.Join(runDir, runMetaName), rc); err != nil {
		return "", nil, err
	}
	refs = append(refs, path.Join(key, runMetaName))
	sort.Strings(refs)
	return key, refs, nil
}

// Prune enforces an additive retention cap within a single (rung, env) group: it
// keeps the `keep` most-recent run directories and removes the rest. Runs are
// ordered by their runstamp segment (a UTC stamp, so lexical order is
// chronological). keep <= 0 keeps everything — the default, no GC. It returns the
// artefacts-relative keys of the runs removed, sorted.
func Prune(artefactsDir, rung, env string, keep int) ([]string, error) {
	if keep <= 0 {
		return nil, nil
	}
	group := (RunContext{Rung: rung, Environment: env}).GroupKey()
	groupDir := filepath.Join(artefactsDir, filepath.FromSlash(group))
	runs, err := runDirsUnder(artefactsDir, groupDir)
	if err != nil {
		return nil, err
	}
	if len(runs) <= keep {
		return nil, nil
	}
	// runs is sorted oldest→newest; drop everything but the newest `keep`.
	victims := runs[:len(runs)-keep]
	removed := make([]string, 0, len(victims))
	for _, r := range victims {
		if err := os.RemoveAll(filepath.Join(artefactsDir, filepath.FromSlash(r))); err != nil {
			return nil, fmt.Errorf("prune %q: %w", r, err)
		}
		removed = append(removed, r)
	}
	sort.Strings(removed)
	return removed, nil
}

// runDirsUnder returns the artefacts-relative keys of every run dir beneath
// groupDir (a dir holding a run.json manifest), sorted by runstamp (the key's last
// segment) so the order is oldest→newest. A missing group dir is empty, not an
// error.
func runDirsUnder(artefactsDir, groupDir string) ([]string, error) {
	var keys []string
	err := filepath.WalkDir(groupDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return filepath.SkipDir
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		if d.Name() != runMetaName {
			return nil
		}
		rel, err := filepath.Rel(artefactsDir, filepath.Dir(p))
		if err != nil {
			return err
		}
		keys = append(keys, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan runs under %s: %w", groupDir, err)
	}
	sort.Slice(keys, func(i, j int) bool {
		return path.Base(keys[i]) < path.Base(keys[j])
	})
	return keys, nil
}

// uniqueRunDir resolves keyBase to an artefacts-relative key whose on-disk dir does
// not yet exist, appending -2, -3, … to the final segment on collision so a prior
// run is never clobbered. It returns the key and its absolute dir.
func uniqueRunDir(artefactsDir, keyBase string) (string, string, error) {
	key := keyBase
	for i := 1; ; i++ {
		if i > 1 {
			key = fmt.Sprintf("%s-%d", keyBase, i)
		}
		dir := filepath.Join(artefactsDir, filepath.FromSlash(key))
		_, err := os.Stat(dir)
		if os.IsNotExist(err) {
			return key, dir, nil
		}
		if err != nil {
			return "", "", fmt.Errorf("resolve run dir: %w", err)
		}
		// exists → try the next suffix
	}
}

func writeRunMeta(path string, rc RunContext) error {
	data, err := json.MarshalIndent(rc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal run meta: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write run meta: %w", err)
	}
	return nil
}

// sanitize keeps a key segment filesystem- and ref-safe: anything outside
// [A-Za-z0-9._-] becomes '-', and an empty result becomes "none" so a key segment
// is never blank.
func sanitize(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "none"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(p, target)
	})
}
