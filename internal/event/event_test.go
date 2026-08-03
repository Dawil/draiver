package event

import (
	"strings"
	"testing"
	"time"
)

func sample() Event {
	return Event{
		Seq:    3,
		Type:   "escalation",
		TS:     time.Date(2026, 8, 3, 16, 12, 30, 0, time.UTC),
		Actor:   "agent:claude-code",
		Ticket:  "PROJ-123",
		Attempt: "0001",
		Refs:    nil,
		Prev:   "9f2c",
		Body:   "Need a decision on the base image.\n",
	}
}

func TestMarshalParseRoundTrip(t *testing.T) {
	e := sample()
	e.Hash = e.ComputeHash()

	data, err := e.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := Parse(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Seq != e.Seq || got.Type != e.Type || got.Actor != e.Actor || got.Ticket != e.Ticket {
		t.Errorf("frontmatter mismatch: %+v", got)
	}
	if !got.TS.Equal(e.TS) {
		t.Errorf("ts mismatch: got %v want %v", got.TS, e.TS)
	}
	if strings.TrimRight(got.Body, "\n") != strings.TrimRight(e.Body, "\n") {
		t.Errorf("body mismatch: %q", got.Body)
	}
	if got.Hash != e.Hash {
		t.Errorf("hash mismatch: %q vs %q", got.Hash, e.Hash)
	}
}

func TestHashDeterministicAndRecomputable(t *testing.T) {
	e := sample()
	h1 := e.ComputeHash()
	h2 := e.ComputeHash()
	if h1 != h2 {
		t.Fatalf("hash not deterministic: %s vs %s", h1, h2)
	}
	// A parsed event recomputes to the same hash (the on-disk ts round-trips).
	e.Hash = h1
	data, _ := e.Marshal()
	got, _ := Parse(data)
	if got.ComputeHash() != h1 {
		t.Errorf("recomputed hash differs after round trip: %s vs %s", got.ComputeHash(), h1)
	}
}

func TestHashSensitiveToEdits(t *testing.T) {
	base := sample().ComputeHash()

	mutBody := sample()
	mutBody.Body = "tampered"
	if mutBody.ComputeHash() == base {
		t.Error("hash unchanged after body edit")
	}

	mutPrev := sample()
	mutPrev.Prev = "deadbeef"
	if mutPrev.ComputeHash() == base {
		t.Error("hash unchanged after prev edit")
	}

	mutType := sample()
	mutType.Type = "note"
	if mutType.ComputeHash() == base {
		t.Error("hash unchanged after type edit")
	}
}

func TestFilename(t *testing.T) {
	e := sample()
	if got, want := e.Filename(), "20260803T161230Z-0003-escalation.md"; got != want {
		t.Errorf("filename = %q want %q", got, want)
	}
}
