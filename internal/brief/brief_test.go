package brief

import (
	"os"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// The brief is now a progressive-reveal fold (drv-026): it indexes only live-fact
// headlines and lists the open (live) escalations, and does NOT replay full bodies,
// resolved escalations, or superseded history — those are pulled via `recall`.
func TestBuildIndexesLiveFactsAndOpenEscalations(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	id := "PROJ-1"
	const att = "0001"
	if err := root.EnsureAttemptDirs(id, att); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(root.SpecPath(id), []byte("---\nid: PROJ-1\ntitle: Wire auth\nassignee: dave\n---\n\n# Wire auth\n\nUse OAuth."), 0o644)

	for _, s := range []struct {
		typ, body string
		refs      []int
		sup       []int
	}{
		{"created", "start", nil, nil},
		{"decision", "chose cobra over urfave", nil, nil},                                 // #2
		{"escalation", "which OAuth provider?", nil, nil},                                 // #3
		{"resolution", "use Auth0", []int{3}, nil},                                        // #4 — retires #3
		{"escalation", "prod secret location?", nil, nil},                                 // #5 — open
		{"gotcha", "test keys only work in test mode", nil, nil},                          // #6
		{"gotcha", "actually live keys 401 until the account is verified", nil, []int{6}}, // #7 — supersedes #6
	} {
		if _, err := ticketlog.Append(root, id, att, event.Event{Type: s.typ, Actor: "a", Refs: s.refs, Supersedes: s.sup, Body: s.body}); err != nil {
			t.Fatal(err)
		}
	}

	out, err := Build(root, id, att)
	if err != nil {
		t.Fatal(err)
	}

	// Present: header, spec, the live-fact index (headlines), and the open escalation.
	for _, want := range []string{
		"BRIEF PROJ-1 / attempt 0001 — Wire auth",
		"State: Needs me",
		"Use OAuth.",               // spec body, frontmatter stripped
		"## Current understanding", // the index section
		"#2 decision — chose cobra over urfave",
		"#5 escalation — prod secret location?", // open escalation is a live fact
		"#7 gotcha — actually live keys 401 until the account is verified",
		"Open escalations (need a human)",
		"#5: prod secret location?",
		"draiver recall", // the pull-on-demand hint
	} {
		if !strings.Contains(out, want) {
			t.Errorf("brief missing %q\n---\n%s", want, out)
		}
	}

	// Absent: resolved escalation #3 and the superseded gotcha #6 — both retired, so
	// off the index and never replayed. (The resolution #4 is itself a live fact, so
	// its "use Auth0" headline legitimately appears above.)
	for _, gone := range []string{
		"which OAuth provider?",            // #3 is retired by the resolution bridge
		"test keys only work in test mode", // #6 superseded by #7
		"#3 escalation",                    // retired facts are off the index
		"#6 gotcha",
	} {
		if strings.Contains(out, gone) {
			t.Errorf("brief should not replay retired/superseded content %q\n---\n%s", gone, out)
		}
	}

	// Frontmatter identity must not leak into the spec section.
	if strings.Contains(out, "assignee: dave") {
		t.Errorf("spec frontmatter leaked into brief:\n%s", out)
	}
}

// The brief no longer carries the invariant how-to-work protocol (drvctl-034):
// it moved above the wall into the system-prompt append (internal/handbook), so
// the brief holds only the per-ticket spec + log. This guards that the slim held —
// the protocol nudge is not re-injected per cold-start — while the spec and log
// still render.
func TestBuildOmitsProtocolCarriesSpecAndLog(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	const id, att = "PROJ-2", "0001"
	if err := root.EnsureAttemptDirs(id, att); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(root.SpecPath(id), []byte("---\nid: PROJ-2\ntitle: Thing\n---\n\n# Thing\n\nDo the spec."), 0o644)
	if _, err := ticketlog.Append(root, id, att, event.Event{Type: "created", Actor: "a", Body: "genesis event"}); err != nil {
		t.Fatal(err)
	}

	out, err := Build(root, id, att)
	if err != nil {
		t.Fatal(err)
	}

	// The protocol nudge (Markdown-when-logging) must be gone from the brief.
	for _, gone := range []string{
		"stay skimmable on the board",
		"backtick file paths",
	} {
		if strings.Contains(out, gone) {
			t.Errorf("brief still carries the invariant protocol %q — it should live in the append now\n---\n%s", gone, out)
		}
	}
	// The per-ticket half is still there: the spec body and the log event.
	for _, want := range []string{"Do the spec.", "genesis event"} {
		if !strings.Contains(out, want) {
			t.Errorf("brief missing per-ticket content %q\n---\n%s", want, out)
		}
	}
}
