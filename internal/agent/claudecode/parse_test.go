package claudecode

import (
	"testing"

	"github.com/Dawil/draiver/internal/agent"
)

func TestNormalize_SingleLines(t *testing.T) {
	tests := []struct {
		name string
		line string
		want []agent.Event
	}{
		{
			name: "init frame becomes system event with session id",
			line: `{"type":"system","subtype":"init","session_id":"sess-1","cwd":"/x","model":"opus"}`,
			want: []agent.Event{{Kind: agent.EventSystem, SessionID: "sess-1"}},
		},
		{
			name: "thinking_tokens system subtype is dropped",
			line: `{"type":"system","subtype":"thinking_tokens","estimated_tokens":9,"session_id":"sess-1"}`,
			want: nil,
		},
		{
			name: "rate_limit_event is dropped",
			line: `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"},"session_id":"sess-1"}`,
			want: nil,
		},
		{
			name: "assistant text",
			line: `{"type":"assistant","session_id":"sess-1","message":{"role":"assistant","content":[{"type":"text","text":"OK"}]}}`,
			want: []agent.Event{{Kind: agent.EventAssistant, SessionID: "sess-1", Text: "OK"}},
		},
		{
			name: "assistant thinking sets the flag",
			line: `{"type":"assistant","session_id":"sess-1","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hmm"}]}}`,
			want: []agent.Event{{Kind: agent.EventAssistant, SessionID: "sess-1", Text: "hmm", Thinking: true}},
		},
		{
			name: "empty text block is skipped",
			line: `{"type":"assistant","session_id":"sess-1","message":{"role":"assistant","content":[{"type":"text","text":""}]}}`,
			want: nil,
		},
		{
			name: "tool_use becomes a tool_call carrying raw input",
			line: `{"type":"assistant","session_id":"sess-1","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"echo hi"}}]}}`,
			want: []agent.Event{{
				Kind: agent.EventToolCall, SessionID: "sess-1",
				Tool: &agent.ToolEvent{ID: "toolu_1", Name: "Bash", Input: []byte(`{"command":"echo hi"}`)},
			}},
		},
		{
			name: "tool_result string content flattens to text",
			line: `{"type":"user","session_id":"sess-1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","is_error":false,"content":"hi"}]}}`,
			want: []agent.Event{{
				Kind: agent.EventToolResult, SessionID: "sess-1",
				Tool: &agent.ToolEvent{ID: "toolu_1", Result: "hi"},
			}},
		},
		{
			name: "tool_result array content concatenates text blocks",
			line: `{"type":"user","session_id":"sess-1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_2","is_error":true,"content":[{"type":"text","text":"line1\n"},{"type":"text","text":"line2"}]}]}}`,
			want: []agent.Event{{
				Kind: agent.EventToolResult, SessionID: "sess-1",
				Tool: &agent.ToolEvent{ID: "toolu_2", Result: "line1\nline2", IsError: true},
			}},
		},
		{
			name: "result closes the turn with status and cost",
			line: `{"type":"result","subtype":"success","is_error":false,"result":"DONE.","total_cost_usd":0.01,"session_id":"sess-1"}`,
			want: []agent.Event{{
				Kind: agent.EventTurnEnd, SessionID: "sess-1", Result: "DONE.", Turn: "success",
				Usage: &agent.Usage{CostUSD: 0.01},
			}},
		},
		{
			name: "malformed json becomes one error event",
			line: `{not json`,
			want: []agent.Event{{Kind: agent.EventError, Err: "decode stream-json: invalid character 'n' looking for beginning of object key string"}},
		},
		{
			name: "blank line yields nothing",
			line: "   ",
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalize([]byte(tt.line))
			assertEvents(t, got, tt.want)
		})
	}
}

func TestNormalize_AssistantWithUsage(t *testing.T) {
	line := `{"type":"assistant","session_id":"s","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":10,"output_tokens":3,"cache_read_input_tokens":100,"cache_creation_input_tokens":50}}}`
	got := normalize([]byte(line))
	if len(got) != 2 {
		t.Fatalf("want text + usage events, got %d: %+v", len(got), got)
	}
	if got[0].Kind != agent.EventAssistant || got[0].Text != "hi" {
		t.Errorf("first event = %+v, want assistant text", got[0])
	}
	u := got[1].Usage
	if got[1].Kind != agent.EventUsage || u == nil {
		t.Fatalf("second event = %+v, want usage", got[1])
	}
	if u.InputTokens != 10 || u.OutputTokens != 3 || u.CacheReadTokens != 100 || u.CacheCreationTokens != 50 {
		t.Errorf("usage tokens = %+v", u)
	}
	if want := 10 + 100 + 50; u.ContextTokens != want {
		t.Errorf("ContextTokens = %d, want %d", u.ContextTokens, want)
	}
}

