// Package agentmsg delivers human messages to an agent session that is already
// running (drvctl-059).
//
// The spine already reaches a *stopped* agent — a reopen/resolution wakes a resumed
// session and its resume note names what changed — but nothing reached a session
// that was live when the human posted: the only prompts are cold-start and resume.
// A message posted mid-session sat unseen, and once the agent claimed review it was
// lost for good.
//
// The fix is a progressive-reveal notice. Three pieces of state decide what is
// "unread":
//
//   - the explicit message marker: an event whose `to` field is "agent" (the board
//     compose form sets it). Actor is never consulted — spine notes are stamped
//     human:* too.
//   - the session watermark: the attempt's log tail when the daemon brought the
//     session up, handed to the agent as DRAIVER_SESSION_SEQ and persisted to
//     session/messages.json. Messages at or below it were delivered by the
//     cold-start brief or the resume note.
//   - the read cursor: the highest message seq the agent has read with `draiver
//     messages`, kept in session/messages-read.json.
//
// Unread = marked messages with seq > max(watermark, cursor). Both files are view
// state, deliberately off the hash chain, and rebuildable (a lost cursor only
// re-shows a message).
package agentmsg

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/store"
)

// ToAgent is the value of an event's `to` field that marks it as a message from
// the human to the agent working the attempt.
const ToAgent = "agent"

// Session environment the daemon layers over a supervised session's process env.
// The notice keys on these — not on DRAIVER_ATTEMPT, which the agent exports
// itself and which a coordinator may point at other tickets — so it always speaks
// for the session's *own* attempt.
const (
	EnvTicket  = "DRAIVER_SESSION_TICKET"
	EnvAttempt = "DRAIVER_SESSION_ATTEMPT"
	EnvSeq     = "DRAIVER_SESSION_SEQ"
)

// IsMessage reports whether e is a human→agent message (carries the explicit
// marker).
func IsMessage(e event.Event) bool { return e.To == ToAgent }

// Session is the in-session context the notice needs: which attempt the running
// session works and the log seq it started at.
type Session struct {
	Ticket  string
	Attempt string
	Seq     int
}

// SessionEnv builds the KEY=VALUE environment entries a session is spawned with.
func SessionEnv(ticket, attempt string, seq int) []string {
	return []string{
		EnvTicket + "=" + ticket,
		EnvAttempt + "=" + attempt,
		EnvSeq + "=" + strconv.Itoa(seq),
	}
}

// FromEnv reads the session context from getenv. ok is false outside a supervised
// session (any var missing or the seq malformed) — human CLI use.
func FromEnv(getenv func(string) string) (Session, bool) {
	t, a, s := getenv(EnvTicket), getenv(EnvAttempt), getenv(EnvSeq)
	if t == "" || a == "" || s == "" {
		return Session{}, false
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return Session{}, false
	}
	return Session{Ticket: t, Attempt: a, Seq: n}, true
}

// state is the on-disk shape of both small view-state files.
type state struct {
	Seq int `json:"seq"`
}

func watermarkPath(root store.Root, ticket, attempt string) string {
	return filepath.Join(root.SessionDir(ticket, attempt), "messages.json")
}

func cursorPath(root store.Root, ticket, attempt string) string {
	return filepath.Join(root.SessionDir(ticket, attempt), "messages-read.json")
}

func readSeq(path string) (int, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var s state
	if err := json.Unmarshal(b, &s); err != nil {
		return 0, fmt.Errorf("agentmsg: parse %s: %w", path, err)
	}
	return s.Seq, nil
}

func writeSeq(path string, seq int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, _ := json.Marshal(state{Seq: seq})
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ReadWatermark returns the session-start seq the daemon recorded for the
// attempt's most recent session (0 when none was recorded).
func ReadWatermark(root store.Root, ticket, attempt string) (int, error) {
	return readSeq(watermarkPath(root, ticket, attempt))
}

// WriteWatermark records seq as the attempt's current session-start watermark.
func WriteWatermark(root store.Root, ticket, attempt string, seq int) error {
	return writeSeq(watermarkPath(root, ticket, attempt), seq)
}

// ReadCursor returns the highest message seq read via `draiver messages` (0 when
// nothing was read). A missing file is not an error.
func ReadCursor(root store.Root, ticket, attempt string) (int, error) {
	return readSeq(cursorPath(root, ticket, attempt))
}

// AdvanceCursor moves the read cursor up to seq. It never moves it down.
func AdvanceCursor(root store.Root, ticket, attempt string, seq int) error {
	cur, err := ReadCursor(root, ticket, attempt)
	if err != nil {
		cur = 0 // an unreadable cursor is simply rewritten
	}
	if seq <= cur {
		return nil
	}
	return writeSeq(cursorPath(root, ticket, attempt), seq)
}

// Unread returns the marked messages in events with seq > max(watermark, cursor),
// in log order.
func Unread(events []event.Event, watermark, cursor int) []event.Event {
	floor := max(watermark, cursor)
	var out []event.Event
	for _, e := range events {
		if e.Seq > floor && IsMessage(e) {
			out = append(out, e)
		}
	}
	return out
}

// Notice is the count-only banner. It deliberately carries no message content —
// the agent pulls that with `draiver messages` when it chooses to.
func Notice(n int) string {
	noun := "messages"
	if n == 1 {
		noun = "message"
	}
	return fmt.Sprintf("📬 %d unread %s from the human since this session started — run `draiver messages` to read.", n, noun)
}

// ReviewHint is printed when `draiver review` is soft-stopped on unread messages.
const ReviewHint = "review not recorded — read with `draiver messages`, or re-run with `--ignore-unread` to claim anyway."
