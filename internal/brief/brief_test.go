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

	// A ticket with no `wants:` edges gets the worker brief unchanged: no manager
	// injection leaks in (drvctl-053 acceptance #2).
	for _, gone := range []string{"## Your team", "Coordinator protocol", "You were woken"} {
		if strings.Contains(out, gone) {
			t.Errorf("worker brief must not carry manager injection %q\n---\n%s", gone, out)
		}
	}
}

// A `wants:` ticket is told it is a manager (drvctl-053): the brief injects the
// `## Your team` roster (each child's control-state + open escalations via the
// project.Control fold), the coordinator protocol, and — as the brief's freshest
// tail — the spine-authored `woken-by` activation event (drvctl-051) as the wake
// reason. The `wants:` roster is no longer stripped out of the brief.
func TestManagerBriefInjectsTeamProtocolAndWakeReason(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}

	// Two children: C1 has an open escalation (Needs me), C2 reached Review. A third
	// wanted child, C3, has no attempt yet.
	const c1, c2, c3 = "PROJ-C1", "PROJ-C2", "PROJ-C3"
	for _, c := range []string{c1, c2, c3} {
		if err := root.EnsureAttemptDirs(c, "0001"); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(root.SpecPath(c), []byte("---\nid: "+c+"\ntitle: Child "+c+"\n---\n\n# "+c), 0o644)
	}
	// C3 has a spec but no attempt — remove the attempt dir we just made.
	os.RemoveAll(root.AttemptsDir(c3))

	mustAppend(t, root, c1, "0001", event.Event{Type: "created", Actor: "a", Body: "start"})
	mustAppend(t, root, c1, "0001", event.Event{Type: "escalation", Actor: "a", Body: "which endpoint should the worker call?"})
	mustAppend(t, root, c2, "0001", event.Event{Type: "created", Actor: "a", Body: "start"})
	mustAppend(t, root, c2, "0001", event.Event{Type: "review", Actor: "a", Body: "claim: done, see PR"})

	// The manager P wants: [C1, C2, C3].
	const p, att = "PROJ-P", "0001"
	if err := root.EnsureAttemptDirs(p, att); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(root.SpecPath(p), []byte("---\nid: PROJ-P\ntitle: Integrate the parts\nwants:\n  - "+c1+"\n  - "+c2+"\n  - "+c3+"\n---\n\n# Integrate\n\nMake the pieces fit."), 0o644)
	mustAppend(t, root, p, att, event.Event{Type: "created", Actor: "a", Body: "manager start"})
	mustAppend(t, root, p, att, event.Event{Type: "enable", Actor: "a", Body: "supervised"})
	// The spine's activation event on the manager's own log — its wake reason.
	mustAppend(t, root, p, att, event.Event{
		Type: "woken-by", Actor: "reconciler", WokenChild: c1, WokenSeq: 2,
		Body: "Woken by `" + c1 + "` #2 — a child `escalation` lifecycle doorbell.",
	})

	out, err := Build(root, p, att)
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"## Your team",
		"You are a **manager**",
		"`PROJ-C1`/0001 — **Needs me**", // child control-state via the Control fold
		"#2: which endpoint should the worker call?", // the child's open escalation
		"`PROJ-C2`/0001 — **Review**",
		"`PROJ-C3` — no attempt yet", // wanted but unminted
		"## Coordinator protocol",
		"charter",
		"draiver park",
		"You were woken for this wake:",                               // the tail label
		"Woken by `PROJ-C1` #2 — a child `escalation` lifecycle doorbell", // the reconciler body
	} {
		if !strings.Contains(out, want) {
			t.Errorf("manager brief missing %q\n---\n%s", want, out)
		}
	}

	// The wake reason is the tail — the freshest line, after the recall hint.
	if i, j := strings.Index(out, "draiver recall <seq>"), strings.Index(out, "You were woken"); i < 0 || j < 0 || j < i {
		t.Errorf("wake reason must be the brief's tail (after the recall hint)\n---\n%s", out)
	}
}

func mustAppend(t *testing.T, root store.Root, ticket, att string, e event.Event) {
	t.Helper()
	if _, err := ticketlog.Append(root, ticket, att, e); err != nil {
		t.Fatal(err)
	}
}
