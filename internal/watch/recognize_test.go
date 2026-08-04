package watch

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Dawil/draiver/internal/agent"
)

// bashCall builds an EventToolCall the way the claudecode adapter normalizes a
// Bash tool_use: tool name "Bash", input {"command": cmd}.
func bashCall(cmd string) agent.Event {
	in, _ := json.Marshal(map[string]string{"command": cmd})
	return agent.Event{
		Kind: agent.EventToolCall,
		Tool: &agent.ToolEvent{ID: "t1", Name: "Bash", Input: in},
	}
}

func TestProtocolRecognizer_Promotes(t *testing.T) {
	r := ProtocolRecognizer{Ticket: "PROJ-1"}
	cases := []struct {
		name string
		cmd  string
		want Promotion
	}{
		{
			name: "decision via --type",
			cmd:  `draiver log PROJ-1 "Chose server-side pagination" --type decision`,
			want: Promotion{Type: "decision", Body: "Chose server-side pagination"},
		},
		{
			name: "gotcha with --type before body",
			cmd:  `draiver log --type gotcha PROJ-1 "Stripe test keys 401 in live mode"`,
			want: Promotion{Type: "gotcha", Body: "Stripe test keys 401 in live mode"},
		},
		{
			name: "type= joined form",
			cmd:  `draiver log PROJ-1 note-body --type=note`,
			want: Promotion{Type: "note", Body: "note-body"},
		},
		{
			name: "escalate maps to escalation",
			cmd:  `draiver escalate PROJ-1 "Which prod DB URL?"`,
			want: Promotion{Type: "escalation", Body: "Which prod DB URL?"},
		},
		{
			name: "review with claim",
			cmd:  `draiver review PROJ-1 "PR #142 green"`,
			want: Promotion{Type: "review", Body: "PR #142 green"},
		},
		{
			name: "review without claim uses default",
			cmd:  `draiver review PROJ-1`,
			want: Promotion{Type: "review", Body: "Agent claims the ticket is complete; ready for review."},
		},
		{
			name: "refs and artefacts",
			cmd:  `draiver log PROJ-1 "see fail" --type decision --ref 3 --ref=5 --artefact logs/x.txt`,
			want: Promotion{Type: "decision", Body: "see fail", Refs: []int{3, 5}, Artefacts: []string{"logs/x.txt"}},
		},
		{
			name: "absolute path binary",
			cmd:  `/usr/local/bin/draiver log PROJ-1 body --type note`,
			want: Promotion{Type: "note", Body: "body"},
		},
		{
			name: "draiver-static variant",
			cmd:  `draiver-static log PROJ-1 body --type note`,
			want: Promotion{Type: "note", Body: "body"},
		},
		{
			name: "single-quoted body",
			cmd:  `draiver log PROJ-1 'it''s literal' --type note`,
			want: Promotion{Type: "note", Body: "its literal"}, // '' closes+reopens, no space
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := r.Recognize(bashCall(tc.cmd))
			if !ok {
				t.Fatalf("expected promotion, got none for %q", tc.cmd)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("promotion mismatch\n cmd:  %s\n got:  %+v\n want: %+v", tc.cmd, got, tc.want)
			}
		})
	}
}

func TestProtocolRecognizer_Refuses(t *testing.T) {
	r := ProtocolRecognizer{Ticket: "PROJ-1"}
	cases := []struct {
		name string
		ev   agent.Event
	}{
		{"not a tool call", agent.Event{Kind: agent.EventAssistant, Text: "draiver log PROJ-1 x --type note"}},
		{"non-shell tool", agent.Event{Kind: agent.EventToolCall, Tool: &agent.ToolEvent{Name: "Read", Input: json.RawMessage(`{"file_path":"x"}`)}}},
		{"tool result not call", agent.Event{Kind: agent.EventToolResult, Tool: &agent.ToolEvent{Name: "Bash", Result: "ok"}}},
		{"not draiver", bashCall(`git log PROJ-1 x --type note`)},
		{"command substitution body", bashCall(`draiver log PROJ-1 "$(cat <<'MD'` + "\n" + `hi` + "\n" + `MD)" --type decision`)},
		{"heredoc", bashCall("draiver log PROJ-1 \"$(cat <<'MD')\" --type note")},
		{"backtick", bashCall("draiver log PROJ-1 `whoami` --type note")},
		{"piped", bashCall(`draiver log PROJ-1 x --type note | tee out`)},
		{"chained with cd", bashCall(`cd /repo && draiver log PROJ-1 x --type note`)},
		{"redirect", bashCall(`draiver log PROJ-1 x --type note > out.txt`)},
		{"dollar expansion in double quotes", bashCall(`draiver log PROJ-1 "$HOME" --type note`)},
		{"bare dollar", bashCall(`draiver log PROJ-1 $x --type note`)},
		{"log missing --type", bashCall(`draiver log PROJ-1 "body only"`)},
		{"log missing body", bashCall(`draiver log PROJ-1 --type note`)},
		{"escalate missing question", bashCall(`draiver escalate PROJ-1`)},
		{"wrong ticket", bashCall(`draiver log OTHER-9 body --type note`)},
		{"unknown verb", bashCall(`draiver status PROJ-1`)},
		{"unbalanced quote", bashCall(`draiver log PROJ-1 "oops --type note`)},
		{"empty command", bashCall(``)},
		{"--type at end no value", bashCall(`draiver log PROJ-1 body --type`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if p, ok := r.Recognize(tc.ev); ok {
				t.Fatalf("expected refusal, got promotion %+v", p)
			}
		})
	}
}

func TestProtocolRecognizer_AnyTicketWhenUnset(t *testing.T) {
	r := ProtocolRecognizer{} // no Ticket pin
	got, ok := r.Recognize(bashCall(`draiver log ANY-42 body --type note`))
	if !ok {
		t.Fatal("expected promotion with no ticket pin")
	}
	if got.Body != "body" || got.Type != "note" {
		t.Fatalf("unexpected promotion %+v", got)
	}
}

func TestTokenize(t *testing.T) {
	ok := func(in string, want ...string) {
		t.Helper()
		got, ok := tokenize(in)
		if !ok {
			t.Fatalf("tokenize(%q) refused, wanted %v", in, want)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("tokenize(%q) = %v, want %v", in, got, want)
		}
	}
	refuse := func(in string) {
		t.Helper()
		if got, ok := tokenize(in); ok {
			t.Fatalf("tokenize(%q) = %v, wanted refusal", in, got)
		}
	}

	ok(`a b c`, "a", "b", "c")
	ok(`a "b c" d`, "a", "b c", "d")
	ok(`a 'b c' d`, "a", "b c", "d")
	ok(`a b\ c`, "a", "b c")
	ok(`"he said \"hi\""`, `he said "hi"`)
	ok(`x   y`, "x", "y") // collapse runs of spaces

	refuse(`a $(b)`)
	refuse("a `b`")
	refuse(`a << EOF`)
	refuse(`a | b`)
	refuse(`a "unbalanced`)
	refuse("a\nb")
}
