// Package agent is the thin seam between draiverctld and a coding agent. A
// supervisor drives every agent — Claude Code, Aider, Codex — through the one
// Adapter interface here and consumes a single normalized Event stream, so no
// agent-specific type ever leaks past this package. The reference backend
// (Claude Code over headless stream-json) lives in the claudecode subpackage.
package agent

import (
	"context"
	"encoding/json"
)

// Adapter drives one live session: one OS process against one working
// directory, identified by a session id that is the durable cattle handle.
// The supervisor may Kill the process freely and later Resume from the same id,
// because the truth is the ticket log, not the process.
//
// The interface is the spec's five control verbs — Spawn, Resume, Kill, Stream,
// Interrupt — plus Prompt, the data-plane verb they sit around: a session is
// multi-turn, and Interrupt only means something against a turn Prompt started.
//
// One Adapter value corresponds to one session for its whole lifetime. Spawn or
// Resume brings it online exactly once; the remaining methods act on that
// session. Methods are safe for concurrent use: typically one goroutine ranges
// over Stream while another calls Prompt/Interrupt/Kill.
type Adapter interface {
	// Spawn starts a fresh session and returns its session id. The id is
	// allocated before the process is confirmed, so the caller can record the
	// cattle handle even if the spawn later fails. Spawn does not send a prompt;
	// call Prompt to begin a turn.
	Spawn(ctx context.Context, spec SessionSpec) (sessionID string, err error)

	// Resume reattaches to a previously-spawned session by id, replaying its
	// history from the agent's own store. The workdir in spec must match the
	// original. Like Spawn it does not prompt.
	Resume(ctx context.Context, sessionID string, spec SessionSpec) error

	// Prompt submits a user turn to the running session. It returns once the
	// prompt is handed to the agent, not when the turn completes; the turn's
	// output arrives on Stream and ends with an EventTurnEnd.
	Prompt(ctx context.Context, text string) error

	// Stream returns the session's normalized event channel. It returns the same
	// channel on every call. The channel is closed when the session process
	// exits (cleanly or reaped); a closed channel with no error event means a
	// clean exit.
	Stream() <-chan Event

	// Interrupt cancels the in-flight turn without ending the session; the
	// session stays alive and can be Prompted again. It is a no-op if no turn is
	// running.
	Interrupt(ctx context.Context) error

	// Kill reaps the session process. The session id and its log survive for a
	// later Resume. Kill is idempotent and blocks until the process is gone.
	Kill() error
}

// Permissioner is the optional capability of an adapter whose agent surfaces a
// tool-permission callback: the agent asks "may I use this tool?" as an
// EventPermission on Stream, and the supervisor answers it here by request id.
// It is discovered by type-assertion, like the PID and SessionID accessors, so
// the core Adapter stays the five control verbs — an agent without an
// interactive permission callback (e.g. one that pre-authorizes via flags) need
// not implement it.
type Permissioner interface {
	// Decide answers the pending PermissionRequest with the given id. Allowing
	// lets the call proceed; denying stops it with Decision.Message as the reason
	// the agent sees. It returns once the answer is handed to the agent.
	Decide(ctx context.Context, requestID string, d Decision) error
}

// Onliner is the optional capability of an adapter that can signal when its
// session has come online — the first system/init frame observed on its stream.
// The supervisor uses it to confirm a Resume actually reattached (the process
// forked and the agent acknowledged the session) before trusting it, rather than
// looping forever on a session id that is no longer resumable: an unresumable id
// launches a process that dies on arrival, never coming online. It is discovered
// by type-assertion like the other optional accessors; an adapter that does not
// implement it is assumed to have come online (the pre-cascade behaviour).
type Onliner interface {
	// Online returns a channel closed once the session is confirmed online. It
	// returns the same channel on every call, and closing is one-shot.
	Online() <-chan struct{}
}

// Decision answers a PermissionRequest. Allow lets the tool call proceed;
// otherwise it is denied and Message is the reason surfaced to the agent. Input,
// when non-nil on an allow, is the (possibly rewritten) call input to run — the
// gate echoes the original request's input unchanged.
type Decision struct {
	Allow   bool
	Message string
	Input   json.RawMessage
}

// SessionSpec is the declarative description of a session to bring up — the
// "unit file" fields the adapter needs. Everything is optional except WorkDir;
// zero values mean "use the agent's default."
type SessionSpec struct {
	// WorkDir is the directory the agent runs in (typically a per-session git
	// worktree). Required.
	WorkDir string

	// Model is an agent-specific model id or alias (e.g. "opus", "claude-opus-4-8").
	Model string

	// SystemPromptAppend is appended to the agent's default system prompt.
	SystemPromptAppend string

	// PermissionMode selects how tool-use permission is handled. The values are
	// agent-specific; the supervisor's escalation seam sets this (a later
	// ticket). Empty means the agent's default.
	PermissionMode string

	// AllowedTools / DisallowedTools pre-authorize or forbid tools by name,
	// bypassing the permission callback for those. Agent-specific syntax.
	AllowedTools    []string
	DisallowedTools []string

	// Env is extra environment for the session process, as KEY=VALUE strings,
	// layered over the parent environment.
	Env []string
}

