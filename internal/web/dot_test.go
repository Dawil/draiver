package web

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/session"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// deadPID is a pid the liveness stub never treats as alive.
const deadPID = 99999

// mkAttempt creates an attempt with a spec title and a first `created` event.
func mkAttempt(t *testing.T, root store.Root, id, title, att string) {
	t.Helper()
	if err := root.EnsureAttemptDirs(id, att); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(root.SpecPath(id), []byte("---\nid: "+id+"\ntitle: "+title+"\n---\n\n# "+title), 0o644)
	if _, err := ticketlog.Append(root, id, att, event.Event{Type: "created", Actor: "a", Body: "start"}); err != nil {
		t.Fatal(err)
	}
}

// writeSession writes an attempt's session.json directly (as draiverctld would),
// without going through session.Open.
func writeSession(t *testing.T, root store.Root, id, att string, sess session.Identity) {
	t.Helper()
	if err := root.EnsureSessionDir(id, att); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root.SessionMetaPath(id, att), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// seedDots builds a root exercising every session-dot state and returns a server
// whose liveness probe treats only os.Getpid() as alive (deterministic).
func seedDots(t *testing.T) *Server {
	t.Helper()
	root := store.Root{Dir: t.TempDir()}

	// RUN/0001 — live pid, enabled → running (green).
	mkAttempt(t, root, "RUN", "Running", "0001")
	ticketlog.Append(root, "RUN", "0001", event.Event{Type: "enable", Actor: "h", Body: "on"})
	writeSession(t, root, "RUN", "0001", session.Identity{SessionID: "s-run", PID: os.Getpid()})

	// STOP/0001 — dead pid, enabled → stopped (wattle).
	mkAttempt(t, root, "STOP", "Stopped", "0001")
	ticketlog.Append(root, "STOP", "0001", event.Event{Type: "enable", Actor: "h", Body: "on"})
	writeSession(t, root, "STOP", "0001", session.Identity{SessionID: "s-stop", PID: deadPID})

	// GONE/0001 — dead pid, NOT enabled → disabled (grey).
	mkAttempt(t, root, "GONE", "Disabled", "0001")
	writeSession(t, root, "GONE", "0001", session.Identity{SessionID: "s-gone", PID: deadPID})

	// FRESH/0001 — no session.json → no dot.
	mkAttempt(t, root, "FRESH", "Fresh", "0001")

	// EMPTY/0001 — session.json present but empty session_id → no dot.
	mkAttempt(t, root, "EMPTY", "Empty", "0001")
	writeSession(t, root, "EMPTY", "0001", session.Identity{SessionID: "", PID: deadPID})

	// STUCK/0001 — open escalation (Stuck) + dead pid + enabled → dot suppressed.
	mkAttempt(t, root, "STUCK", "Stuck", "0001")
	ticketlog.Append(root, "STUCK", "0001", event.Event{Type: "enable", Actor: "h", Body: "on"})
	ticketlog.Append(root, "STUCK", "0001", event.Event{Type: "escalation", Actor: "a", Body: "which?"})
	writeSession(t, root, "STUCK", "0001", session.Identity{SessionID: "s-stuck", PID: deadPID})

	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	s.alive = func(pid int) bool { return pid == os.Getpid() }
	return s
}

// TestSessionDotStatesOnBoard pins the runtime-liveness dot the board card
// renders for each session state, and that never-run/empty/stuck attempts show no
// dot (a fresh attempt shows nothing rather than a misleading grey; a Stuck
// attempt is carried by the rust-red Stuck signals).
func TestSessionDotStatesOnBoard(t *testing.T) {
	h := seedDots(t).Handler()
	body := get(t, h, "/board").Body.String()

	for _, want := range []string{
		`data-testid="session-dot-RUN-0001" role="img" title="agent running" aria-label="agent running"`,
		`session-dot session-running`,
		`data-testid="session-dot-STOP-0001" role="img" title="stopped" aria-label="stopped"`,
		`session-dot session-stopped`,
		`data-testid="session-dot-GONE-0001" role="img" title="disabled" aria-label="disabled"`,
		`session-dot session-disabled`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("board missing dot markup %q\n%s", want, body)
		}
	}

	// No dot for never-run, empty-session-id, or Stuck attempts.
	for _, none := range []string{
		`session-dot-FRESH-0001`,
		`session-dot-EMPTY-0001`,
		`session-dot-STUCK-0001`,
	} {
		if strings.Contains(body, none) {
			t.Errorf("board should render no dot for %q\n%s", none, body)
		}
	}
}

