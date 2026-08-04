package protocol_test

import (
	"testing"

	"github.com/Dawil/draiver/internal/protocol"
)

func TestEditsPolicyRequires(t *testing.T) {
	p := protocol.Edits()
	for _, tc := range []struct {
		tool string
		want bool
	}{
		{"Edit", true},
		{"Write", true},
		{"NotebookEdit", true},
		{"Read", false},
		{"Grep", false},
		{"Bash", false}, // the agent logs through Bash; gating it would deadlock
		{"SomethingNew", false},
	} {
		if got := p.Requires(tc.tool); got != tc.want {
			t.Errorf("Edits().Requires(%q) = %v, want %v", tc.tool, got, tc.want)
		}
	}
}

func TestZeroPolicyRequiresNothing(t *testing.T) {
	var p protocol.Policy
	if p.Requires("Edit") {
		t.Error("the zero Policy must withhold nothing (fail-open)")
	}
}

func TestPolicyDefaultApplies(t *testing.T) {
	p := protocol.Policy{Default: protocol.Justified, Tools: map[string]protocol.Requirement{"Read": protocol.Free}}
	if !p.Requires("AnyTool") {
		t.Error("Default Justified should apply to an unlisted tool")
	}
	if p.Requires("Read") {
		t.Error("an explicit Free rule should override a Justified default")
	}
}

func TestLayerPrecedence(t *testing.T) {
	global := protocol.Edits() // Edit/Write/NotebookEdit Justified, default Free
	project := protocol.Policy{Tools: map[string]protocol.Requirement{"Bash": protocol.Justified}}
	ticket := protocol.Policy{Tools: map[string]protocol.Requirement{"Edit": protocol.Free}}

	merged := protocol.Layer(global, project, ticket)

	if !merged.Requires("Write") {
		t.Error("global Justified should survive layering")
	}
	if !merged.Requires("Bash") {
		t.Error("project layer should add Bash as Justified")
	}
	if merged.Requires("Edit") {
		t.Error("ticket layer should relax Edit back to Free")
	}
}

func TestLayerDefaultReplaced(t *testing.T) {
	base := protocol.Policy{Default: protocol.Free}
	strict := protocol.Policy{Default: protocol.Justified}
	if !protocol.Layer(base, strict).Requires("X") {
		t.Error("a later layer's Default should replace an earlier one")
	}
	// An empty later Default must not wipe an earlier one.
	if protocol.Layer(strict, protocol.Policy{}).Requires("X") != true {
		t.Error("an unset later Default should preserve the earlier Default")
	}
}
