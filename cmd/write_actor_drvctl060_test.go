package cmd

import (
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// TestWriteActorFor pins the drvctl-060 refusal rule's pure core: only a human
// actor that did not come from an explicit --actor, inside a supervised session,
// is refused.
func TestWriteActorFor(t *testing.T) {
	session := map[string]string{
		"DRAIVER_SESSION_TICKET":  "T-1",
		"DRAIVER_SESSION_ATTEMPT": "0001",
		"DRAIVER_SESSION_SEQ":     "3",
	}
	inSession := func(k string) string { return session[k] }
	outside := func(string) string { return "" }

	cases := []struct {
		name     string
		flag     string
		getenv   func(string) string
		resolved string
		wantErr  bool
	}{
		{"agent in session", "", inSession, "agent:claude-code", false},
		{"human outside session", "", outside, "human:david", false},
		{"explicit human flag in session", "human:david", inSession, "human:david", false},
		{"implicit human in session", "", inSession, "human:david", true},
	}
	for _, c := range cases {
		got, err := writeActorFor(c.flag, c.getenv, c.resolved)
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: want refusal, got actor %q", c.name, got)
				continue
			}
			if !strings.Contains(err.Error(), "--actor") || !strings.Contains(err.Error(), "T-1/0001") {
				t.Errorf("%s: refusal should name the session and the --actor escape hatch: %v", c.name, err)
			}
			continue
		}
		if err != nil || got != c.resolved {
			t.Errorf("%s: got %q, %v; want %q", c.name, got, err, c.resolved)
		}
	}
}

// TestLogRefusesHumanFallbackInSession: a supervised session whose agent unset
// DRAIVER_ACTOR would have written as human:$USER; the CLI now refuses, and with
// the daemon-injected DRAIVER_ACTOR the same write lands as the agent.
func TestLogRefusesHumanFallbackInSession(t *testing.T) {
	dir := newTicket(t)
	setSessionEnv(t, "PROJ-1", "0001", 1)
	t.Setenv("USER", "david")

	t.Setenv("DRAIVER_ACTOR", "")
	_, code, err := runE(t, "--data", dir, "log", "PROJ-1", "--type", "note", "forgot the export")
	if code == 0 || err == nil || !strings.Contains(err.Error(), "human:david") {
		t.Fatalf("want a refusal naming human:david, got code %d err %v", code, err)
	}

	t.Setenv("DRAIVER_ACTOR", "agent:claude-code")
	if _, code := run(t, "--data", dir, "log", "PROJ-1", "--type", "note", "injected identity"); code != 0 {
		t.Fatalf("log with injected actor exited %d", code)
	}
	last, _, err := ticketlog.Last(store.Root{Dir: dir}, "PROJ-1", "0001")
	if err != nil || last.Actor != "agent:claude-code" || last.Body != "injected identity" {
		t.Fatalf("last event = %+v, %v; want the agent-attributed note", last, err)
	}

	// The explicit escape hatch still works.
	if _, code := run(t, "--data", dir, "--actor", "human:david", "log", "PROJ-1", "--type", "note", "really me"); code != 0 {
		t.Fatalf("explicit --actor human exited %d", code)
	}
}
