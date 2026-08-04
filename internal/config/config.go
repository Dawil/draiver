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
}

// Default returns the built-in configuration used when no file is present and as
// the fill-in for any field a file omits.
func Default() Config {
	return Config{
		ContextWindow: DefaultContextWindow,
		ContextLimit:  DefaultContextLimit,
	}
}

// file mirrors Config but with pointer fields, so Load can tell an omitted key
// (nil → take the default) from a key explicitly set to 0 (→ used verbatim, e.g.
// context_limit: 0 to disable the auto-stop). A plain-int decode cannot make that
// distinction, which is what makes "0 disables" expressible in the file at all.
type file struct {
	ContextWindow      *int              `json:"context_window"`
	ContextLimit       *int              `json:"context_limit"`
	PermissionsDefault *string           `json:"permissions_default"`
	Permissions        map[string]string `json:"permissions"`
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
	return c, nil
}
