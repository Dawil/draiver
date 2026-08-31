package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/session"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// writeSpecWith writes a ticket's spec.md with an explicit frontmatter body, so a
// test can author `wants:`/`after:` edges the seedBoard helpers don't cover.
func writeSpecWith(t *testing.T, root store.Root, id, frontmatter, title string) {
	t.Helper()
	body := "---\nid: " + id + "\ntitle: " + title + "\n" + frontmatter + "---\n\n# " + title
	if err := os.WriteFile(root.SpecPath(id), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// postSupervision posts the dial the way the panel does — a single `mode` field
// with a same-origin Origin so the guard passes.
func postSupervision(t *testing.T, h http.Handler, id, att, mode string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"mode": {mode}}
	req := httptest.NewRequest(http.MethodPost, "/ticket/"+id+"/"+att+"/supervision", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://"+req.Host)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// seedCapability builds a Capability CAP that wants four children in distinct
// states, plus a predecessor holding a Pending child's gate shut:
//
//   - CH-RUN   — Running with a live agent.
//   - CH-PEND  — enabled + Running + dead session ⇒ desired, no live agent ⇒
//     Pending; its spec is admitted after BLOCK (only Running), so its gate reason
//     is "waiting on BLOCK".
//   - CH-DONE  — Done.
//   - CH-NONE  — wanted but has no attempt yet ⇒ the "no attempt yet" row.
//   - BLOCK    — Running; CH-PEND's unmet `after:` predecessor.
//
// It returns a server whose liveness stub treats only os.Getpid() as alive.
func seedCapability(t *testing.T, opts ...Option) (*Server, store.Root) {
	t.Helper()
	root := store.Root{Dir: t.TempDir()}

	// The coordinator: wants the four children (CH-NONE deliberately has no attempt).
	if err := root.EnsureAttemptDirs("CAP", "0001"); err != nil {
		t.Fatal(err)
	}
	writeSpecWith(t, root, "CAP",
		"wants:\n  - CH-RUN\n  - CH-PEND\n  - CH-DONE\n  - CH-NONE\n", "Capability")
	ticketlog.Append(root, "CAP", "0001", event.Event{Type: "created", Actor: "a", Body: "start"})

	// CH-RUN — a live agent is working: Running.
	mkAttempt(t, root, "CH-RUN", "Child running", "0001")
	writeSession(t, root, "CH-RUN", "0001", session.Identity{SessionID: "s-run", PID: os.Getpid()})

	// BLOCK — the Running predecessor holding CH-PEND's forward gate shut.
	mkAttempt(t, root, "BLOCK", "Blocker", "0001")

	// CH-PEND — enabled + Running + dead session ⇒ Pending; after: BLOCK (Running) so
	// the gate is shut and the reason names BLOCK.
	if err := root.EnsureAttemptDirs("CH-PEND", "0001"); err != nil {
		t.Fatal(err)
	}
	writeSpecWith(t, root, "CH-PEND", "after:\n  - BLOCK\n", "Child pending")
	ticketlog.Append(root, "CH-PEND", "0001", event.Event{Type: "created", Actor: "a", Body: "start"})
	ticketlog.Append(root, "CH-PEND", "0001", event.Event{Type: "enable", Actor: "h", Body: "on"})
	writeSession(t, root, "CH-PEND", "0001", session.Identity{SessionID: "s-pend", PID: deadPID})

	// CH-DONE — closed.
	mkAttempt(t, root, "CH-DONE", "Child done", "0001")
	ticketlog.Append(root, "CH-DONE", "0001", event.Event{Type: "done", Actor: "h", Body: "closed"})

	s, err := New(root, opts...)
	if err != nil {
		t.Fatal(err)
	}
	s.alive = func(pid int) bool { return pid == os.Getpid() }
	return s, root
}

// TestCapabilityPanelListsChildrenStatesGates pins AC#1: the Capability page lists
// its `wants:` children with their control states and, for a Pending child, the
// gate reason holding it.
func TestCapabilityPanelListsChildrenStatesGates(t *testing.T) {
	s, _ := seedCapability(t)
	h := s.Handler()

	body := get(t, h, "/ticket/CAP/0001").Body.String()
	for _, want := range []string{
		`data-testid="capability"`,
		`data-testid="capability-children"`,
		// each child row is present
		`data-child="CH-RUN"`,
		`data-child="CH-PEND"`,
		`data-child="CH-DONE"`,
		`data-child="CH-NONE"`,
		// states: CH-RUN Running, CH-PEND Pending, CH-DONE Done
		`state-Running`,
		`state-Pending`,
		`state-Done`,
		// the Pending child's gate reason names its unmet predecessor
		`data-testid="capability-child-gate">waiting on BLOCK<`,
		// the wanted-but-unstarted child shows the placeholder
		`data-testid="capability-child-none"`,
		// children deep-link to their ticket pages
		`href="/ticket/CH-RUN"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("capability panel missing %q\n%s", want, body)
		}
	}
	// A non-Pending child carries no gate reason.
	if strings.Count(body, `data-testid="capability-child-gate"`) != 1 {
		t.Errorf("exactly one child (CH-PEND) should show a gate reason:\n%s", body)
	}
}

// TestCapabilityPanelOnlyForCapability pins that the panel renders on a Capability
// (a ticket carrying `wants:`) and is absent on an ordinary ticket.
func TestCapabilityPanelOnlyForCapability(t *testing.T) {
	s, _ := seedCapability(t)
	h := s.Handler()

	if body := get(t, h, "/ticket/CAP/0001").Body.String(); !strings.Contains(body, `data-testid="capability"`) {
		t.Errorf("CAP is a Capability and must show the panel:\n%s", body)
	}
	// BLOCK carries no wants: — an ordinary ticket, no panel.
	if body := get(t, h, "/ticket/BLOCK/0001").Body.String(); strings.Contains(body, `data-testid="capability"`) {
		t.Errorf("an ordinary ticket must not show the capability panel:\n%s", body)
	}
}

// TestCapabilityDialDefaultsToEffective pins that, with no per-ticket override, the
// dial pre-selects the effective mode (the config default, passthrough floor) and
// notes it is inheriting the default.
func TestCapabilityDialDefaultsToEffective(t *testing.T) {
	s, _ := seedCapability(t)
	h := s.Handler()

	body := get(t, h, "/ticket/CAP/0001").Body.String()
	// passthrough is the floor default and should be the checked option.
	if !strings.Contains(body, `data-testid="capability-mode-passthrough"`) ||
		!strings.Contains(body, `data-testid="capability-mode-pre-digest"`) {
		t.Errorf("both modes should be offered:\n%s", body)
	}
	if !strings.Contains(body, `value="passthrough" checked`) {
		t.Errorf("passthrough (the default) should be pre-checked:\n%s", body)
	}
	if !strings.Contains(body, `data-testid="capability-inherit"`) {
		t.Errorf("with no override the panel should note it inherits the default:\n%s", body)
	}
}

// TestCapabilityDialWithConfigDefault pins that the config default threaded in via
// WithSupervisionDefault is the effective mode shown when no override is set.
func TestCapabilityDialWithConfigDefault(t *testing.T) {
	s, _ := seedCapability(t, WithSupervisionDefault("pre-digest"))
	h := s.Handler()

	body := get(t, h, "/ticket/CAP/0001").Body.String()
	if !strings.Contains(body, `value="pre-digest" checked`) {
		t.Errorf("the config default (pre-digest) should be pre-checked:\n%s", body)
	}
	if !strings.Contains(body, `inheriting default (pre-digest)`) {
		t.Errorf("the inherit note should name the config default:\n%s", body)
	}
}

// TestCapabilitySetSupervisionPersists pins AC#2: changing the supervision mode
// writes the per-ticket override (a `supervision` log event) and is reflected on
// reload — the override wins over the config default and the inherit note is gone.
func TestCapabilitySetSupervisionPersists(t *testing.T) {
	s, root := seedCapability(t)
	h := s.Handler()

	rr := postSupervision(t, h, "CAP", "0001", "pre-digest")
	if rr.Code != http.StatusOK {
		t.Fatalf("set supervision = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	resp := rr.Body.String()
	if !strings.Contains(resp, `value="pre-digest" checked`) {
		t.Errorf("the POST response should show pre-digest selected:\n%s", resp)
	}
	if !strings.Contains(resp, `data-testid="capability-saved"`) {
		t.Errorf("a successful save should confirm with saved ✓:\n%s", resp)
	}

	// The write landed as a durable `supervision` override on the attempt log.
	a, err := project.LoadAttempt(root, "CAP", "0001")
	if err != nil {
		t.Fatal(err)
	}
	if a.Supervision != project.SupervisionPreDigest {
		t.Fatalf("override not persisted: Supervision = %q", a.Supervision)
	}

	// A fresh GET reflects it: pre-digest checked, no inherit note (it is now set).
	body := get(t, h, "/ticket/CAP/0001").Body.String()
	if !strings.Contains(body, `value="pre-digest" checked`) {
		t.Errorf("reload should show the persisted mode selected:\n%s", body)
	}
	if strings.Contains(body, `data-testid="capability-inherit"`) {
		t.Errorf("with an override set, the inherit note should be gone:\n%s", body)
	}

	// Switching back writes a second override (last-wins), reflected in turn.
	if rr := postSupervision(t, h, "CAP", "0001", "passthrough"); rr.Code != http.StatusOK {
		t.Fatalf("switch back = %d", rr.Code)
	}
	if a, _ := project.LoadAttempt(root, "CAP", "0001"); a.Supervision != project.SupervisionPassthrough {
		t.Errorf("last-wins override not applied, got %q", a.Supervision)
	}
}

// TestCapabilitySupervisionGuards covers the write route's guards: an unknown
// attempt 404s, a bad mode 400s, a non-Capability 400s, and a cross-origin POST is
// blocked 403 and writes nothing.
func TestCapabilitySupervisionGuards(t *testing.T) {
	s, root := seedCapability(t)
	h := s.Handler()

	if rr := postSupervision(t, h, "CAP", "9999", "pre-digest"); rr.Code != http.StatusNotFound {
		t.Errorf("unknown attempt = %d, want 404", rr.Code)
	}
	if rr := postSupervision(t, h, "CAP", "0001", "auto-execute"); rr.Code != http.StatusBadRequest {
		t.Errorf("deferred/unknown mode = %d, want 400", rr.Code)
	}
	// BLOCK is not a Capability (no wants:) — the dial never renders for it, so a POST
	// is a bad request rather than a silent write.
	if rr := postSupervision(t, h, "BLOCK", "0001", "pre-digest"); rr.Code != http.StatusBadRequest {
		t.Errorf("supervision on a non-Capability = %d, want 400", rr.Code)
	}

	// Cross-origin POST is blocked and nothing is written.
	form := url.Values{"mode": {"pre-digest"}}
	req := httptest.NewRequest(http.MethodPost, "/ticket/CAP/0001/supervision", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("cross-origin supervision POST = %d, want 403", rr.Code)
	}
	if a, _ := project.LoadAttempt(root, "CAP", "0001"); a.Supervision != project.SupervisionDefault {
		t.Errorf("a blocked/guarded POST must write nothing, got %q", a.Supervision)
	}
}
