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
