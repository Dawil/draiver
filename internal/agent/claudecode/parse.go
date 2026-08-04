package claudecode

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/Dawil/draiver/internal/agent"
)

// The types below mirror only the fields of Claude Code's stream-json we
// normalize. They are unexported on purpose: no Claude-specific shape escapes
// this package. Unknown message types and unknown content blocks are ignored,
// so a newer CLI adding fields or event kinds never breaks the adapter.

// wireEnvelope is the outer shape common to every stream-json line.
type wireEnvelope struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	SessionID string          `json:"session_id"`
	Message   json.RawMessage `json:"message"`
}

// wireMessage is the Anthropic message inside an assistant/user line.
type wireMessage struct {
	Role    string        `json:"role"`
	Content []wireContent `json:"content"`
	Usage   *wireUsage    `json:"usage"`
}

// wireContent is one content block. Fields are a superset across block types;
// only those matching Type are populated.
type wireContent struct {
	Type string `json:"type"`

	// text / thinking
	Text     string `json:"text"`
	Thinking string `json:"thinking"`

	// tool_use
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`

	// tool_result
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"` // string OR array of blocks
}

type wireUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// wireResult is the terminal "result" line closing one turn.
type wireResult struct {
	Subtype      string     `json:"subtype"`
	IsError      bool       `json:"is_error"`
	Result       string     `json:"result"`
	TotalCostUSD float64    `json:"total_cost_usd"`
	SessionID    string     `json:"session_id"`
	Usage        *wireUsage `json:"usage"`
}

// normalize decodes one stream-json line into zero or more normalized events.
// A single assistant line fans out into one event per content block; lines the
// adapter does not model (thinking_tokens ticks, rate_limit_event, control
// frames) yield nothing. A line that is not valid JSON yields one EventError.
func normalize(line []byte) []agent.Event {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return nil
	}
	raw := json.RawMessage(append([]byte(nil), line...))

	var env wireEnvelope
	if err := json.Unmarshal(line, &env); err != nil {
		return []agent.Event{{Kind: agent.EventError, Err: "decode stream-json: " + err.Error(), Raw: raw}}
	}

	switch env.Type {
	case "system":
		// The init frame marks the session online; other system subtypes
		// (thinking_tokens, ...) are progress noise we drop.
		if env.Subtype == "init" {
			return []agent.Event{{Kind: agent.EventSystem, SessionID: env.SessionID, Raw: raw}}
		}
		return nil

	case "assistant", "user":
		return normalizeMessage(env, raw)

	case "result":
		return normalizeResult(line, raw)

	default:
		// rate_limit_event, control_response, and anything future: not modeled.
		return nil
	}
}

func normalizeMessage(env wireEnvelope, raw json.RawMessage) []agent.Event {
	var msg wireMessage
	if err := json.Unmarshal(env.Message, &msg); err != nil {
		return []agent.Event{{Kind: agent.EventError, Err: "decode message: " + err.Error(), Raw: raw}}
	}
	var out []agent.Event
	for _, c := range msg.Content {
		switch c.Type {
		case "text":
			if c.Text == "" {
				continue
			}
			out = append(out, agent.Event{Kind: agent.EventAssistant, SessionID: env.SessionID, Text: c.Text, Raw: raw})
		case "thinking":
			if c.Thinking == "" {
				continue
			}
			out = append(out, agent.Event{Kind: agent.EventAssistant, SessionID: env.SessionID, Text: c.Thinking, Thinking: true, Raw: raw})
		case "tool_use":
			out = append(out, agent.Event{
				Kind: agent.EventToolCall, SessionID: env.SessionID, Raw: raw,
				Tool: &agent.ToolEvent{ID: c.ID, Name: c.Name, Input: c.Input},
			})
		case "tool_result":
			out = append(out, agent.Event{
				Kind: agent.EventToolResult, SessionID: env.SessionID, Raw: raw,
				Tool: &agent.ToolEvent{ID: c.ToolUseID, Result: flattenToolContent(c.Content), IsError: c.IsError},
			})
		}
	}
	// Attach a usage event when the message reports one, so the supervisor's
	// context/cost gauge advances per assistant message, not only per turn.
	if msg.Usage != nil {
		out = append(out, usageEvent(env.SessionID, msg.Usage, 0, raw))
	}
	return out
}

func normalizeResult(line []byte, raw json.RawMessage) []agent.Event {
	var res wireResult
	if err := json.Unmarshal(line, &res); err != nil {
		return []agent.Event{{Kind: agent.EventError, Err: "decode result: " + err.Error(), Raw: raw}}
	}
	turn := res.Subtype
	if turn == "" {
		if res.IsError {
			turn = "error"
		} else {
			turn = "success"
		}
	}
	ev := agent.Event{Kind: agent.EventTurnEnd, SessionID: res.SessionID, Result: res.Result, Turn: turn, Raw: raw}
	if res.Usage != nil {
		u := toUsage(res.Usage, res.TotalCostUSD)
		ev.Usage = &u
	} else if res.TotalCostUSD != 0 {
		ev.Usage = &agent.Usage{CostUSD: res.TotalCostUSD}
	}
	return []agent.Event{ev}
}

func usageEvent(sessionID string, u *wireUsage, cost float64, raw json.RawMessage) agent.Event {
	usage := toUsage(u, cost)
	return agent.Event{Kind: agent.EventUsage, SessionID: sessionID, Usage: &usage, Raw: raw}
}

func toUsage(u *wireUsage, cost float64) agent.Usage {
	return agent.Usage{
		InputTokens:         u.InputTokens,
		OutputTokens:        u.OutputTokens,
		CacheReadTokens:     u.CacheReadInputTokens,
		CacheCreationTokens: u.CacheCreationInputTokens,
		ContextTokens:       u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens,
		CostUSD:             cost,
	}
}

// flattenToolContent reduces a tool_result's content — which is either a JSON
// string or an array of content blocks — to plain text. Anything it cannot read
// as text is dropped rather than erroring, since tool output is advisory.
func flattenToolContent(c json.RawMessage) string {
	c = bytes.TrimSpace(c)
	if len(c) == 0 || string(c) == "null" {
		return ""
	}
	// string form
	if c[0] == '"' {
		var s string
		if json.Unmarshal(c, &s) == nil {
			return s
		}
		return ""
	}
	// array-of-blocks form
	if c[0] == '[' {
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(c, &blocks) == nil {
			var b strings.Builder
			for _, blk := range blocks {
				if blk.Type == "text" {
					b.WriteString(blk.Text)
				}
			}
			return b.String()
		}
	}
	return ""
}
