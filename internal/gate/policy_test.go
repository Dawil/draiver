package gate_test

import (
	"testing"

	"github.com/Dawil/draiver/internal/gate"
)

func TestPolicyDecideDefaults(t *testing.T) {
	// The zero policy escalates everything — nothing auto-approves.
	var zero gate.Policy
	if got := zero.Decide("Read"); got != gate.Escalate {
		t.Fatalf("zero policy Decide(Read) = %q, want escalate", got)
	}

	p := gate.Policy{Default: gate.Allow, Tools: map[string]gate.Rule{"Bash": gate.Escalate}}
	if got := p.Decide("Bash"); got != gate.Escalate {
		t.Fatalf("explicit rule ignored: Decide(Bash) = %q, want escalate", got)
	}
	if got := p.Decide("Whatever"); got != gate.Allow {
		t.Fatalf("default ignored: Decide(Whatever) = %q, want allow", got)
	}
}

func TestReadOnlyAllowsReadsEscalatesRest(t *testing.T) {
	p := gate.ReadOnly()
	for _, tool := range []string{"Read", "Glob", "Grep", "LS", "NotebookRead", "TodoWrite"} {
		if got := p.Decide(tool); got != gate.Allow {
			t.Errorf("ReadOnly Decide(%s) = %q, want allow", tool, got)
		}
	}
	for _, tool := range []string{"Bash", "Edit", "Write", "WebFetch", "NotebookEdit", "SomethingNew"} {
		if got := p.Decide(tool); got != gate.Escalate {
			t.Errorf("ReadOnly Decide(%s) = %q, want escalate", tool, got)
		}
	}
}

func TestLayerPrecedence(t *testing.T) {
	global := gate.ReadOnly()                                              // Bash escalates
	project := gate.Policy{Tools: map[string]gate.Rule{"Bash": gate.Allow}} // project trusts Bash
	ticket := gate.Policy{Tools: map[string]gate.Rule{"Read": gate.Escalate}} // this ticket gates Read

	p := gate.Layer(global, project, ticket)

	if got := p.Decide("Bash"); got != gate.Allow {
		t.Errorf("project override lost: Decide(Bash) = %q, want allow", got)
	}
	if got := p.Decide("Read"); got != gate.Escalate {
		t.Errorf("ticket override lost: Decide(Read) = %q, want escalate", got)
	}
	// A tool only the base named survives.
	if got := p.Decide("Grep"); got != gate.Allow {
		t.Errorf("base rule dropped: Decide(Grep) = %q, want allow", got)
	}
	// The base Default carries through when no later layer sets one.
	if got := p.Decide("Unlisted"); got != gate.Escalate {
		t.Errorf("base default dropped: Decide(Unlisted) = %q, want escalate", got)
	}
}

func TestLayerDefaultOverride(t *testing.T) {
	// A later layer setting Default replaces an earlier one.
	p := gate.Layer(
		gate.Policy{Default: gate.Escalate},
		gate.Policy{Default: gate.Allow},
	)
	if got := p.Decide("Anything"); got != gate.Allow {
		t.Fatalf("later Default did not win: Decide = %q, want allow", got)
	}
}

// Layer must not alias the input maps: mutating a source after layering, or
// mutating the result, must not bleed across.
func TestLayerCopiesMaps(t *testing.T) {
	src := gate.Policy{Tools: map[string]gate.Rule{"Bash": gate.Allow}}
	p := gate.Layer(src)
	src.Tools["Bash"] = gate.Escalate
	if got := p.Decide("Bash"); got != gate.Allow {
		t.Fatalf("Layer aliased the source map: Decide(Bash) = %q, want allow", got)
	}
}
