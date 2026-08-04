package gate

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

// ReadOnly is the built-in base policy: auto-approve the tools that only observe
// — they cannot mutate the repo or reach outside it — and escalate everything
// else (edits, shell, network, and any tool not listed). It is the sensible
// global layer to build project/ticket overrides on top of via Layer.
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
