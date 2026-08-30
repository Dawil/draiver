package project

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Dawil/draiver/internal/event"
)

// Supervision is the coordinator supervision mode (drvctl-042) — "how much the
// coordinator does *before* a human." It is the dial on drvctl-041's
// reverse-`wants:` activation: on a sub-ticket escalation, the mode of the
// Capability that `wants:` it decides whether its coordinator wakes at all.
//
// Like `enable`/`disable`, the mode is a *mutable* control setting: a global/project
// default lives in operator config and a per-ticket override is a `supervision` log
// event (mutable declaration → log, not frontmatter). The two shipped modes are the
// floor and the incremental step; a third, `auto-execute`, is deliberately deferred
// (docs/capabilities-and-supervision.md §The supervision dial) and is not a value
// this type accepts yet.
type Supervision string

const (
	// SupervisionDefault is the zero value: no per-ticket `supervision` event has
	// been recorded, so the effective mode falls back to the configured default (see
	// Effective). It is never a mode the dial acts on directly.
	SupervisionDefault Supervision = ""

	// SupervisionPassthrough is the floor: a sub-ticket escalation goes straight to
	// Needs-me and no coordinator wakes — the Capability stays a dormant Pending
	// shell that wakes only for its own final review. Works with today's primitives
	// plus the DAG; the safe default.
	SupervisionPassthrough Supervision = "passthrough"

	// SupervisionPreDigest is the first step into agentic supervision: a sub-ticket
	// escalation wakes the coordinator (drvctl-041 reverse-`wants:` activation), which
	// assesses across children and posts one consolidated escalate-and-recommend for a
	// human to ratify. Proposer, never executor.
	SupervisionPreDigest Supervision = "pre-digest"
)

// ParseSupervision validates a mode string into a concrete Supervision, the seam
// the CLI verb and the config-default reader share so a bad value is rejected once,
// at the edge, rather than silently degrading the dial. It accepts only the two
// shipped modes; the deferred `auto-execute` (docs §The supervision dial) is named
// explicitly in the error so an operator reaching for it is told it is not built
// yet rather than getting an opaque "unknown mode".
func ParseSupervision(s string) (Supervision, error) {
	switch strings.TrimSpace(s) {
	case string(SupervisionPassthrough):
		return SupervisionPassthrough, nil
	case string(SupervisionPreDigest):
		return SupervisionPreDigest, nil
	case "auto-execute":
		return "", fmt.Errorf("supervision mode %q is deferred (not yet built); use %q or %q", s, SupervisionPassthrough, SupervisionPreDigest)
	default:
		return "", fmt.Errorf("unknown supervision mode %q; want %q or %q", s, SupervisionPassthrough, SupervisionPreDigest)
	}
}

// DeriveSupervision reports an attempt's per-ticket supervision override from its
// log, or SupervisionDefault when none is set — the last `supervision` event wins,
// exactly like DeriveEnabled's enable/disable. The mode rides on the event's Outcome
// field (the multi-flavour-event mechanism `archive` uses). An event whose Outcome
// does not parse to a known mode is skipped defensively, so a hand-edited or
// future-mode marker cannot flip a valid override to an unusable value; the CLI
// validates on write, so a well-formed log never exercises that skip.
func DeriveSupervision(events []event.Event) Supervision {
	mode := SupervisionDefault
	for _, e := range events {
		if e.Type != "supervision" {
			continue
		}
		if m, err := ParseSupervision(e.Outcome); err == nil {
			mode = m
		}
	}
	return mode
}

// Effective resolves the mode the dial acts on: the per-ticket override when it is a
// concrete mode, else the configured default, else the passthrough floor. It is a
// total function — it always returns a concrete mode — so the reconciler's wake gate
// never has to special-case an unset value: an attempt with no override under an
// unconfigured daemon reads as passthrough, the safe floor.
func Effective(override, def Supervision) Supervision {
	if override == SupervisionPassthrough || override == SupervisionPreDigest {
		return override
	}
	if def == SupervisionPassthrough || def == SupervisionPreDigest {
		return def
	}
	return SupervisionPassthrough
}

// SupervisionModes lists the concrete modes the dial accepts, in floor→forward
// order, for a CLI usage string. Kept alongside ParseSupervision so the two cannot
// drift.
func SupervisionModes() []string {
	m := []string{string(SupervisionPassthrough), string(SupervisionPreDigest)}
	sort.Strings(m)
	return m
}
