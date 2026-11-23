package web

import (
	"bytes"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Dawil/draiver/internal/agentmsg"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/streamlog"
	"github.com/Dawil/draiver/internal/systemlog"
)

// composeTypes is the curated allow-list of event types the web compose box may
// append. draiver log accepts a free-form --type, but the web form must never
// let a human hand-type a lifecycle type that owns a dedicated flow —
// escalation/resolution (the escalate/resolve pair), review (the agent's own
// claim), enable/disable (supervision), or created. Only note and gotcha (plain
// context), decision (which also reopens a Review attempt to Running), and done
// (the Review terminal action) are allowed; any other type is a 400.
var composeTypes = map[string]bool{
	"note":     true,
	"gotcha":   true,
	"decision": true,
	"done":     true,
}

// appendComposed appends a web-composed event through the in-process data-access
// gateway (drv-008) rather than reimplementing the append or shelling the CLI:
// note/gotcha/decision go through AppendTyped (the generic `log`) and the terminal
// action through Done — the same library functions `draiver log`/`draiver done`
// call, so the board and a terminal share one write path. The gateway stamps the
// server's actor and runs the same attempt-existence and link checks every append
// goes through.
//
// to is the drvctl-059 message marker: "agent" when the human addressed the entry
// to the agent (the compose form's default-checked "Message the agent" box, and
// the Review "Decision — reopen" button). It is ignored for the terminal `done`.
func (s *Server) appendComposed(typ, id, att, body string, tags []string, supersedes []int, to string) error {
	if typ == "done" {
		_, err := s.gw.Done(id, att, body)
		return err
	}
	_, err := s.gw.AppendTyped(id, att, event.Event{Type: typ, Body: body, Tags: tags, Supersedes: supersedes, To: to})
	return err
}

// parseComposeTags splits a free-text tags field into individual labels on
// whitespace or commas, dropping empties. An empty field yields no tags.
func parseComposeTags(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
	if len(fields) == 0 {
		return nil
	}
	return fields
}

// handleAgentLogs streams an attempt's live session logs as Server-Sent Events
// (GET /ticket/{id}/{attempt}/agent-logs) — the first stream in the web layer,
// which is otherwise all htmx polling. It renders the stream in-process via the
// shared internal/streamlog.TailStream (the same follow `draiver ctl logs -f`
// runs, drv-008) and relays each rendered line as one SSE `data:` event. Writing
// to an http.ResponseWriter (not a *os.File) the renderer is non-TTY, so it emits
// plain, uncoloured text — exactly what this read-only panel wants, with the
// prefix column, markdown-as-literal, and ctl.jsonl health interleave reused
// verbatim. tail=50 matches the CLI's default backlog window.
//
// The follow is bound to r.Context(): when the client closes the SSE — the
// <details> panel collapses, the page is left — the context cancels and TailStream
// returns at its next idle poll. Each line is relayed as text and set client-side
// via textContent; it is never routed through the markdown→HTML path, so the
// untrusted session content (arbitrary tool output + model prose) cannot inject
// markup. A hand-run attempt with no session yet simply streams nothing; the
// client shows a quiet placeholder, not an error.
func (s *Server) handleAgentLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	att := r.PathValue("attempt")
	if !s.root.AttemptExists(id, att) {
		http.NotFound(w, r)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.fail(w, fmt.Errorf("streaming unsupported"))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Disable proxy buffering so the stream reaches the panel line-by-line.
	w.Header().Set("X-Accel-Buffering", "no")

	// TailStream writes newline-terminated rendered lines; sseLineWriter reframes
	// each complete line as one `data:` record and flushes, so the panel updates
	// live rather than in buffer-sized bursts. A follow never ends on its own — it
	// unblocks when r.Context() cancels on client disconnect.
	sse := &sseLineWriter{w: w, flusher: flusher}
	// drv-045: the Agent Logs panel is now purely the agent session stream — no
	// ctl.jsonl health interleave, no synthetic session-online/turn-end lines. Those
	// system events moved to the System Logs panel (handleSystemLogs). TailAgentStream
	// renders stream.jsonl alone with the system-role lines suppressed; the on-disk
	// stream.jsonl and its --json passthrough are unchanged (this is a UI split).
	_ = streamlog.TailAgentStream(r.Context(), sse,
		s.root.SessionStreamPath(id, att),
		true /*follow*/, 50 /*tail backlog*/)
}