// EventKind tags a normalized Event. Consumers switch on it. The set is
// deliberately small and agent-independent; the spec's stream (assistant /
// tool-call / tool-result / usage) plus the lifecycle markers a supervisor
// needs to drive the session state machine.
type EventKind string

const (
	// EventSystem marks the session coming online; its SessionID is the id the
	// agent confirmed (which must match the one Spawn returned).
	EventSystem EventKind = "system"
	// EventAssistant carries a chunk of assistant output. Thinking blocks arrive
	// as EventAssistant with Thinking set, so a consumer that ignores reasoning
	// can filter on that flag.
	EventAssistant EventKind = "assistant"
	// EventToolCall is the agent invoking a tool: Tool holds the id, name, and
	// raw input.
	EventToolCall EventKind = "tool_call"
	// EventToolResult is the outcome of a tool call, correlated by Tool.ID; its
	// content is flattened to text in Tool.Result.
	EventToolResult EventKind = "tool_result"
	// EventUsage reports cumulative token/cost/context figures for the session.
	EventUsage EventKind = "usage"
	// EventTurnEnd marks one user turn finished; Result carries the final
	// assistant text and Turn its terminal status.
	EventTurnEnd EventKind = "turn_end"
	// EventError reports a transport, decode, or process failure. Err is the
	// message; it is not necessarily terminal (the channel closing is).
	EventError EventKind = "error"
	// EventPermission is the agent asking to use a tool its own policy will not
	// auto-approve — the tool-permission callback surfaced as a stream event.
	// Permission carries the ask; the supervisor's gate answers it (allow, or
	// escalate-and-halt) via Permissioner.Decide, correlating by Permission.ID.
	EventPermission EventKind = "permission"
)

// Event is one normalized item on a session's stream. It is a tagged union:
// Kind selects which of the embedded groups is meaningful. Raw preserves the
// original agent line so a supervisor can tee it verbatim to a journal without
// re-marshalling.
type Event struct {
	Kind      EventKind `json:"kind"`
	SessionID string    `json:"session_id,omitempty"`

	// Assistant (EventAssistant): Text is the chunk; Thinking is true for a
	// reasoning block rather than user-facing output.
	Text     string `json:"text,omitempty"`
	Thinking bool   `json:"thinking,omitempty"`

	// Tool (EventToolCall / EventToolResult).
	Tool *ToolEvent `json:"tool,omitempty"`

	// Permission (EventPermission): the tool-permission ask awaiting a Decision.
	Permission *PermissionRequest `json:"permission,omitempty"`

	// Usage (EventUsage), also attached to EventTurnEnd when the agent reports a
	// final tally.
	Usage *Usage `json:"usage,omitempty"`

	// Turn (EventTurnEnd): Result is the final assistant text; Turn is the
	// terminal status ("success", "error", "interrupted", ...).
	Result string `json:"result,omitempty"`
	Turn   string `json:"turn,omitempty"`

	// Err (EventError).
	Err string `json:"err,omitempty"`

	// Raw is the untouched agent line this Event was normalized from, for
	// journalling. Nil for events the adapter synthesizes.
	Raw json.RawMessage `json:"-"`
}

// ToolEvent describes a tool call or its result.
type ToolEvent struct {
	// ID correlates a call with its result.
	ID    string          `json:"id"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// Result and IsError are set on EventToolResult; Result is the tool output
	// flattened to text.
	Result  string `json:"result,omitempty"`
	IsError bool   `json:"is_error,omitempty"`
}

// PermissionRequest is a pending tool-permission ask, carried by an
// EventPermission. ID correlates the eventual Decision back to the agent; Tool
// is the tool name the policy keys on, and Input is the call awaiting approval.
type PermissionRequest struct {
	ID    string          `json:"id"`
	Tool  string          `json:"tool,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

// Usage is a normalized token/cost snapshot. ContextTokens is the size of the
// request that produced it (input + cache read + cache creation), i.e. how full
// the context window is — the figure the supervisor surfaces as a live gauge.
type Usage struct {
	InputTokens         int     `json:"input_tokens"`
	OutputTokens        int     `json:"output_tokens"`
	CacheReadTokens     int     `json:"cache_read_tokens"`
	CacheCreationTokens int     `json:"cache_creation_tokens"`
	ContextTokens       int     `json:"context_tokens"`
	CostUSD             float64 `json:"cost_usd"`
}