// TestSessionDotOnAttemptsIndexAndDetail pins that the same dot renders on the
// per-ticket attempts list and the attempt detail header, not just the board.
func TestSessionDotOnAttemptsIndexAndDetail(t *testing.T) {
	h := seedDots(t).Handler()
	for _, path := range []string{"/ticket/STOP", "/ticket/STOP/0001"} {
		body := get(t, h, path).Body.String()
		if !strings.Contains(body, `data-testid="session-dot-STOP-0001"`) || !strings.Contains(body, `session-dot session-stopped`) {
			t.Errorf("%s should render the stopped dot\n%s", path, body)
		}
	}
}

// TestSessionDotRunningWinsWhenDisabled pins decision #2: a disabled attempt
// whose pid is still alive shows green (liveness wins) until the daemon reaps it.
func TestSessionDotRunningWinsWhenDisabled(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	mkAttempt(t, root, "LIVE", "Live-but-disabled", "0001") // no enable event → disabled
	writeSession(t, root, "LIVE", "0001", session.Identity{SessionID: "s", PID: os.Getpid()})

	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	s.alive = func(pid int) bool { return pid == os.Getpid() }
	body := get(t, s.Handler(), "/board").Body.String()
	if !strings.Contains(body, `data-testid="session-dot-LIVE-0001"`) || !strings.Contains(body, `session-dot session-running`) {
		t.Errorf("a disabled-but-alive attempt should still show the running dot\n%s", body)
	}
}

// TestPaletteVarsRenderedFromConstants pins that the dot colours reach the
// browser as :root custom properties sourced from the Go palette constants (not
// hand-typed in style.css), and that style.css carries no dot hex of its own.
func TestPaletteVarsRenderedFromConstants(t *testing.T) {
	h := newServer(t)
	page := get(t, h, "/").Body.String()
	for _, want := range []string{
		"--dot-running:" + Eucalypt,
		"--dot-stopped:" + Wattle,
		"--dot-disabled:" + GhostGum,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("board page missing palette custom property %q", want)
		}
	}

	css, err := os.ReadFile("static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(css), Wattle) {
		t.Errorf("style.css should reference var(--dot-*), not the Wattle hex %s directly", Wattle)
	}
}

// TestStaticSVGsMatchPaletteConstants is the drift guard for spec option 2: the
// favicon SVGs stay static files, but each hex they carry must equal its palette
// constant, so a change to one without the other fails CI.
func TestStaticSVGsMatchPaletteConstants(t *testing.T) {
	for _, c := range []struct {
		file string
		has  []string
		lack []string
	}{
		{"static/favicon.svg", []string{Eucalypt}, []string{MurrayRust, BlueMountains, Wattle}},
		{"static/favicon-stuck.svg", []string{Eucalypt, MurrayRust}, []string{BlueMountains}},
		{"static/favicon-review.svg", []string{Eucalypt, BlueMountains}, []string{MurrayRust}},
	} {
		data, err := os.ReadFile(c.file)
		if err != nil {
			t.Fatal(err)
		}
		svg := string(data)
		for _, h := range c.has {
			if !strings.Contains(svg, h) {
				t.Errorf("%s should carry palette hex %s", c.file, h)
			}
		}
		for _, h := range c.lack {
			if strings.Contains(svg, h) {
				t.Errorf("%s should not carry palette hex %s", c.file, h)
			}
		}
	}
}
