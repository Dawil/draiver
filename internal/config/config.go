// Package config loads draiverctld's operator configuration — the tunables the
// supervisor reads but the ticket data does not own. It is deliberately tiny and
// forgiving: a missing file is not an error (defaults apply), so an operator only
// writes a config file to override a default.
//
// The config lives OUTSIDE the ticket data root, next to it by convention
// (~/.draiver/config.json), because it configures the daemon, not any one
// ticket's durable log. Resolution mirrors store.Resolve: an explicit path, then
// $DRAIVER_CONFIG, then ~/.draiver/config.json.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Default context tunables. ContextWindow is a 200K-token model window (the
// gauge capacity `ctl status` already assumed); ContextLimit is the auto-stop
// threshold — a session whose live context fill crosses it is halted before it
// can run away, the drvctl-012 backstop.
const (
	DefaultContextWindow = 200_000
	DefaultContextLimit  = 150_000

	// DefaultSupervision is the built-in coordinator supervision mode (drvctl-042)
	// when config sets none: "passthrough", the floor — a sub-ticket escalation
	// routes straight to Needs-me and no coordinator wakes. Opting a fleet into
	// pre-digest is a deliberate config choice, mirroring how enable is opt-in.
	DefaultSupervision = "passthrough"

	// DefaultReviewEnvMaxAgeMinutes is the builtin idle/max-age for the review-env
	// reaper (drv-020) when the operator sets none: two hours of no interaction before
	// a launched-but-forgotten review env is torn down.
	DefaultReviewEnvMaxAgeMinutes = 120
)

