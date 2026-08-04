package watch

import (
	"encoding/json"
	"path"
	"strconv"
	"strings"

	"github.com/Dawil/draiver/internal/agent"
)

// Promotion is a semantic event the supervisor lifts out of a session's stream
// and appends to the attempt's durable log. It mirrors the fields the draiver
// protocol verbs (log/escalate/review) carry, minus the ones ticketlog.Append
// stamps itself (seq, ts, hash, ...).
type Promotion struct {
	Type      string   // gotcha | decision | note | escalation | review | ...
	Body      string   // the markdown message
	Refs      []int    // seqs of events this one references
	Artefacts []string // paths under artefacts/ this event references
}

// Recognizer decides whether one normalized event carries a semantic signal to
// promote into the durable log. It returns ok=false for the vast majority of
// events (assistant chatter, tool results, usage frames) and a filled Promotion
// only when it is confident it can reproduce the agent's intent losslessly.
//
// The contract is deliberately conservative: a Recognizer must refuse (ok=false)
// rather than promote a body it cannot recover exactly, because a promoted event
// is written once into the hash chain and cannot be corrected in place.
type Recognizer interface {
	Recognize(ev agent.Event) (Promotion, bool)
}

// ProtocolRecognizer promotes the draiver protocol verbs the onboarding skill
// teaches an agent to run — `draiver log --type … MSG`, `draiver escalate … Q`,
// `draiver review … CLAIM` — when they surface as shell tool calls in the
// stream. Recognizing the agent's own protocol calls (rather than inventing a
// second signalling channel) keeps the supervisor the single writer of the log
// without changing what the agent is taught to do.
//
// The zero value recognizes `draiver` invoked through a tool named "Bash". Set
// the fields to match a different binary name, extra shell-tool names, or to pin
// promotions to one ticket.
type ProtocolRecognizer struct {
	// Ticket, if set, requires a recognized command to target this ticket id;
	// commands naming a different ticket are ignored. Empty accepts any ticket.
	Ticket string

	// Bin is the binary basename to match (default "draiver"). A leading path and
	// a trailing ".exe"/".static" suffix on the invoked command are tolerated.
	Bin string

	// ShellTools names the tools whose input carries a shell command line in a
	// {"command": "..."} field (default {"Bash"}). Case-insensitive.
	ShellTools []string
}

func (r ProtocolRecognizer) bin() string {
	if r.Bin != "" {
		return r.Bin
	}
	return "draiver"
}

func (r ProtocolRecognizer) isShellTool(name string) bool {
	tools := r.ShellTools
	if tools == nil {
		tools = []string{"Bash"}
	}
	for _, t := range tools {
		if strings.EqualFold(t, name) {
			return true
		}
	}
	return false
}

// Recognize implements Recognizer.
func (r ProtocolRecognizer) Recognize(ev agent.Event) (Promotion, bool) {
	if ev.Kind != agent.EventToolCall || ev.Tool == nil {
		return Promotion{}, false
	}
	if !r.isShellTool(ev.Tool.Name) {
		return Promotion{}, false
	}
	cmd, ok := shellCommand(ev.Tool.Input)
	if !ok || cmd == "" {
		return Promotion{}, false
	}
	tokens, ok := tokenize(cmd)
	if !ok || len(tokens) < 2 {
		return Promotion{}, false
	}
	// Find the draiver invocation; the first token must be the binary. We do not
	// chase pipelines or `cd … && draiver …` chains — tokenize already rejects the
	// metacharacters that would introduce them, so token[0] is the whole command.
	if !matchesBin(tokens[0], r.bin()) {
		return Promotion{}, false
	}
	return r.fromArgs(tokens[1:])
}

