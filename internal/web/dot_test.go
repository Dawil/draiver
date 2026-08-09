package web

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/reconcile"
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

// writeCtlLog appends daemon health transitions to an attempt's session/ctl.jsonl
// exactly as draiverctld's reconciler would (drvctl-027) — marshalling real
// reconcile.HealthEvent values, so the dot reader is pinned to the shared wire
// contract (JSON field names + the "start"/"end" event kinds), not a hand-typed
// copy that could drift.
func writeCtlLog(t *testing.T, root store.Root, id, att string, evs ...reconcile.HealthEvent) {
	t.Helper()
	if err := root.EnsureSessionDir(id, att); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, ev := range evs {
		line, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(root.SessionCtlLogPath(id, att), []byte(b.String()), 0o644); err != nil {
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

	// ERR/0001 — enabled, empty session_id (the restart --new-session wedge), and an
	// open worktree-clash in ctl.jsonl → error (crimson). This is the founding
	// incident: session_id="" alone renders no dot, but the open error class does.
	mkAttempt(t, root, "ERR", "Erroring", "0001")
	ticketlog.Append(root, "ERR", "0001", event.Event{Type: "enable", Actor: "h", Body: "on"})
	writeSession(t, root, "ERR", "0001", session.Identity{SessionID: "", PID: deadPID})
	writeCtlLog(t, root, "ERR", "0001", reconcile.HealthEvent{Class: reconcile.ClassWorktree, Event: "start"})

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
		`data-testid="session-dot-ERR-0001" role="img" title="draiverctld cannot run this attempt" aria-label="draiverctld cannot run this attempt"`,
		`session-dot session-error`,
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

// TestSessionDotErrorRule pins the ctl.jsonl-derived error dot: it is
// most-recent-transition-per-class-wins, class-agnostic, and outranks
// stopped/disabled — but a live pid still wins over it.
func TestSessionDotErrorRule(t *testing.T) {
	for _, c := range []struct {
		name      string
		enabled   bool
		sessionID string
		pid       int
		alivePID  bool
		log       []reconcile.HealthEvent
		wantState string // "" = no dot
	}{
		{
			name: "open worktree-clash on an empty-session wedge lights error",
			// The founding incident: session_id="" (would be no dot) but an open class.
			enabled: true, sessionID: "", pid: deadPID,
			log:       []reconcile.HealthEvent{{Class: reconcile.ClassWorktree, Event: "start"}},
			wantState: "error",
		},
		{
			name:    "resolved class (start then end) shows no error, falls back to stopped",
			enabled: true, sessionID: "s", pid: deadPID,
			log: []reconcile.HealthEvent{
				{Class: reconcile.ClassWorktree, Event: "start"},
				{Class: reconcile.ClassWorktree, Event: "end"},
			},
			wantState: "stopped",
		},
		{
			name:    "admit-failed catch-all lights error too (class-agnostic, gotcha #2)",
			enabled: true, sessionID: "", pid: deadPID,
			log:       []reconcile.HealthEvent{{Class: reconcile.ClassAdmit, Event: "start"}},
			wantState: "error",
		},
		{
			name:    "one class resolved, another still open → error",
			enabled: true, sessionID: "s", pid: deadPID,
			log: []reconcile.HealthEvent{
				{Class: reconcile.ClassWorktree, Event: "start"},
				{Class: reconcile.ClassWorktree, Event: "end"},
				{Class: reconcile.ClassAdmit, Event: "start"},
			},
			wantState: "error",
		},
		{
			name:    "error outranks disabled",
			enabled: false, sessionID: "s", pid: deadPID,
			log:       []reconcile.HealthEvent{{Class: reconcile.ClassWorktree, Event: "start"}},
			wantState: "error",
		},
		{
			name:    "a live pid wins over an open error class (the impossible pair)",
			enabled: true, sessionID: "s", pid: os.Getpid(), alivePID: true,
			log:       []reconcile.HealthEvent{{Class: reconcile.ClassWorktree, Event: "start"}},
			wantState: "running",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := store.Root{Dir: t.TempDir()}
			mkAttempt(t, root, "T", "Ticket", "0001")
			if c.enabled {
				ticketlog.Append(root, "T", "0001", event.Event{Type: "enable", Actor: "h", Body: "on"})
			}
			writeSession(t, root, "T", "0001", session.Identity{SessionID: c.sessionID, PID: c.pid})
			writeCtlLog(t, root, "T", "0001", c.log...)

			s, err := New(root)
			if err != nil {
				t.Fatal(err)
			}
			s.alive = func(pid int) bool { return pid == os.Getpid() }

			// State left zero (not NeedsMe): these cases exercise the error/liveness
			// rules, not the Stuck-suppression path.
			got := s.sessionDot(project.Attempt{Ticket: "T", ID: "0001", Enabled: c.enabled})
			if got.State != c.wantState {
				t.Errorf("state = %q, want %q (label %q)", got.State, c.wantState, got.Label)
			}
		})
	}
}

// TestSessionDotErrorMissingLogIsUnchanged pins that an attempt with no ctl.jsonl
// (older data, or one that never tripped) behaves exactly as before the error dot.
func TestSessionDotErrorMissingLogIsUnchanged(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	mkAttempt(t, root, "T", "Ticket", "0001")
	ticketlog.Append(root, "T", "0001", event.Event{Type: "enable", Actor: "h", Body: "on"})
	writeSession(t, root, "T", "0001", session.Identity{SessionID: "s", PID: deadPID})
	// No writeCtlLog: no ctl.jsonl at all.
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	s.alive = func(pid int) bool { return pid == os.Getpid() }
	if got := s.sessionDot(project.Attempt{Ticket: "T", ID: "0001", Enabled: true}); got.State != "stopped" {
		t.Errorf("missing ctl.jsonl should read as stopped, got %q", got.State)
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
		"--dot-error:" + Waratah,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("board page missing palette custom property %q", want)
		}
	}

	css, err := os.ReadFile("static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, hex := range []string{Wattle, Waratah} {
		if strings.Contains(string(css), hex) {
			t.Errorf("style.css should reference var(--dot-*), not the hex %s directly", hex)
		}
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