// Config is the operator's resolved supervisor tunables. Every field carries a
// concrete value once Load has applied defaults.
type Config struct {
	// ContextWindow is the model's context capacity in tokens — the denominator
	// of the context-% gauge. 0 leaves the gauge as an absolute token count.
	ContextWindow int `json:"context_window"`

	// ContextLimit is the auto-stop threshold in tokens: a session whose context
	// fill crosses it is halted and the stop recorded as an escalation. 0 disables
	// the auto-stop.
	ContextLimit int `json:"context_limit"`

	// PermissionsDefault is the permission-gate default rule ("allow" | "escalate")
	// for any tool not named in Permissions. Empty leaves the gate's base default
	// (auto-mode allow-all) in place. Validated into a gate.Policy by the caller.
	PermissionsDefault string `json:"permissions_default"`

	// Permissions is the per-tool permission-gate override: tool name → rule
	// ("allow" | "escalate"). It layers over the allow-all base, so the common use
	// is escalating a few sensitive tools (e.g. {"WebFetch": "escalate"}) while the
	// rest stay auto-approved. Validated into a gate.Policy by the caller.
	Permissions map[string]string `json:"permissions"`

	// ReviewLinkHosts is an optional allowlist of hosts a review link (an event's
	// Links) may point at — the genuinely per-deployment policy, e.g. "review links
	// may only point at bitbucket.mycorp.com". Empty (the default) admits any
	// http/https host, so it is opt-in and off by default. It sits alongside
	// Permissions and layers on top of the hardcoded {http, https} scheme floor,
	// which it cannot loosen (see event.ValidateLink).
	ReviewLinkHosts []string `json:"review_link_hosts"`

	// PrimaryRemote names the git remote an agent should push to (and derive the
	// review URL from) when a working tree has more than one remote configured —
	// e.g. "forgejo" when origin=GitHub upstream and forgejo=the review forge. It
	// disambiguates only the multi-remote case: with exactly one remote the agent
	// uses it regardless, and with several remotes and no PrimaryRemote set the
	// agent must not guess (it surfaces the ambiguity rather than pushing to the
	// wrong forge). Empty (the default) leaves the choice unconfigured. This is
	// prompt/agent policy — draiver itself does not push; see the onboarding skill.
	PrimaryRemote string `json:"primary_remote"`

	// ExcludeDynamicSystemPromptSections toggles the coding-agent's
	// --exclude-dynamic-system-prompt-sections flag: it moves the per-machine
	// system-prompt sections (cwd, env, memory paths, git status) into the first
	// user message, so every worktree shares a byte-identical tools+prefix and the
	// prompt cache is reused across tickets/worktrees on a repo (docs/prompt-caching.md,
	// the M0 unlock). Default false → a byte-identical no-op vs today. Turning it on
	// requires the pinned agent to support the flag; the daemon fails loud at
	// construction if it does not (paired with the drvctl-033 version pin).
	ExcludeDynamicSystemPromptSections bool `json:"exclude_dynamic_system_prompt_sections"`

	// DefaultSupervision is the global/project default coordinator supervision mode
	// (drvctl-042): "passthrough" (the floor — a sub-ticket escalation goes straight
	// to Needs-me, no coordinator wakes) or "pre-digest" (reverse-`wants:` wakes the
	// coordinator to assess and post one consolidated recommendation). A per-ticket
	// `supervision` log event overrides it, exactly as `enable` overrides a global
	// "disabled by default". Default "passthrough". Kept a plain string here so this
	// package stays dependency-free; it is validated into a project.Supervision where
	// the reconciler is built, failing daemon start loudly on a bad value.
	DefaultSupervision string `json:"default_supervision"`

	// AppendSystemPromptFile is a path to a file whose contents the daemon passes
	// as the coding-agent's --append-system-prompt, carrying draiver's invariant
	// protocol *above* the excluded per-machine wall so it stays part of the shared,
	// cacheable prefix. The file is read once at reconciler construction, so the
	// text is byte-identical across every attempt in a daemon lifetime (the
	// prefix-sharing invariant). Empty (the default) appends nothing.
	AppendSystemPromptFile string `json:"append_system_prompt_file"`

	// BDDArtefactKeep caps how many captured BDD run artefact sets `draiver test`
	// retains per (rung, environment) group — the GC knob keeping the per-attempt
	// store from growing unbounded as runs regenerate side by side (drv-017).
	// Retention is additive: on capture the oldest run dirs beyond the cap are
	// pruned, newest kept. 0 (the default) keeps everything — no GC.
	BDDArtefactKeep int `json:"bdd_artefact_keep"`

	// ReviewEnvMaxAgeMinutes is the idle/max-age reaper knob for review environments
	// (drv-020): the controlplane tears down a launched review env that has gone
	// untouched (no launch/poll interaction) for longer than this, the safety net
	// against a forgotten env leaking a worktree and a bound port. Each webui status
	// poll bumps the env's last-active stamp, so an env a reviewer is actively driving
	// stays up; one abandoned mid-review is reaped. Default 120 (two hours). 0 disables
	// the reaper — teardown then happens only on the explicit button or when the ticket
	// is marked Done.
	ReviewEnvMaxAgeMinutes int `json:"review_env_max_age_minutes"`

	// Repos holds per-repo setting overrides keyed by the repo path — the same
	// string an attempt records as its `repo`/provenance Repo (drv-011). Some knobs
	// are properties of a repo, not a single attempt (which remote is the forge,
	// which branch is the trunk, how high the pyramid must climb); setting them once
	// here lets every attempt on that repo read the same value from the same place.
	// A repo with no entry falls through to the global default, then the builtin —
	// see RepoSettings. Omitted (the default) → nil, read as "no per-repo overrides".
	Repos map[string]RepoSettings `json:"repos"`
}

