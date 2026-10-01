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
// command that runs it. A level says *what the code is* (its Run); *where it runs*
// is factored out into a named Environment (drv-012), which a level references by
// name via Environment — unset means the implicit ambient context (today's
// behaviour), so existing files keep working.
type Level struct {
	// Name is a unique, non-blank identifier for the rung (e.g. "unit").
	Name string `yaml:"name"`
	// Run is the shell command that executes the rung. Non-blank.
	Run string `yaml:"run"`
	// Environment optionally names one entry in the pyramid's Environments list —
	// the context this rung executes in (local / staging / QA …). Blank is the
	// implicit ambient context: no up/down/healthcheck, run as today. A non-blank
	// name that resolves to no environment is a validation error (see validate).
	Environment string `yaml:"environment"`
}

// Healthcheck is one readiness probe of an Environment: a named shell command that
// must exit zero before a rung's Run executes. It probes a dependency draiver does
// NOT own (a sibling's deployed infra, a staging URL) — so a red probe is a
// dependency-not-ready *block*, not a code fault (see the control-outcome mapping
// in cmd/test.go). An environment may carry zero or more.
type Healthcheck struct {
	// Name is a non-blank label for the probe (e.g. "postgres-up"), used in output.
	Name string `yaml:"name"`
	// Script is the non-blank shell command whose exit code is the probe verdict.
	Script string `yaml:"script"`
}

// Environment is a named, reusable context a rung executes in: how to bring it Up,
// tear it Down, and probe whether its dependencies are ready (Healthchecks). It
// draws the same own-it-vs-observe-it line the forge coupling does — Up/Down are an
// env draiver *owns* (its ExecStartPre/ExecStopPost), Healthchecks observe an env
// it does *not*. Up and Down are optional (a local env may need neither);
// Healthchecks may be empty.
type Environment struct {
	// Name is a unique, non-blank identifier a Level references via Environment.
	Name string `yaml:"name"`
	// Up optionally brings the environment up before a rung runs. Like a Level's Run,
	// it is a shell command *string* executed via `sh -c` (not a path draiver points
	// at); inline the command, or `sh -c` a script file yourself. A non-zero exit is
	// an infrastructure fault (the harness, not the agent's code) → escalate.
	Up string `yaml:"up"`
	// Down optionally tears the environment down after a rung. Like Up and a Level's
	// Run, it is a shell command string executed via `sh -c`. It runs unconditionally
	// (pass or fail) and best-effort: a failure never changes a rung's verdict.
	Down string `yaml:"down"`
	// Healthchecks are the readiness probes run after Up and before the rung's Run;
	// a red probe blocks the rung (dependency not ready), it is not a code fault.
	Healthchecks []Healthcheck `yaml:"healthchecks"`
}

// Pyramid is a repo's parsed, validated test-pyramid declaration. Levels is an
// ordered list, bottom-to-top: the list order IS the pyramid, so the last entry is
// the target (top) rung. Environments is an unordered, by-name set of the contexts
// levels can reference; it is a sibling of Levels, not nested under one, precisely
// so one staging/QA context defined once can be shared by several rungs.
type Pyramid struct {
	Levels       []Level       `yaml:"levels"`
	Environments []Environment `yaml:"environments"`
}

// EnvironmentFor resolves the Environment a level targets. ok is false when the
// level binds no environment (the ambient context) — the common case. For a
// Pyramid returned by Load/Parse, a level whose Environment is non-blank always
// resolves (validate guarantees it), so ok is false there only for the unset case;
// a non-blank name that matches nothing still returns (nil, false) defensively, for
// callers holding a hand-built, unvalidated Pyramid.
func (p *Pyramid) EnvironmentFor(lv Level) (*Environment, bool) {
	want := strings.TrimSpace(lv.Environment)
	if want == "" {
		return nil, false
	}
	for i := range p.Environments {
		if strings.TrimSpace(p.Environments[i].Name) == want {
			return &p.Environments[i], true
		}
	}
	return nil, false
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

// validate enforces the invariants: at least one level, every level name unique and
// non-blank, every run non-blank; every environment name unique and non-blank, each
// healthcheck's name and script non-blank; and every level's environment (when set)
// resolves to a declared environment. Blank means empty-after-trim, so a field of
// only whitespace is rejected as the typo it almost certainly is.
func (p *Pyramid) validate() error {
	if len(p.Levels) == 0 {
		return errors.New("levels is empty: a pyramid needs at least one level")
	}

	// Environments first, so a level's environment can be resolved against a known
	// name set below.
	envs := make(map[string]bool, len(p.Environments))
	for i, env := range p.Environments {
		name := strings.TrimSpace(env.Name)
		if name == "" {
			return fmt.Errorf("environment %d: name is blank", i)
		}
		if envs[name] {
			return fmt.Errorf("environment %d: duplicate name %q", i, name)
		}
		envs[name] = true
		for j, hc := range env.Healthchecks {
			if strings.TrimSpace(hc.Name) == "" {
				return fmt.Errorf("environment %q healthcheck %d: name is blank", name, j)
			}
			if strings.TrimSpace(hc.Script) == "" {
				return fmt.Errorf("environment %q healthcheck %q: script is blank", name, strings.TrimSpace(hc.Name))
			}
		}
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
		if env := strings.TrimSpace(lv.Environment); env != "" && !envs[env] {
			return fmt.Errorf("level %q: environment %q is not declared (environments: %s)", name, env, envNames(p))
		}
	}
	return nil
}

// envNames lists the declared environment names, for an error that names what the
// level could have referenced. It returns "none" when no environments are declared.
func envNames(p *Pyramid) string {
	if len(p.Environments) == 0 {
		return "none"
	}
	names := make([]string, len(p.Environments))
	for i, env := range p.Environments {
		names[i] = env.Name
	}
	return strings.Join(names, ", ")
}