// fromArgs turns a draiver argv (verb + operands) into a Promotion. It parses
// only the flags the three promotable verbs accept; unknown flags are skipped
// without consuming an operand, and any structural surprise yields ok=false.
func (r ProtocolRecognizer) fromArgs(args []string) (Promotion, bool) {
	if len(args) == 0 {
		return Promotion{}, false
	}
	verb := args[0]
	rest := args[1:]

	var (
		pos  []string
		typ  string
		refs []int
		arts []string
	)
	for i := 0; i < len(rest); i++ {
		t := rest[i]
		switch {
		case t == "--type":
			if i+1 >= len(rest) {
				return Promotion{}, false
			}
			i++
			typ = rest[i]
		case strings.HasPrefix(t, "--type="):
			typ = t[len("--type="):]
		case t == "--ref":
			if i+1 >= len(rest) {
				return Promotion{}, false
			}
			i++
			n, err := strconv.Atoi(rest[i])
			if err != nil {
				return Promotion{}, false
			}
			refs = append(refs, n)
		case strings.HasPrefix(t, "--ref="):
			n, err := strconv.Atoi(t[len("--ref="):])
			if err != nil {
				return Promotion{}, false
			}
			refs = append(refs, n)
		case t == "--artefact":
			if i+1 >= len(rest) {
				return Promotion{}, false
			}
			i++
			arts = append(arts, rest[i])
		case strings.HasPrefix(t, "--artefact="):
			arts = append(arts, t[len("--artefact="):])
		case strings.HasPrefix(t, "-"):
			// An unknown flag. The promotable verbs take no other value-bearing
			// flags, so skip it without swallowing the next token.
			continue
		default:
			pos = append(pos, t)
		}
	}

	// pos[0] is the ticket for every verb; the body (if any) is pos[1].
	if len(pos) == 0 {
		return Promotion{}, false
	}
	if r.Ticket != "" && pos[0] != r.Ticket {
		return Promotion{}, false
	}

	p := Promotion{Refs: refs, Artefacts: arts}
	switch verb {
	case "log":
		if typ == "" || len(pos) < 2 {
			return Promotion{}, false
		}
		p.Type = typ
		p.Body = pos[1]
	case "escalate":
		if len(pos) < 2 {
			return Promotion{}, false
		}
		p.Type = "escalation"
		p.Body = pos[1]
	case "review":
		p.Type = "review"
		if len(pos) >= 2 {
			p.Body = pos[1]
		} else {
			// Mirror the CLI's default claim so a supervisor-promoted review reads
			// the same as a human-invoked one.
			p.Body = "Agent claims the ticket is complete; ready for review."
		}
	default:
		return Promotion{}, false
	}
	return p, true
}

// shellCommand pulls the command line out of a shell tool's raw input, which is
// a JSON object with a "command" string. A missing/blank command yields ok=false.
func shellCommand(input json.RawMessage) (string, bool) {
	if len(input) == 0 {
		return "", false
	}
	var obj struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(input, &obj); err != nil {
		return "", false
	}
	return obj.Command, obj.Command != ""
}

// matchesBin reports whether an invoked command token names the draiver binary.
// It tolerates a leading path (/usr/local/bin/draiver) and the repo's built
// variants (draiver-static). It does not tolerate a different basename.
func matchesBin(tok, bin string) bool {
	base := path.Base(tok)
	if base == bin {
		return true
	}
	// draiver-static, draiver.exe, etc.: match the binary name up to a separator.
	if strings.HasPrefix(base, bin) {
		switch base[len(bin)] {
		case '-', '.', '_':
			return true
		}
	}
	return false
}

// tokenize splits a shell command line into argv, honoring single quotes
// (literal), double quotes (literal here — we intentionally do not expand $),
// and backslash escapes outside quotes. It returns ok=false when it meets any
// construct that could make the visible argv differ from what the shell would
// actually run — command substitution ($( ) or backticks), a heredoc (<<),
// parameter expansion outside quotes, pipes/redirects/lists, or an unbalanced
// quote. Refusing these is the point: a body we cannot reproduce exactly must
// not be promoted.
func tokenize(s string) ([]string, bool) {
	// Fast reject of the multi-word danger constructs anywhere in the line.
	if strings.Contains(s, "$(") || strings.Contains(s, "`") || strings.Contains(s, "<<") {
		return nil, false
	}

	var (
		tokens []string
		cur    strings.Builder
		inTok  bool
	)
	flush := func() {
		if inTok {
			tokens = append(tokens, cur.String())
			cur.Reset()
			inTok = false
		}
	}

	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch c {
		case ' ', '\t':
			flush()
		case '\n', '\r':
			// A newline means a second command line; we only trust single commands.
			return nil, false
		case '|', '&', ';', '<', '>', '(', ')':
			// Pipelines, lists, subshells, redirects: the argv is no longer just
			// this command. Refuse.
			return nil, false
		case '\'':
			inTok = true
			for i++; i < len(runes) && runes[i] != '\''; i++ {
				cur.WriteRune(runes[i])
			}
			if i >= len(runes) {
				return nil, false // unbalanced single quote
			}
		case '"':
			inTok = true
			for i++; i < len(runes) && runes[i] != '"'; i++ {
				if runes[i] == '\\' && i+1 < len(runes) {
					n := runes[i+1]
					// In double quotes the shell only unescapes these four.
					if n == '"' || n == '\\' || n == '$' || n == '`' {
						cur.WriteRune(n)
						i++
						continue
					}
				}
				if runes[i] == '$' || runes[i] == '`' {
					return nil, false // expansion inside double quotes
				}
				cur.WriteRune(runes[i])
			}
			if i >= len(runes) {
				return nil, false // unbalanced double quote
			}
		case '\\':
			if i+1 < len(runes) {
				i++
				cur.WriteRune(runes[i])
				inTok = true
			}
		case '$':
			return nil, false // bare parameter/command expansion
		default:
			inTok = true
			cur.WriteRune(c)
		}
	}
	flush()
	return tokens, true
}