// RepoSettings is one repo's stored overrides. Each field is a plain string; an
// empty one means "unset — fall through to the global default, then the builtin",
// the resolution RepoSettings applies. It mirrors the shape the config file carries
// under repos.<path> and the fields the attempt page's repo-settings panel edits.
type RepoSettings struct {
	// DefaultRemote is the git remote name/URL git actions use for this repo,
	// superseding the global PrimaryRemote. Empty → fall through to PrimaryRemote.
	DefaultRemote string `json:"default_remote"`
	// DefaultBranch is the repo's trunk: what sync pulls and what a new attempt's
	// base defaults to. Empty → the repo's current branch (a git concern the caller
	// resolves), so there is no global default branch to fall through to.
	DefaultBranch string `json:"default_branch"`
	// DefaultTestRung is the top rung the pyramid must clear before Review for this
	// repo. Empty → the file's own top rung (the builtin). The global
	// default_test_level it would supersede is deferred (docs/test-pyramid.md), so
	// there is no global value to fall through to today.
	DefaultTestRung string `json:"default_test_rung"`
	// ReviewEnvironment names the .test-pyramid.yaml environment the attempt page's
	// review-environment panel launches at Review (drv-020). Empty → no review env is
	// offered for this repo (the common case; it is opt-in). The named environment
	// must be review-ready (up + down + ≥1 healthcheck, pyramid.Environment.ReviewReady)
	// for the panel to appear — a validation the consuming web/controlplane layer runs,
	// not this dependency-free package. There is no global default to fall through to.
	ReviewEnvironment string `json:"review_environment"`
}

// Resolved is a repo's effective settings after global fallback is applied — what a
// consumer actually reads. A field is empty only when neither the per-repo entry nor
// the global default set it, signalling the caller to apply its builtin (the repo's
// current branch, the pyramid file's top rung).
type Resolved struct {
	// Remote is DefaultRemote → PrimaryRemote → "".
	Remote string
	// Branch is DefaultBranch → "" (the repo's current branch is the builtin,
	// resolved git-side by the caller — there is no global default branch).
	Branch string
	// TestRung is DefaultTestRung → "" (the pyramid file's top rung is the builtin;
	// the global default_test_level is deferred, so nothing sits between them yet).
	TestRung string
	// ReviewEnvironment is ReviewEnvironment → "" (there is no global default and no
	// builtin: unset simply means this repo offers no review environment, drv-020).
	ReviewEnvironment string
}

// RepoSettings resolves a repo's effective settings: per-repo entry → global
// default → builtin, each key resolved independently so a per-repo entry that sets
// only one key leaves the others falling through. An empty resolved field hands the
// builtin back to the caller (the repo's current branch, the file's top rung). It is
// total — a repo with no entry is a valid, common state, not an error.
func (c Config) RepoSettings(repoPath string) Resolved {
	rs := c.Repos[repoPath] // zero value when absent — every field empty, all fall through
	r := Resolved{
		Remote:            rs.DefaultRemote,
		Branch:            rs.DefaultBranch,
		TestRung:          rs.DefaultTestRung,
		ReviewEnvironment: rs.ReviewEnvironment,
	}
	if r.Remote == "" {
		r.Remote = c.PrimaryRemote
	}
	return r
}

// RepoSettingsUpdate is a partial edit to one repo's stored settings: a nil field is
// left untouched, a non-nil one is written (including to "" to clear a key). It is
// the per-repo twin of repo.Provenance — the shape both the `config repo` verb and
// the attempt page's repo-settings panel hand to SetRepoSettings.
type RepoSettingsUpdate struct {
	DefaultRemote     *string
	DefaultBranch     *string
	DefaultTestRung   *string
	ReviewEnvironment *string
}

// set reports whether the update touches any field — the "nothing to set" guard the
// caller uses to leave the file untouched, mirroring SetProvenance's changed=false.
func (u RepoSettingsUpdate) set() bool {
	return u.DefaultRemote != nil || u.DefaultBranch != nil || u.DefaultTestRung != nil ||
		u.ReviewEnvironment != nil
}

// Default returns the built-in configuration used when no file is present and as
// the fill-in for any field a file omits.
func Default() Config {
	return Config{
		ContextWindow:          DefaultContextWindow,
		ContextLimit:           DefaultContextLimit,
		DefaultSupervision:     DefaultSupervision,
		ReviewEnvMaxAgeMinutes: DefaultReviewEnvMaxAgeMinutes,
	}
}

