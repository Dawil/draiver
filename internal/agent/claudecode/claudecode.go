// Package claudecode implements the agent.Adapter seam over Claude Code driven
// headless — one `claude` process per session, spoken to in newline-delimited
// stream-json on stdio (`--input-format stream-json --output-format
// stream-json`). It normalizes Claude's wire events into agent.Event and keeps
// every Claude-specific type unexported, so the supervisor never sees Claude.
package claudecode

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"

	"github.com/Dawil/draiver/internal/agent"
)

// DefaultBin is the executable used when Adapter.Bin is empty.
const DefaultBin = "claude"

// Adapter is a Claude Code session. The zero value is not usable; construct one
// with New, then bring it online with Spawn or Resume exactly once.
type Adapter struct {
	// Bin is the claude executable; empty means DefaultBin resolved on PATH.
	Bin string

	// newCmd builds the process for a set of args. It is a field so tests can
	// inject a fake agent without a real claude on PATH; production leaves it nil
	// and buildCmd is used.
	newCmd func(ctx context.Context, bin string, args []string) *exec.Cmd

	mu         sync.Mutex
	started    bool
	sessionID  string
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	events     chan agent.Event
	done       chan struct{} // closed when the process has been reaped
	killed     chan struct{} // closed by Kill so scan abandons blocked sends
	killOnce   sync.Once
	online     chan struct{} // closed by scan on the first system/init frame
	onlineOnce sync.Once
	waitErr    error
}

// New returns an unstarted Claude Code adapter. Set Bin to override the
// executable.
func New() *Adapter {
	return &Adapter{events: make(chan agent.Event, 64), online: make(chan struct{})}
}

// Spawn starts a fresh session and returns its client-minted session id. The id
// is generated before the process starts, so a caller records the cattle handle
// even if the process fails to come up.
func (a *Adapter) Spawn(ctx context.Context, spec agent.SessionSpec) (string, error) {
	id, err := newUUID()
	if err != nil {
		return "", fmt.Errorf("claudecode: mint session id: %w", err)
	}
	if err := a.launch(ctx, spec, baseArgs(spec, "--session-id", id)); err != nil {
		return id, err
	}
	return id, nil
}

// Resume reattaches to a prior session by id in the same working directory.
func (a *Adapter) Resume(ctx context.Context, sessionID string, spec agent.SessionSpec) error {
	if sessionID == "" {
		return errors.New("claudecode: resume needs a session id")
	}
	return a.launch(ctx, spec, baseArgs(spec, "--resume", sessionID))
}

func (a *Adapter) launch(ctx context.Context, spec agent.SessionSpec, args []string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.started {
		return errors.New("claudecode: session already started")
	}
	if spec.WorkDir == "" {
		return errors.New("claudecode: spec.WorkDir is required")
	}

	bin := a.Bin
	if bin == "" {
		bin = DefaultBin
	}
	build := a.newCmd
	if build == nil {
		build = buildCmd
	}
	cmd := build(ctx, bin, args)
	cmd.Dir = spec.WorkDir
	// Layer spec.Env over the process environment, preserving any environment
	// the command builder already set (tests rely on this).
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Env = append(cmd.Env, spec.Env...)
	// Own the process group so Kill reaps the whole tool subtree, not just the
	// top process.
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("claudecode: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("claudecode: stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("claudecode: start %s: %w", bin, err)
	}

	a.started = true
	a.cmd = cmd
	a.stdin = stdin
	a.done = make(chan struct{})
	a.killed = make(chan struct{})
	go a.scan(stdout)
	return nil
}

// scan owns the events channel: it reads stream-json lines until the process's
// stdout closes, then reaps the process and closes the channel. It is the sole
// sender on a.events.
func (a *Adapter) scan(stdout io.Reader) {
	r := bufio.NewReaderSize(stdout, 1<<20)
loop:
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			for _, ev := range normalize(line) {
				if ev.Kind == agent.EventSystem && ev.SessionID != "" {
					a.mu.Lock()
					a.sessionID = ev.SessionID
					a.mu.Unlock()
					// The system/init frame means the session is live: a Spawn
					// forked and confirmed its id, or a Resume actually reattached.
					// Signal online so a supervisor can distinguish a resume that
					// came up from one that died on a stale id (drvctl-016).
					a.onlineOnce.Do(func() { close(a.online) })
				}
				// Abandon the send if Kill fired, so a stalled consumer can never
				// deadlock a reap.
				select {
				case a.events <- ev:
				case <-a.killed:
					break loop
				}
			}
		}
		if err != nil {
			if err != io.EOF {
				select {
				case a.events <- agent.Event{Kind: agent.EventError, Err: "read stdout: " + err.Error()}:
				case <-a.killed:
				}
			}
			break
		}
	}
	werr := a.cmd.Wait()
	a.mu.Lock()
	a.waitErr = werr
	close(a.done)
	a.mu.Unlock()
	close(a.events)
}

