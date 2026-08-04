package cmd

import (
	"testing"

	"github.com/Dawil/draiver/internal/config"
	"github.com/Dawil/draiver/internal/gate"
)

func TestResolvePermPolicy_DefaultIsAutoMode(t *testing.T) {
	// No config, no flag → allow-all: Claude in auto mode, the local default.
	p, err := resolvePermPolicy(config.Default(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"Bash", "Edit", "Write", "WebFetch", "Anything"} {
		if got := p.Decide(tool); got != gate.Allow {
			t.Errorf("auto-mode default: Decide(%s) = %q, want allow", tool, got)
		}
	}
}

func TestResolvePermPolicy_ConfigEscalatesOneTool(t *testing.T) {
	cfg := config.Default()
	cfg.Permissions = map[string]string{"WebFetch": "escalate"}

	p, err := resolvePermPolicy(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Decide("WebFetch"); got != gate.Escalate {
		t.Errorf("Decide(WebFetch) = %q, want escalate (config)", got)
	}
	if got := p.Decide("Bash"); got != gate.Allow {
		t.Errorf("Decide(Bash) = %q, want allow (untouched by config)", got)
	}
}

func TestResolvePermPolicy_ConfigLockdownDefault(t *testing.T) {
	// The opposite posture: escalate everything, allow-list a few.
	cfg := config.Default()
	cfg.PermissionsDefault = "escalate"
	cfg.Permissions = map[string]string{"Read": "allow"}

	p, err := resolvePermPolicy(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Decide("Read"); got != gate.Allow {
		t.Errorf("Decide(Read) = %q, want allow", got)
	}
	if got := p.Decide("Bash"); got != gate.Escalate {
		t.Errorf("Decide(Bash) = %q, want escalate (lockdown default)", got)
	}
}

func TestResolvePermPolicy_FlagOverridesConfig(t *testing.T) {
	cfg := config.Default()
	cfg.Permissions = map[string]string{"Bash": "allow"}

	p, err := resolvePermPolicy(cfg, map[string]string{"Bash": "escalate"})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Decide("Bash"); got != gate.Escalate {
		t.Errorf("Decide(Bash) = %q, want escalate (--permission wins over config)", got)
	}
}

func TestResolvePermPolicy_InvalidRuleErrors(t *testing.T) {
	cfg := config.Default()
	cfg.Permissions = map[string]string{"Bash": "maybe"}
	if _, err := resolvePermPolicy(cfg, nil); err == nil {
		t.Error("an invalid config rule should error, not silently default")
	}
	if _, err := resolvePermPolicy(config.Default(), map[string]string{"Bash": "maybe"}); err == nil {
		t.Error("an invalid --permission rule should error")
	}
}
