// Package event defines the atomic, write-once unit of a ticket's log: a
// hash-chained markdown file with YAML frontmatter. An event never changes once
// written; corrections are new events.
package event

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Link is an opaque, forge-neutral hyperlink attached to an event — a review
// link: a draft PR, a merge request, a diff URL, a CI run. Rel is an
// uninterpreted free label (pr, mr, diff, ci, …) draiver uses only to pick a
// primary for display; it never parses the host or the path, and there is no
// forge-specific code anywhere. Href is the URL, validated at append time
// against a hardcoded scheme allowlist (see ValidateLink).
type Link struct {
	Rel  string `yaml:"rel"`
	Href string `yaml:"href"`
}

// Event is one entry in a ticket's append-only log. The frontmatter fields are
// persisted as YAML; Body is the markdown message beneath it.
type Event struct {
	Seq       int       `yaml:"seq"`
	Type      string    `yaml:"type"`
	TS        time.Time `yaml:"ts"`
	Actor     string    `yaml:"actor"`
	Ticket    string    `yaml:"ticket"`
	Attempt   string    `yaml:"attempt"`
	Refs      []int     `yaml:"refs,omitempty"`
	Artefacts []string  `yaml:"artefacts,omitempty"`
	Links     []Link    `yaml:"links,omitempty"`
	// Outcome is an optional sentiment carried by an event whose type has more than
	// one flavour — today only `archive` (accepted vs abandoned), so a board archive
	// records whether a human accepted a finished attempt or abandoned an unfinished
	// one without a second event type. It is omitempty, so every event that does not
	// set it (all but archive) hashes and serialises exactly as before.
	Outcome string `yaml:"outcome,omitempty"`
	Prev    string `yaml:"prev"`
	Hash    string `yaml:"hash"`

	Body string `yaml:"-"`
}

// hashable mirrors Event's frontmatter with the Hash field omitted (a field
// cannot hash itself) and TS pinned to a canonical RFC3339 UTC string, so the
// canonical form is byte-stable across marshals.
type hashable struct {
	Seq       int      `yaml:"seq"`
	Type      string   `yaml:"type"`
	TS        string   `yaml:"ts"`
	Actor     string   `yaml:"actor"`
	Ticket    string   `yaml:"ticket"`
	Attempt   string   `yaml:"attempt"`
	Refs      []int    `yaml:"refs,omitempty"`
	Artefacts []string `yaml:"artefacts,omitempty"`
	Links     []Link   `yaml:"links,omitempty"`
	Outcome   string   `yaml:"outcome,omitempty"`
	Prev      string   `yaml:"prev"`
}

// ComputeHash returns the sha256 (hex) over the event's canonical frontmatter
// (excluding the hash field, which includes prev for chain linkage) followed by
// the body. Editing any field, the body, or prev changes the hash.
func (e Event) ComputeHash() string {
	h := hashable{
		Seq:       e.Seq,
		Type:      e.Type,
		TS:        e.TS.UTC().Format(time.RFC3339),
		Actor:     e.Actor,
		Ticket:    e.Ticket,
		Attempt:   e.Attempt,
		Refs:      e.Refs,
		Artefacts: e.Artefacts,
		Links:     e.Links,
		Outcome:   e.Outcome,
		Prev:      e.Prev,
	}
	front, err := yaml.Marshal(h)
	if err != nil {
		// yaml.Marshal on this fixed struct cannot fail in practice.
		panic("event: canonical marshal failed: " + err.Error())
	}
	// Canonicalize the body exactly as it is persisted (trailing newlines
	// trimmed) so a parsed event recomputes to the same hash.
	body := strings.TrimRight(e.Body, "\n")
	sum := sha256.Sum256(append(front, append([]byte("\n"), []byte(body)...)...))
	return hex.EncodeToString(sum[:])
}

// Marshal renders the event to its on-disk form: a YAML frontmatter block
// (including the hash) delimited by --- lines, followed by the markdown body.
func (e Event) Marshal() ([]byte, error) {
	// Normalize TS so the persisted frontmatter matches the hashed form.
	e.TS = e.TS.UTC().Truncate(time.Second)
	front, err := yaml.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("marshal frontmatter: %w", err)
	}
	var buf bytes.Buffer
	buf.WriteString("---\n")
	buf.Write(front)
	buf.WriteString("---\n")
	body := strings.TrimRight(e.Body, "\n")
	if body != "" {
		buf.WriteString(body)
		buf.WriteString("\n")
	}
	return buf.Bytes(), nil
}

// Parse reads an event from its on-disk form.
func Parse(data []byte) (Event, error) {
	s := string(data)
	if !strings.HasPrefix(s, "---\n") {
		return Event{}, fmt.Errorf("event: missing frontmatter opener")
	}
	rest := s[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		// Allow a frontmatter-only file whose closing --- ends the input.
		if strings.HasSuffix(rest, "\n---") {
			end = len(rest) - len("\n---")
		} else {
			return Event{}, fmt.Errorf("event: missing frontmatter closer")
		}
	}
	front := rest[:end]
	var e Event
	if err := yaml.Unmarshal([]byte(front), &e); err != nil {
		return Event{}, fmt.Errorf("event: unmarshal frontmatter: %w", err)
	}
	bodyStart := end + len("\n---\n")
	if bodyStart <= len(rest) {
		e.Body = strings.TrimRight(rest[min(bodyStart, len(rest)):], "\n")
	}
	return e, nil
}

// Filename is the write-once log filename for the event: a filesystem-safe
// basic-ISO-8601 UTC timestamp, a zero-padded sequence, and the type. It sorts
// lexicographically into causal order.
func (e Event) Filename() string {
	return fmt.Sprintf("%s-%04d-%s.md", e.TS.UTC().Format("20060102T150405Z"), e.Seq, e.Type)
}