// Prompt sends a user turn to the running session.
func (a *Adapter) Prompt(ctx context.Context, text string) error {
	line, err := json.Marshal(userMessage(text))
	if err != nil {
		return fmt.Errorf("claudecode: encode prompt: %w", err)
	}
	return a.writeLine(line)
}

// Interrupt cancels the in-flight turn via the stream-json control protocol
// (interrupt_receipt_v1). The session stays alive.
func (a *Adapter) Interrupt(ctx context.Context) error {
	id, err := newUUID()
	if err != nil {
		return fmt.Errorf("claudecode: mint request id: %w", err)
	}
	line, err := json.Marshal(controlRequest{
		Type:      "control_request",
		RequestID: id,
		Request:   controlBody{Subtype: "interrupt"},
	})
	if err != nil {
		return fmt.Errorf("claudecode: encode interrupt: %w", err)
	}
	return a.writeLine(line)
}

// Decide answers a tool-permission callback (an EventPermission carrying the
// request id) by writing a control_response frame back to the session. Allowing
// echoes the (possibly rewritten) input as updatedInput; denying carries the
// reason the agent sees. It satisfies agent.Permissioner.
func (a *Adapter) Decide(ctx context.Context, requestID string, d agent.Decision) error {
	if requestID == "" {
		return errors.New("claudecode: decide needs a request id")
	}
	var pd permissionDecision
	if d.Allow {
		pd.Behavior = "allow"
		pd.UpdatedInput = d.Input
	} else {
		pd.Behavior = "deny"
		pd.Message = d.Message
	}
	payload, err := json.Marshal(pd)
	if err != nil {
		return fmt.Errorf("claudecode: encode permission decision: %w", err)
	}
	line, err := json.Marshal(controlResponse{
		Type: "control_response",
		Response: controlResponseBody{
			Subtype:   "success",
			RequestID: requestID,
			Response:  payload,
		},
	})
	if err != nil {
		return fmt.Errorf("claudecode: encode control_response: %w", err)
	}
	return a.writeLine(line)
}

// writeLine appends a newline and writes one stream-json frame to stdin under
// the lock, so Prompt and Interrupt never interleave partial lines.
func (a *Adapter) writeLine(line []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.started {
		return errors.New("claudecode: session not started")
	}
	if a.stdin == nil {
		return errors.New("claudecode: session closed")
	}
	if _, err := a.stdin.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("claudecode: write stdin: %w", err)
	}
	return nil
}

// Stream returns the normalized event channel.
func (a *Adapter) Stream() <-chan agent.Event { return a.events }

// Online returns a channel closed once the session has come online — the scan
// loop saw the first system/init frame. It lets the supervisor confirm a Resume
// actually reattached before trusting it, rather than looping on a session id
// that can no longer be resumed. It satisfies the optional agent.Onliner
// capability. The channel is created at New, so it is safe to read before the
// process starts (it simply stays open until the init frame arrives, or forever
// if the process dies without emitting one).
func (a *Adapter) Online() <-chan struct{} { return a.online }

// SessionID returns the confirmed session id once known (after Spawn/Resume and
// the init frame), else empty.
func (a *Adapter) SessionID() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessionID
}

// PID returns the session process's pid, or 0 before Spawn/Resume or after the
// process has been reaped. It is best-effort runtime metadata (recorded in
// session.json for status and re-adoption), not part of the core Adapter seam.
func (a *Adapter) PID() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cmd == nil || a.cmd.Process == nil {
		return 0
	}
	return a.cmd.Process.Pid
}

// Kill reaps the session process group and blocks until it is gone. The session
// id and log survive for a later Resume. It is idempotent.
func (a *Adapter) Kill() error {
	a.mu.Lock()
	if !a.started {
		a.mu.Unlock()
		return errors.New("claudecode: session not started")
	}
	proc := a.cmd.Process
	done := a.done
	// Release any send the scanner is blocked on, so a stalled consumer cannot
	// wedge the reap.
	a.killOnce.Do(func() { close(a.killed) })
	// Closing stdin so nothing can write mid-reap; the scanner ignores the
	// resulting pipe error.
	if a.stdin != nil {
		_ = a.stdin.Close()
		a.stdin = nil
	}
	a.mu.Unlock()

	if proc != nil {
		// Signal the whole group (negative pid); fall back to the lone process.
		if err := syscall.Kill(-proc.Pid, syscall.SIGKILL); err != nil {
			_ = proc.Kill()
		}
	}
	<-done
	return nil
}

