// Package handbook holds draiver's invariant working protocol — the "how to work
// a ticket" guidance every supervised session needs (cold-start from the brief,
// log gotchas and decisions, escalate then stop, claim review). It is the content
// side of rung E in docs/prompt-caching.md: the protocol is identical across every
// ticket, so it belongs *above the wall* in the system prompt (carried by the
// coding-agent's --append-system-prompt), where it caches once per repo instead of
// paying a cold write on every ticket's cold-start via the conversation layer.
//
// The text is embedded from handbook.md, which is the onboarding SKILL.md body
// (frontmatter stripped) — the two are kept byte-identical by a drift test, so the
// skill (interactive/manual delivery) and this append (the supervised, cacheable
// delivery) never diverge. The distinct enforcement gate that turns the protocol's
// log discipline into process control lives in internal/protocol; this package is
// only the words.
//
// The one hard constraint is byte-invariance: the append must carry no ticket id,
// timestamp, or per-ticket datum, or the shared prefix re-fragments across tickets
// (docs/prompt-caching.md, "the append must be byte-invariant"). handbook.md holds
// only literal <TICKET>-style placeholders, so Content() is a daemon-lifetime
// constant; anything per-ticket stays in the brief.
package handbook

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

//go:embed handbook.md
var content string

// Content returns the invariant protocol text — the bytes passed as the
// coding-agent's --append-system-prompt. It is a compile-time constant: identical
// across every attempt, so the appended system-prompt prefix is shared and
// cacheable across tickets on a repo.
func Content() string {
	return content
}

// Version is the reproducibility handle for the shipped protocol: a short content
// digest that changes iff Content changes. It is recorded in attempt provenance so
// an A/B cohort can be pinned to the exact protocol text it ran against. It equals
// VersionOf(Content()).
func Version() string {
	return VersionOf(content)
}

// VersionOf returns the version handle for an arbitrary append text — the digest
// recorded in provenance for whatever a session actually launched with (the
// shipped Content, an operator's override file, or nothing). Empty text yields the
// empty version, so an un-appended session records no protocol version rather than
// the digest of "".
func VersionOf(text string) string {
	if text == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(text))
	// Twelve hex chars (48 bits) is ample to tell protocol revisions apart in a
	// provenance field while staying short enough to read on the board.
	return "sha256:" + hex.EncodeToString(sum[:6])
}
