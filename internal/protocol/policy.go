package protocol

// Requirement is a per-tool verdict in a Policy: whether a tool may run freely
// or must be justified by a preceding log event.
type Requirement string

const (
	// Free lets a tool run with no preceding log event — reads, and the shell
	// itself (the agent logs via `draiver log`, which runs through Bash, so
	// gating Bash on a prior log would deadlock).
	Free Requirement = "free"
	// Justified withholds a tool until a decision/gotcha justifying it is on disk
	// — the structured edit tools whose changes must be explained first.
	Justified Requirement = "justified"
)

// Policy decides, per tool name, whether a tool runs freely or must be preceded
// by a justifying log event. Tools holds explicit per-tool rules; Default applies
// to any tool not listed. The zero Policy leaves everything Free — the protocol
// gate only withholds tools it is explicitly told require a rationale, so an
// unconfigured gate never blocks.
type Policy struct {
	Default Requirement
	Tools   map[string]Requirement
}

// Requires reports whether a tool must be justified by a preceding log event: its
// explicit Tools entry, else Default, else Free (the fail-open default — an
// unclassified tool is not withheld).
func (p Policy) Requires(tool string) bool {
	if r, ok := p.Tools[tool]; ok && r != "" {
		return r == Justified
	}
	return p.Default == Justified
}

// Layer flattens policies from lowest to highest precedence — the layered config
// the design calls for: global -> project -> ticket. A later layer's Default
// (when set) replaces earlier ones, and its per-tool rules override earlier
// entries for the same tool; tools only an earlier layer named are preserved. The
// result is a single Policy with no aliasing of the inputs' maps.
func Layer(layers ...Policy) Policy {
	out := Policy{Tools: map[string]Requirement{}}
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

// Edits is the built-in base policy: withhold the structured edit tools until a
// decision/gotcha justifies them, and leave everything else Free. Bash is
// deliberately Free — it is how the agent runs `draiver log`, and it stays gated
// to a human by the permission gate (gate.ReadOnly) instead. It is the sensible
// global layer to build project/ticket overrides on top of via Layer.
func Edits() Policy {
	return Policy{
		Default: Free,
		Tools: map[string]Requirement{
			"Edit":         Justified,
			"Write":        Justified,
			"NotebookEdit": Justified,
		},
	}
}
