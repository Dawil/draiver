package report

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Dawil/draiver/internal/store"
)

// BDDArtefactSubdir is the root, under an attempt's artefacts/ dir, beneath which
// drv-017 captures BDD runs. The full per-run key is
//
//	artefacts/bdd/<rung>/<env>/<commit>/<runstamp>/…
//
// so two regenerations at the same commit/env coexist side by side under distinct
// runstamps and a prior run is never clobbered. drv-018 only *reads* this tree;
// drv-017 owns writing it. This constant plus runKeyDepth is the integration
// contract between the two tickets — keep it in sync with drv-017.
const BDDArtefactSubdir = "bdd"

// runKeyDepth is the number of path segments below bdd/ that address one run:
// <rung>/<env>/<commit>/<runstamp>.
const runKeyDepth = 4

// cucumberJSONNames are the filenames, in preference order, a run's cucumber-JSON
// may use. The first that exists in a run dir is the report source.
var cucumberJSONNames = []string{"cucumber.json", "report.json", "results.json"}

// Run is one captured BDD run discovered under an attempt's artefact store. It is
// the unit the webui selects among: runs sharing (Rung, Env, Commit) but with
// different Runstamp are side-by-side regenerations of the same scenarios.
type Run struct {
	Rung     string
	Env      string
	Commit   string
	Runstamp string

	// Rel is the run directory relative to the attempt's artefacts/ dir, using
	// forward slashes — e.g. "bdd/unit/ci/abc1234/20260101T120000Z". It doubles as
	// the run's stable id in webui URLs.
	Rel string
	// Dir is the absolute run directory on disk.
	Dir string
	// CucumberJSON is the absolute path to the run's cucumber-JSON, or "" if the
	// run dir has none (a partial/failed capture).
	CucumberJSON string
}

// ID is the run's stable identifier for URLs and selection — its Rel path.
func (r Run) ID() string { return r.Rel }

// ShortCommit is the commit trimmed to 8 chars for compact display.
func (r Run) ShortCommit() string {
	if len(r.Commit) > 8 {
		return r.Commit[:8]
	}
	return r.Commit
}

// LoadReport reads and parses the run's cucumber-JSON.
func (r Run) LoadReport() (*Report, error) {
	if r.CucumberJSON == "" {
		return nil, fmt.Errorf("report: run %s has no cucumber-JSON", r.Rel)
	}
	data, err := os.ReadFile(r.CucumberJSON)
	if err != nil {
		return nil, fmt.Errorf("report: read %s: %w", r.Rel, err)
	}
	return Parse(data)
}

// DiscoverRuns lists the BDD runs captured for an attempt, newest-first (by
// runstamp, descending). A missing artefacts/ or bdd/ tree is treated as "no
// runs" — not an error — so the webui renders an empty state while drv-017's
// capture has not yet written anything for this attempt.
func DiscoverRuns(root store.Root, id, attempt string) ([]Run, error) {
	base := filepath.Join(root.ArtefactsDir(id, attempt), BDDArtefactSubdir)
	var runs []Run
	err := walkRunDirs(base, func(segments []string, abs string) {
		runs = append(runs, newRun(segments, abs))
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(runs, func(i, j int) bool {
		if runs[i].Runstamp != runs[j].Runstamp {
			return runs[i].Runstamp > runs[j].Runstamp // newest first
		}
		return runs[i].Rel < runs[j].Rel
	})
	return runs, nil
}

// LatestRun returns the newest run, or ok=false if there are none.
func LatestRun(root store.Root, id, attempt string) (Run, bool, error) {
	runs, err := DiscoverRuns(root, id, attempt)
	if err != nil {
		return Run{}, false, err
	}
	if len(runs) == 0 {
		return Run{}, false, nil
	}
	return runs[0], true, nil
}

// FindRun resolves a run by its Rel id, guarding against path traversal and
// returning ok=false if no such run exists for the attempt.
func FindRun(root store.Root, id, attempt, relID string) (Run, bool, error) {
	clean := path.Clean("/" + strings.ReplaceAll(relID, "\\", "/"))[1:]
	if clean == "" || !strings.HasPrefix(clean+"/", BDDArtefactSubdir+"/") {
		return Run{}, false, nil
	}
	runs, err := DiscoverRuns(root, id, attempt)
	if err != nil {
		return Run{}, false, err
	}
	for _, r := range runs {
		if r.Rel == clean {
			return r, true, nil
		}
	}
	return Run{}, false, nil
}

// RunGroup is a set of side-by-side regenerations: runs that share rung, env and
// commit, ordered newest-first. The webui offers these as the choices for "pick
// among regenerations of a scenario".
type RunGroup struct {
	Rung   string
	Env    string
	Commit string
	Runs   []Run
}

// GroupRuns buckets runs into regeneration groups by (rung, env, commit),
// preserving the newest-first order within each group and ordering groups by
// their newest run.
func GroupRuns(runs []Run) []RunGroup {
	index := map[string]int{}
	var groups []RunGroup
	for _, r := range runs {
		key := r.Rung + "\x00" + r.Env + "\x00" + r.Commit
		gi, ok := index[key]
		if !ok {
			gi = len(groups)
			index[key] = gi
			groups = append(groups, RunGroup{Rung: r.Rung, Env: r.Env, Commit: r.Commit})
		}
		groups[gi].Runs = append(groups[gi].Runs, r)
	}
	return groups
}

// newRun builds a Run from the four key segments and the absolute dir, locating
// its cucumber-JSON.
func newRun(segments []string, abs string) Run {
	r := Run{
		Rung:     segments[0],
		Env:      segments[1],
		Commit:   segments[2],
		Runstamp: segments[3],
		Dir:      abs,
		Rel:      path.Join(append([]string{BDDArtefactSubdir}, segments...)...),
	}
	for _, name := range cucumberJSONNames {
		p := filepath.Join(abs, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			r.CucumberJSON = p
			break
		}
	}
	return r
}

// walkRunDirs descends exactly runKeyDepth directory levels below base and calls
// fn with the path segments (rung,env,commit,runstamp) and absolute dir for each
// run. A missing base is not an error.
func walkRunDirs(base string, fn func(segments []string, abs string)) error {
	var recurse func(dir string, segs []string) error
	recurse = func(dir string, segs []string) error {
		if len(segs) == runKeyDepth {
			fn(append([]string(nil), segs...), dir)
			return nil
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("report: scan runs under %s: %w", dir, err)
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if err := recurse(filepath.Join(dir, e.Name()), append(segs, e.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	return recurse(base, nil)
}