// baseArgs assembles the headless stream-json invocation plus the spec's
// options. head is the session-selection flag pair (--session-id / --resume).
func baseArgs(spec agent.SessionSpec, head ...string) []string {
	args := []string{
		"--print",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose", // required for stream-json output under --print
		// Route tool-permission asks over the stdio control protocol as
		// control_request{can_use_tool} frames, which Decide answers — the seam the
		// permission gate lives on. Without it, headless claude auto-denies any tool
		// that needs approval ("…you haven't granted it yet") instead of asking the
		// client, so the gate is never consulted. The flag is undocumented (dropped
		// from --help) but live in claude 2.1.x; verified against 2.1.216.
		"--permission-prompt-tool", "stdio",
	}
	args = append(args, head...)
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if spec.SystemPromptAppend != "" {
		args = append(args, "--append-system-prompt", spec.SystemPromptAppend)
	}
	// Strip the per-machine system-prompt sections (cwd/env/git) so every worktree
	// shares a byte-identical prefix — the cross-worktree prompt-cache unlock. The
	// flag takes no value and is only emitted when the toggle is on, so a spec that
	// leaves it off launches byte-for-byte as before (drvctl-032). Support is
	// verified before the daemon comes up (SupportsExcludeDynamicSystemPrompt).
	if spec.ExcludeDynamicSystemPromptSections {
		args = append(args, "--exclude-dynamic-system-prompt-sections")
	}
	if spec.PermissionMode != "" {
		args = append(args, "--permission-mode", spec.PermissionMode)
	}
	if len(spec.AllowedTools) > 0 {
		args = append(args, "--allowedTools")
		args = append(args, spec.AllowedTools...)
	}
	if len(spec.DisallowedTools) > 0 {
		args = append(args, "--disallowedTools")
		args = append(args, spec.DisallowedTools...)
	}
	return args
}

func buildCmd(ctx context.Context, bin string, args []string) *exec.Cmd {
	return exec.CommandContext(ctx, bin, args...)
}

// excludeDynamicFlag is the claude flag that moves the per-machine system-prompt
// sections into the first user message — the cross-worktree prompt-cache unlock.
const excludeDynamicFlag = "--exclude-dynamic-system-prompt-sections"

// SupportsExcludeDynamicSystemPrompt reports whether the claude binary at bin
// accepts --exclude-dynamic-system-prompt-sections, by probing `bin --help` for
// the flag. It lets the daemon fail loud at start-up when an operator turns the
// toggle on but the pinned agent is too old to honor it, rather than silently
// launching a session without the flag (drvctl-032 spec #5; pairs with the
// drvctl-033 version pin). bin empty means DefaultBin resolved on PATH. A probe
// that cannot run at all (binary missing, --help errored with no output) is
// returned as an error so the caller can distinguish "unsupported" from
// "couldn't check".
func SupportsExcludeDynamicSystemPrompt(ctx context.Context, bin string) (bool, error) {
	if bin == "" {
		bin = DefaultBin
	}
	out, err := exec.CommandContext(ctx, bin, "--help").CombinedOutput()
	if err != nil && len(out) == 0 {
		return false, fmt.Errorf("claudecode: probe %s --help: %w", bin, err)
	}
	return strings.Contains(string(out), excludeDynamicFlag), nil
}

// --- stream-json input frames ---

type inputMessage struct {
	Type    string       `json:"type"`
	Message inputMsgBody `json:"message"`
}

type inputMsgBody struct {
	Role    string             `json:"role"`
	Content []inputTextContent `json:"content"`
}

type inputTextContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func userMessage(text string) inputMessage {
	return inputMessage{
		Type: "user",
		Message: inputMsgBody{
			Role:    "user",
			Content: []inputTextContent{{Type: "text", Text: text}},
		},
	}
}

type controlRequest struct {
	Type      string      `json:"type"`
	RequestID string      `json:"request_id"`
	Request   controlBody `json:"request"`
}

type controlBody struct {
	Subtype string `json:"subtype"`
}

// controlResponse answers an inbound control_request (e.g. the can_use_tool
// permission callback). Response.Response carries the subtype-specific payload —
// for a permission callback, a marshalled permissionDecision.
type controlResponse struct {
	Type     string              `json:"type"`
	Response controlResponseBody `json:"response"`
}

type controlResponseBody struct {
	Subtype   string          `json:"subtype"` // "success"
	RequestID string          `json:"request_id"`
	Response  json.RawMessage `json:"response"`
}

// permissionDecision is the can_use_tool response payload. Behavior is "allow"
// or "deny"; UpdatedInput is the call input to run on allow, Message the reason
// on deny.
type permissionDecision struct {
	Behavior     string          `json:"behavior"`
	UpdatedInput json.RawMessage `json:"updatedInput,omitempty"`
	Message      string          `json:"message,omitempty"`
}

// newUUID returns a random RFC-4122 v4 UUID string without pulling in a
// dependency.
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