// The result line's usage is a cumulative turn total, not a single request's
// prompt, so its ContextTokens must be zeroed — otherwise a long turn's summed
// cache_read (millions of tokens) masquerades as context-window fill (the
// drvctl-009 "2M context" artifact). Cost still flows through.
func TestNormalize_ResultUsageIsNotAContextSnapshot(t *testing.T) {
	line := `{"type":"result","subtype":"success","session_id":"s","total_cost_usd":3.33,"usage":{"input_tokens":43,"output_tokens":46321,"cache_read_input_tokens":1931206,"cache_creation_input_tokens":120403}}`
	got := normalize([]byte(line))
	if len(got) != 1 || got[0].Kind != agent.EventTurnEnd || got[0].Usage == nil {
		t.Fatalf("want one turn-end with usage, got %+v", got)
	}
	u := got[0].Usage
	if u.ContextTokens != 0 {
		t.Errorf("ContextTokens = %d, want 0 (cumulative result usage is not a context snapshot)", u.ContextTokens)
	}
	if u.CostUSD != 3.33 {
		t.Errorf("CostUSD = %v, want 3.33 (cost still flows from the result line)", u.CostUSD)
	}
}

func TestNormalize_CanUseToolBecomesPermission(t *testing.T) {
	line := `{"type":"control_request","request_id":"req-1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"rm -rf /"}}}`
	got := normalize([]byte(line))
	if len(got) != 1 {
		t.Fatalf("want 1 permission event, got %d: %+v", len(got), got)
	}
	ev := got[0]
	if ev.Kind != agent.EventPermission {
		t.Fatalf("kind = %q, want permission", ev.Kind)
	}
	if ev.Permission == nil {
		t.Fatal("permission payload is nil")
	}
	if ev.Permission.ID != "req-1" || ev.Permission.Tool != "Bash" {
		t.Errorf("permission = %+v, want id req-1 tool Bash", ev.Permission)
	}
	if string(ev.Permission.Input) != `{"command":"rm -rf /"}` {
		t.Errorf("permission input = %s", ev.Permission.Input)
	}
	if len(ev.Raw) == 0 {
		t.Error("permission event dropped its raw line")
	}
}

func TestNormalize_OtherControlRequestsDropped(t *testing.T) {
	// A control_request the adapter does not model (not can_use_tool) yields
	// nothing, like other unmodeled frames.
	line := `{"type":"control_request","request_id":"r2","request":{"subtype":"mcp_message","server":"x"}}`
	if got := normalize([]byte(line)); got != nil {
		t.Fatalf("want nil for an unmodeled control_request, got %+v", got)
	}
}

// assertEvents compares normalized events field-by-field, ignoring Raw (which
// is just the untouched input line).
func assertEvents(t *testing.T, got, want []agent.Event) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d\n got=%+v\nwant=%+v", len(got), len(want), got, want)
	}
	for i := range want {
		g, w := got[i], want[i]
		g.Raw = nil
		if g.Kind != w.Kind || g.SessionID != w.SessionID || g.Text != w.Text ||
			g.Thinking != w.Thinking || g.Result != w.Result || g.Turn != w.Turn || g.Err != w.Err {
			t.Errorf("event[%d] scalar mismatch:\n got=%+v\nwant=%+v", i, g, w)
		}
		assertTool(t, i, g.Tool, w.Tool)
		assertUsage(t, i, g.Usage, w.Usage)
	}
}

func assertTool(t *testing.T, i int, got, want *agent.ToolEvent) {
	t.Helper()
	if (got == nil) != (want == nil) {
		t.Errorf("event[%d] tool presence: got=%v want=%v", i, got, want)
		return
	}
	if want == nil {
		return
	}
	if got.ID != want.ID || got.Name != want.Name || got.Result != want.Result || got.IsError != want.IsError {
		t.Errorf("event[%d] tool = %+v, want %+v", i, got, want)
	}
	if string(got.Input) != string(want.Input) {
		t.Errorf("event[%d] tool input = %s, want %s", i, got.Input, want.Input)
	}
}

func assertUsage(t *testing.T, i int, got, want *agent.Usage) {
	t.Helper()
	if (got == nil) != (want == nil) {
		t.Errorf("event[%d] usage presence: got=%v want=%v", i, got, want)
		return
	}
	if want == nil {
		return
	}
	if *got != *want {
		t.Errorf("event[%d] usage = %+v, want %+v", i, *got, *want)
	}
}
