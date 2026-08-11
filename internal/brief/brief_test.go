package brief

import (
	"os"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

func TestBuildIncludesSpecLogAndEscalationPairing(t *testing.T) {
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
	}{
		{"created", "start", nil},
		{"decision", "chose cobra over urfave", nil},
		{"escalation", "which OAuth provider?", nil},
		{"resolution", "use Auth0", []int{3}},
		{"escalation", "prod secret location?", nil},
	} {
		if _, err := ticketlog.Append(root, id, att, event.Event{Type: s.typ, Actor: "a", Refs: s.refs, Body: s.body}); err != nil {
			t.Fatal(err)
		}
	}

	out, err := Build(root, id, att)
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"BRIEF PROJ-1 / attempt 0001 — Wire auth",
		"State: Needs me",
		"Use OAuth.",                  // spec body, frontmatter stripped
		"chose cobra over urfave",     // a decision event
		"→ RESOLVED by #4",            // escalation #3 paired with resolution
		"use Auth0",                   // resolution body inline
		"→ UNRESOLVED",                // escalation #5 has no resolution
		"Open escalations (need a human)",
		"#5: prod secret location?",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("brief missing %q\n---\n%s", want, out)
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