// file mirrors Config but with pointer fields, so Load can tell an omitted key
// (nil → take the default) from a key explicitly set to 0 (→ used verbatim, e.g.
// context_limit: 0 to disable the auto-stop). A plain-int decode cannot make that
// distinction, which is what makes "0 disables" expressible in the file at all.
//
// Every tag carries ,omitempty so the mirror also round-trips: SetRepoSettings
// decodes the existing file into it, patches one repo, and re-marshals — and a nil
// pointer / empty map / empty slice is dropped, so a key the operator never set
// stays absent rather than being written back as a zero. (omitempty on a pointer
// drops only nil, so a *bool set to false or a *int set to 0 is still written — the
// explicit-zero semantics Load relies on survive the round-trip.)
type file struct {
	ContextWindow                      *int                 `json:"context_window,omitempty"`
	ContextLimit                       *int                 `json:"context_limit,omitempty"`
	PermissionsDefault                 *string              `json:"permissions_default,omitempty"`
	Permissions                        map[string]string    `json:"permissions,omitempty"`
	ReviewLinkHosts                    []string             `json:"review_link_hosts,omitempty"`
	PrimaryRemote                      *string              `json:"primary_remote,omitempty"`
	ExcludeDynamicSystemPromptSections *bool                `json:"exclude_dynamic_system_prompt_sections,omitempty"`
	AppendSystemPromptFile             *string              `json:"append_system_prompt_file,omitempty"`
	DefaultSupervision                 *string              `json:"default_supervision,omitempty"`
	BDDArtefactKeep                    *int                 `json:"bdd_artefact_keep,omitempty"`
	ReviewEnvMaxAgeMinutes             *int                 `json:"review_env_max_age_minutes,omitempty"`
	Repos                              map[string]*repoFile `json:"repos,omitempty"`
}

// repoFile is the pointer-mirror of RepoSettings: a nil field is an omitted key (not
// written back), so the merging writer leaves keys it did not touch exactly as it
// found them. An empty-string value is a key explicitly cleared to "" — distinct
// from nil, and preserved on round-trip — mirroring the explicit-zero discipline the
// top-level pointer fields carry.
type repoFile struct {
	DefaultRemote     *string `json:"default_remote,omitempty"`
	DefaultBranch     *string `json:"default_branch,omitempty"`
	DefaultTestRung   *string `json:"default_test_rung,omitempty"`
	ReviewEnvironment *string `json:"review_environment,omitempty"`
}

// Path resolves the config file location: an explicit flag value, then
// $DRAIVER_CONFIG, then ~/.draiver/config.json.
func Path(flagVal string) (string, error) {
	p := flagVal
	if p == "" {
		p = os.Getenv("DRAIVER_CONFIG")
	}
	if p == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve config path: %w", err)
		}
		p = filepath.Join(home, ".draiver", "config.json")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("resolve config path: %w", err)
	}
	return abs, nil
}

