// Package pyramid loads a repo's test-pyramid declaration — the ordered ladder of
// verification levels a worktree ships in its .test-pyramid.yaml. It is the first
// rung of the test-pyramid epic (drv-009) and is deliberately data-model only: it
// parses, validates, and exposes structure. Execution, gating, logging, and UI are
// later tickets.
//
// The declaration is the generic, loosely-coupled verification twin of the
// `git remote` forge coupling: a repo says how it wants to be verified without the
// supervisor hardcoding any one toolchain. Resolution mirrors config.Load's
// missing-file-is-fine discipline — a repo that declares no pyramid is a valid
// state, not an error — but keeps "absent" distinguishable from "present" because
// having no declared pyramid is itself a meaningful, useful answer here.
package pyramid

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// FileName is the fixed basename resolved under a worktree path. It sits at the
// repo root, alongside the code it verifies.
const FileName = ".test-pyramid.yaml"

// Level is one rung of the pyramid: a named verification step and the shell
// command that runs it. v1 is deliberately minimal — a level's own Run does any
// env bring-up and teardown, so there is no up/down/healthcheck/timeout yet. Those
// can be added as new fields without a schema break because Load ignores unknown
// keys (see Load).
type Level struct {
	// Name is a unique, non-blank identifier for the rung (e.g. "unit").
	Name string `yaml:"name"`
	// Run is the shell command that executes the rung. Non-blank.
	Run string `yaml:"run"`
}

// Pyramid is a repo's parsed, validated test-pyramid declaration. Levels is an
// ordered list, bottom-to-top: the list order IS the pyramid, so the last entry is
// the target (top) rung.
type Pyramid struct {
	Levels []Level `yaml:"levels"`
}

// Target returns the top (last) rung — the target the pyramid climbs toward. It is
// safe to call on any Pyramid returned by Load, since Load validates that Levels is
// non-empty.
func (p *Pyramid) Target() Level {
	return p.Levels[len(p.Levels)-1]
}

// FloorTarget returns the rung a consumer should target given the file's ladder and
// an optional repo-configured default rung (drv-011's default_test_rung). The repo
// default acts as a FLOOR, never lowering the file's top-rung bar: the result is the
// higher-in-climb-order of the file's top rung and repoRung. An empty repoRung, or
// one naming a rung not in this pyramid (a typo, or a level since removed/renamed),
// leaves the target at the file's top rung.
//
// Because the top rung is the maximum climb index, a floor can only ever raise the
// bar, never lower it — so repo config can tighten a review but structurally cannot
// weaken one below what the .test-pyramid.yaml author designated. That is the
// deliberate, conservative resolution of the file-top-vs-repo-default open question
// (the spec's recommendation): the read is honoured, the gate is never softened.
func (p *Pyramid) FloorTarget(repoRung string) Level {
	top := p.Target()
	if repoRung == "" {
		return top
	}
	topIdx := len(p.Levels) - 1
	for i, lv := range p.Levels {
		if lv.Name == repoRung && i > topIdx {
			return lv
		}
	}
	return top
}

// Result is one logged rung outcome the review gate folds over: a rung that was
// recorded green (drvctl-048 only logs passing results) and the commit it passed
// at. It is a neutral pair deliberately decoupled from internal/event, so this
// data-model package stays free of the log schema — the caller maps its
// test-result events onto Results.
type Result struct {
	Rung   string
	Commit string
}

// HighestAtHEAD folds results down to "the highest logged rung at this commit":
// the top-most rung in climb order that has a Result whose Commit equals head. A
// Result naming a rung not in this pyramid (a renamed or removed level) is
// ignored, and a Result whose Commit != head is stale — it does not count, so a
// commit made after logging correctly invalidates it. ok is false when no result
// matches HEAD at all, i.e. nothing was climbed at this commit.
//
// The fold is trivial precisely because only green results are logged: there is
// no failing/partial state to reconcile, just the highest rung present at HEAD.
func (p *Pyramid) HighestAtHEAD(results []Result, head string) (Level, bool) {
	index := make(map[string]int, len(p.Levels))
	for i, lv := range p.Levels {
		index[lv.Name] = i
	}
	best := -1
	for _, r := range results {
		if r.Commit != head {
			continue
		}
		if i, ok := index[r.Rung]; ok && i > best {
			best = i
		}
	}
	if best < 0 {
		return Level{}, false
	}
	return p.Levels[best], true
}

// Load resolves and reads .test-pyramid.yaml from a worktree path.
//
// A missing file is a valid, non-error state: it returns (nil, nil), meaning "this
// repo declares no pyramid", mirroring config.Load's missing-file-is-fine
// discipline. A present file is parsed and validated; a malformed or invalid file
// IS an error, so a typo is surfaced rather than silently ignored (unlike a missing
// one).
//
// Unknown mapping keys are ignored (yaml.v3's default decode), which keeps the
// parser forward-compatible: a newer file carrying a not-yet-known key (a future
// up/down/healthcheck/timeout) still loads under an older binary without a schema
// break.
func Load(worktreePath string) (*Pyramid, error) {
	path := filepath.Join(worktreePath, FileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read pyramid %s: %w", path, err)
	}
	p, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}

// Parse decodes and validates a pyramid from raw .test-pyramid.yaml bytes — the
// content-addressed core of Load, for callers that hold the file's bytes rather
// than a directory on disk (e.g. a read-only board reading it from a branch ref
// via `git show <branch>:.test-pyramid.yaml`, with no live worktree to Load from).
// Unlike Load, an empty/absent file is the caller's concern: Parse validates that
// there is at least one level.
func Parse(data []byte) (*Pyramid, error) {
	var p Pyramid
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse pyramid: %w", err)
	}
	if err := p.validate(); err != nil {
		return nil, fmt.Errorf("invalid pyramid: %w", err)
	}
	return &p, nil
}

// validate enforces the v1 invariants: at least one level, every name unique and
// non-blank, every run non-blank. Blank means empty-after-trim, so a name or run of
// only whitespace is rejected as the typo it almost certainly is.
func (p *Pyramid) validate() error {
	if len(p.Levels) == 0 {
		return errors.New("levels is empty: a pyramid needs at least one level")
	}
	seen := make(map[string]bool, len(p.Levels))
	for i, lv := range p.Levels {
		name := strings.TrimSpace(lv.Name)
		if name == "" {
			return fmt.Errorf("level %d: name is blank", i)
		}
		if seen[name] {
			return fmt.Errorf("level %d: duplicate name %q", i, name)
		}
		seen[name] = true
		if strings.TrimSpace(lv.Run) == "" {
			return fmt.Errorf("level %d (%q): run is blank", i, name)
		}
	}
	return nil
}