// handleSystemLogs streams the attempt's System Logs panel (GET
// /ticket/{id}/{attempt}/system-logs) — drv-045's second live panel, modelled on
// handleAgentLogs with the same SSE framing, the same sseLineWriter, and the same
// r.Context() binding so the follow ends when the panel collapses. Its source is the
// system-event journal (system.jsonl) interleaved with draiverctld's health
// transitions (ctl.jsonl), each line rendered with its own recorded timestamp —
// never render-time now() — so replaying an attempt shows the same times a live
// follow showed. Lines are relayed as text and set client-side via textContent,
// never the markdown→HTML path, the same XSS discipline as Agent Logs.
func (s *Server) handleSystemLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	att := r.PathValue("attempt")
	if !s.root.AttemptExists(id, att) {
		http.NotFound(w, r)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.fail(w, fmt.Errorf("streaming unsupported"))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	sse := &sseLineWriter{w: w, flusher: flusher}
	_ = streamlog.TailSystemLog(r.Context(), sse,
		s.root.SessionSystemLogPath(id, att), s.root.SessionCtlLogPath(id, att),
		true /*follow*/, 50 /*tail backlog*/)
}

// emitSystem appends one operational system event to the attempt's system.jsonl for
// the System Logs stream (drv-045). It stamps the server's actor onto the record (so
// "by whom" is legible) and is strictly best-effort: a write failure is swallowed —
// never failing the board action that produced it — exactly like the reconciler's
// appendHealth. system.jsonl is an operational projection, never a truth substrate,
// so it must not be able to break the action it merely observes.
func (s *Server) emitSystem(id, att string, ev systemlog.SystemEvent) {
	if ev.Fields == nil {
		ev.Fields = map[string]string{}
	}
	if _, ok := ev.Fields["actor"]; !ok && s.actor != "" {
		ev.Fields["actor"] = s.actor
	}
	_ = systemlog.Append(s.root, id, att, time.Now, ev)
}

// relaySystemOutput relays an operator action's captured subprocess output into the
// System Logs stream (drv-048): it splits output on newlines and appends one
// SystemEvent per non-blank line under class, each stamped at emit time like every
// other system line, so the diagnostic stdout/stderr a check or control produced
// interleaves with the started/verdict summary events a reviewer already sees.
// failed marks every line with the error role so a failed command's output reads
// red. It is best-effort by construction — emitSystem swallows its write error — so
// relaying the output can never fail the action it observes (the emitSystem /
// appendHealth discipline). A trailing '\r' is trimmed so a CRLF stream renders
// cleanly; blank lines are dropped (renderSystemLine shows nothing for them anyway).
func (s *Server) relaySystemOutput(id, att, class, output string, failed bool) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		s.emitSystem(id, att, systemlog.SystemEvent{Class: class, Message: line, Error: failed})
	}
}

// systemRelay is a line-buffered io.Writer that relays a LIVE subprocess stream into
// the System Logs panel (drv-048) — the "stop sending to io.Discard" substrate wired
// as the stdout/stderr writer of a streaming operator action (handlePromote's
// promote.Run). It buffers bytes until a '\n', then relays everything up to it as one
// system line under class, exactly the discipline sseLineWriter uses for SSE framing.
// isErr selects the role each line takes: a stderr relay sets it so the command's
// error output reads red in the panel, stdout stays the neutral system role. relay is
// the sink (typically a Server.relaySystemOutput closure bound to id/att); it is
// best-effort, so a Write never reports an error back to the subprocess copy loop.
type systemRelay struct {
	relay func(line string, isErr bool)
	isErr bool
	buf   []byte
}

func (w *systemRelay) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.emitLine(string(w.buf[:i]))
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}

// emitLine relays one physical line, trimming a trailing CR and dropping a blank one
// (a blank system line renders nothing). It is the shared tail of Write and flush.
func (w *systemRelay) emitLine(line string) {
	line = strings.TrimRight(line, "\r")
	if strings.TrimSpace(line) == "" {
		return
	}
	w.relay(line, w.isErr)
}