// Load reads the config at the resolved path. A missing file is not an error — a
// config file is opt-in — so it yields Default(). A field the file omits takes
// its default; a field the file sets (including to 0) is used verbatim, so
// context_limit: 0 disables the auto-stop. A present-but-unparseable file is an
// error, so a typo is surfaced rather than silently ignored.
func Load(flagVal string) (Config, error) {
	path, err := Path(flagVal)
	if err != nil {
		return Config{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Default(), nil
		}
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	c := Default()
	if f.ContextWindow != nil {
		c.ContextWindow = *f.ContextWindow
	}
	if f.ContextLimit != nil {
		c.ContextLimit = *f.ContextLimit
	}
	if f.PermissionsDefault != nil {
		c.PermissionsDefault = *f.PermissionsDefault
	}
	if f.Permissions != nil {
		c.Permissions = f.Permissions
	}
	if f.ReviewLinkHosts != nil {
		c.ReviewLinkHosts = f.ReviewLinkHosts
	}
	if f.PrimaryRemote != nil {
		c.PrimaryRemote = *f.PrimaryRemote
	}
	if f.ExcludeDynamicSystemPromptSections != nil {
		c.ExcludeDynamicSystemPromptSections = *f.ExcludeDynamicSystemPromptSections
	}
	if f.AppendSystemPromptFile != nil {
		c.AppendSystemPromptFile = *f.AppendSystemPromptFile
	}
	if f.DefaultSupervision != nil {
		c.DefaultSupervision = *f.DefaultSupervision
	}
	if f.BDDArtefactKeep != nil {
		c.BDDArtefactKeep = *f.BDDArtefactKeep
	}
	if f.ReviewEnvMaxAgeMinutes != nil {
		c.ReviewEnvMaxAgeMinutes = *f.ReviewEnvMaxAgeMinutes
	}
	if f.Repos != nil {
		c.Repos = make(map[string]RepoSettings, len(f.Repos))
		for path, rf := range f.Repos {
			c.Repos[path] = rf.resolve()
		}
	}
	return c, nil
}

// SetRepoSettings merges a partial update to one repo's settings into the config
// file at the resolved path and writes it atomically, preserving every other key —
// other repos, that repo's unchanged keys, and all the global tunables — untouched.
// It is the config twin of repo.SetProvenance's partial-update discipline: a nil
// field in upd is left as the file has it, a non-nil one is written (including to ""
// to clear a key). changed is false when upd sets nothing, and then nothing is
// written, leaving the "nothing to set" framing to the caller.
//
// A missing file is not an error (it is opt-in, as Load treats it) — the write
// creates it, and ~/.draiver along with it. The merge decodes the existing bytes
// into the pointer-mirror so unknown-to-Default keys and the explicit-zero fields
// survive the round-trip, patches the one repo, then re-marshals. The write is
// atomic (temp file + rename) so a concurrent reader never sees a half-written file.
func SetRepoSettings(flagVal, repoPath string, upd RepoSettingsUpdate) (changed bool, err error) {
	if !upd.set() {
		return false, nil
	}
	path, err := Path(flagVal)
	if err != nil {
		return false, err
	}
	var f file
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &f); err != nil {
			return false, fmt.Errorf("parse config %s: %w", path, err)
		}
	case errors.Is(err, fs.ErrNotExist):
		// No file yet — start from an empty mirror; the write mints it.
	default:
		return false, fmt.Errorf("read config %s: %w", path, err)
	}

	if f.Repos == nil {
		f.Repos = map[string]*repoFile{}
	}
	rf := f.Repos[repoPath]
	if rf == nil {
		rf = &repoFile{}
		f.Repos[repoPath] = rf
	}
	if upd.DefaultRemote != nil {
		rf.DefaultRemote = upd.DefaultRemote
	}
	if upd.DefaultBranch != nil {
		rf.DefaultBranch = upd.DefaultBranch
	}
	if upd.DefaultTestRung != nil {
		rf.DefaultTestRung = upd.DefaultTestRung
	}
	if upd.ReviewEnvironment != nil {
		rf.ReviewEnvironment = upd.ReviewEnvironment
	}

	out, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return false, fmt.Errorf("encode config: %w", err)
	}
	out = append(out, '\n')
	if err := atomicWrite(path, out); err != nil {
		return false, err
	}
	return true, nil
}

// atomicWrite writes data to path via a temp file in the same directory and a
// rename, creating the parent directory if it is missing (the common first-write
// case for ~/.draiver). The temp file shares the target's directory so the rename is
// a same-filesystem atomic swap, not a cross-device copy.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config dir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace config %s: %w", path, err)
	}
	return nil
}

// resolve flattens a repoFile's pointers into a plain RepoSettings, treating an
// omitted key as "" — the same empty-means-fall-through the resolver reads. A nil
// receiver (a repos.<path> entry present but null) yields the zero value.
func (rf *repoFile) resolve() RepoSettings {
	if rf == nil {
		return RepoSettings{}
	}
	var rs RepoSettings
	if rf.DefaultRemote != nil {
		rs.DefaultRemote = *rf.DefaultRemote
	}
	if rf.DefaultBranch != nil {
		rs.DefaultBranch = *rf.DefaultBranch
	}
	if rf.DefaultTestRung != nil {
		rs.DefaultTestRung = *rf.DefaultTestRung
	}
	if rf.ReviewEnvironment != nil {
		rs.ReviewEnvironment = *rf.ReviewEnvironment
	}
	return rs
}
