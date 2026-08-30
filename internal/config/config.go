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
}

// Default returns the built-in configuration used when no file is present and as
// the fill-in for any field a file omits.
func Default() Config {
	return Config{
		ContextWindow:      DefaultContextWindow,
		ContextLimit:       DefaultContextLimit,
		DefaultSupervision: DefaultSupervision,
	}
}

// file mirrors Config but with pointer fields, so Load can tell an omitted key
// (nil → take the default) from a key explicitly set to 0 (→ used verbatim, e.g.
// context_limit: 0 to disable the auto-stop). A plain-int decode cannot make that
// distinction, which is what makes "0 disables" expressible in the file at all.
type file struct {
	ContextWindow                      *int              `json:"context_window"`
	ContextLimit                       *int              `json:"context_limit"`
	PermissionsDefault                 *string           `json:"permissions_default"`
	Permissions                        map[string]string `json:"permissions"`
	ReviewLinkHosts                    []string          `json:"review_link_hosts"`
	PrimaryRemote                      *string           `json:"primary_remote"`
	ExcludeDynamicSystemPromptSections *bool             `json:"exclude_dynamic_system_prompt_sections"`
	AppendSystemPromptFile             *string           `json:"append_system_prompt_file"`
	DefaultSupervision                 *string           `json:"default_supervision"`
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
	return c, nil
}
