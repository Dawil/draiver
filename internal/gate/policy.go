package gate

import "fmt"

// Rule is a per-tool verdict in a Policy.
type Rule string

const (
	// Allow auto-approves a tool: the gate answers the agent and the session
	// keeps working, no human involved.
	Allow Rule = "allow"
	// Escalate routes a tool to a human: the gate records an escalation and halts
	// the session.
	Escalate Rule = "escalate"
)

// Policy decides, per tool name, whether a permission request auto-approves or
// escalates. Tools holds explicit per-tool rules; Default applies to any tool
// not listed. The zero Policy escalates everything — the safe default is that
// nothing auto-approves.
type Policy struct {
	Default Rule
	Tools   map[string]Rule
}

// Decide returns the rule for a tool: its explicit Tools entry, else Default,
// else Escalate (the fail-safe when neither is set).
func (p Policy) Decide(tool string) Rule {
	if r, ok := p.Tools[tool]; ok && r != "" {
		return r
	}
	if p.Default != "" {
		return p.Default
	}
	return Escalate
}

// Layer flattens policies from lowest to highest precedence — the layered config
// the design calls for: global → project → ticket. A later layer's Default (when
// set) replaces earlier ones, and its per-tool rules override earlier entries
// for the same tool; tools only an earlier layer named are preserved. The result
// is a single Policy with no aliasing of the inputs' maps.
func Layer(layers ...Policy) Policy {
	out := Policy{Tools: map[string]Rule{}}
	for _, l := range layers {
		if l.Default != "" {
			out.Default = l.Default
		}
		for tool, rule := range l.Tools {
			if rule != "" {
				out.Tools[tool] = rule
			}
		}
	}
	return out
}

// ReadOnly is a conservative base policy: auto-approve the tools that only
// observe — they cannot mutate the repo or reach outside it — and escalate
// everything else (edits, shell, network, and any tool not listed). Useful where
// a human should wave through anything with side effects.
//
// Note this escalates Bash, which the Claude Code adapter uses for read-only
// exploration too, so a ReadOnly gate halts a session on its first shell command.
// It is the strict end of the spectrum; the local auto-mode default is AllowAll.
func ReadOnly() Policy {
	return Policy{
		Default: Escalate,
		Tools: map[string]Rule{
			"Read":         Allow,
			"Glob":         Allow,
			"Grep":         Allow,
			"LS":           Allow,
			"NotebookRead": Allow,
			"TodoWrite":    Allow,
		},
	}
}

// AllowAll is the auto-mode base policy: auto-approve every tool. It makes the
// permission gate a pass-through approver — the seam still sees, logs, and meters
// each request, and a higher Layer can flip individual tools to Escalate — but
// nothing halts for a human out of the box. This is the sensible global layer for
// the current posture (running locally on human-enabled tickets, drvctl-009),
// where security is the enable gate + the sandbox boundary, not per-tool prompts.
func AllowAll() Policy {
	return Policy{Default: Allow}
}

// ParseRule validates a rule string from operator config (config.json /
// --permission), returning an error naming the accepted values rather than
// silently defaulting an unrecognized rule.
func ParseRule(s string) (Rule, error) {
	switch Rule(s) {
	case Allow, Escalate:
		return Rule(s), nil
	default:
		return "", fmt.Errorf("unknown permission rule %q (want %q or %q)", s, Allow, Escalate)
	}
}

// PolicyFromMap builds a Policy from operator-supplied strings: an optional
// default rule (empty leaves Default unset, so a lower Layer's default shows
// through) and a per-tool rule map. Every rule string is validated via ParseRule;
// the first invalid one is returned as an error naming the offending tool.
func PolicyFromMap(def string, tools map[string]string) (Policy, error) {
	p := Policy{Tools: map[string]Rule{}}
	if def != "" {
		r, err := ParseRule(def)
		if err != nil {
			return Policy{}, fmt.Errorf("permissions default: %w", err)
		}
		p.Default = r
	}
	for tool, s := range tools {
		r, err := ParseRule(s)
		if err != nil {
			return Policy{}, fmt.Errorf("permissions[%q]: %w", tool, err)
		}
		p.Tools[tool] = r
	}
	return p, nil
}