// flush relays any held partial final line — output the command wrote without a
// trailing newline — and must be called once the subprocess has returned, so the last
// line is not silently dropped. It is idempotent on an empty buffer.
func (w *systemRelay) flush() {
	if len(w.buf) == 0 {
		return
	}
	w.emitLine(string(w.buf))
	w.buf = nil
}

// sseLineWriter adapts streamlog's line-oriented io.Writer output to Server-Sent
// Events: it buffers bytes until a newline, then emits everything up to it as one
// `data: <line>\n\n` record and flushes. A stray CR is trimmed so it cannot break
// the `data:` framing, mirroring the bufio.Scanner the subprocess relay used. A
// write that fails (the client went away) is swallowed — the follow ends when the
// request context cancels, so a broken pipe here need not propagate.
type sseLineWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	buf     []byte
}

func (s *sseLineWriter) Write(p []byte) (int, error) {
	s.buf = append(s.buf, p...)
	for {
		i := bytes.IndexByte(s.buf, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(string(s.buf[:i]), "\r")
		s.buf = s.buf[i+1:]
		if _, err := fmt.Fprintf(s.w, "data: %s\n\n", line); err != nil {
			return len(p), nil // client went away; the context cancel ends the follow
		}
		s.flusher.Flush()
	}
	return len(p), nil
}

// handleLogAppend appends a human-composed typed event to an attempt (POST
// /ticket/{id}/{attempt}/log) and returns the re-rendered live fragment, so the
// new entry, the state badge, and the log count all update from one swap. It is
// the webui's single write path: a Decision reopens a Review attempt to Running
// and a Done closes it (both via Derive on the appended lifecycle event), while
// note/gotcha/decision on any other attempt just record context.
func (s *Server) handleLogAppend(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "draiver: cross-origin request refused", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	att := r.PathValue("attempt")
	if !s.root.AttemptExists(id, att) {
		http.NotFound(w, r)
		return
	}
	typ := r.FormValue("type")
	if !composeTypes[typ] {
		http.Error(w, "draiver: unsupported log type "+strconv.Quote(typ), http.StatusBadRequest)
		return
	}
	// The two Review actions (decision/done) may fire without a typed reason, so
	// they fall back to a sensible default body; a plain note/gotcha with no body
	// is a mistake and is rejected.
	body := strings.TrimSpace(r.FormValue("body"))
	if body == "" {
		switch typ {
		case "done":
			body = "Ticket closed."
		case "decision":
			body = "Reopened to Running."
		default:
			http.Error(w, "draiver: a "+typ+" needs a message", http.StatusBadRequest)
			return
		}
	}
	// Optional retrieval aids (drv-026), carried only by the generic compose form (the
	// Review-action buttons include just the body): opaque tags and a supersedes seq.
	// A blank/zero value is simply absent, so a plain entry appends exactly as before;
	// a non-existent supersedes seq is rejected by the gateway's append-time check.
	tags := parseComposeTags(r.FormValue("tags"))
	var supersedes []int
	if sv := strings.TrimSpace(r.FormValue("supersedes")); sv != "" {
		n, err := strconv.Atoi(sv)
		if err != nil || n < 1 {
			http.Error(w, "draiver: supersedes must be a positive event seq", http.StatusBadRequest)
			return
		}
		supersedes = []int{n}
	}
	// The drvctl-059 message marker: only the exact "agent" value marks the entry as
	// a message to the agent (counted by the in-session unread notice); anything else
	// — including an unchecked box, which posts nothing — leaves it unmarked.
	to := ""
	if r.FormValue("to") == agentmsg.ToAgent {
		to = agentmsg.ToAgent
	}
	if err := s.appendComposed(typ, id, att, body, tags, supersedes, to); err != nil {
		s.fail(w, err)
		return
	}
	vm, err := s.detail(id, att)
	if err != nil {
		s.fail(w, err)
		return
	}
	live, err := s.executeLive(vm)
	if err != nil {
		s.fail(w, err)
		return
	}
	// Always 200: the badge flips in-place via the OOB swap. A Done attempt's log
	// region keeps its poll attribute and self-cancels on its next /live poll (the
	// 286 path), so we do not need to signal termination from this response.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(live)
}
