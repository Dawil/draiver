package agentmsg

import (
	"reflect"
	"testing"
)

// TestIdentityEnv pins the supervised session's identity (drvctl-060): the daemon
// hands every session DRAIVER_ACTOR=agent:<adapter> and DRAIVER_ATTEMPT=<id>.
func TestIdentityEnv(t *testing.T) {
	got := IdentityEnv("claude-code", "0003")
	want := []string{"DRAIVER_ACTOR=agent:claude-code", "DRAIVER_ATTEMPT=0003"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("IdentityEnv = %v, want %v", got, want)
	}
}
