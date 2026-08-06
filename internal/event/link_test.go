package event

import (
	"bytes"
	"testing"

	"gopkg.in/yaml.v3"
)

// The hardcoded scheme floor admits http/https (any case) and rejects
// everything else — the javascript:/file:/data: footguns and relative URLs.
func TestValidateLinkScheme(t *testing.T) {
	ok := []string{
		"http://example.com/x",
		"https://example.com/x",
		"HTTPS://Example.com/x", // scheme match is case-insensitive
	}
	for _, h := range ok {
		if err := ValidateLink(Link{Rel: "pr", Href: h}, nil); err != nil {
			t.Errorf("ValidateLink(%q) = %v, want nil", h, err)
		}
	}
	bad := []string{
		"javascript:alert(1)",
		"file:///etc/passwd",
		"data:text/html,x",
		"ftp://example.com/x",
		"example.com/x",  // no scheme → not absolute
		"/relative/path", // not absolute
		"",               // empty
		"   ",            // blank
	}
	for _, h := range bad {
		if err := ValidateLink(Link{Rel: "pr", Href: h}, nil); err == nil {
			t.Errorf("ValidateLink(%q) = nil, want rejection", h)
		}
	}
}

// The optional host allowlist admits a matching host (case-insensitively) and
// denies any other; an empty allowlist admits any host.
func TestValidateLinkHostAllowlist(t *testing.T) {
	allow := []string{"bitbucket.mycorp.com"}

	if err := ValidateLink(Link{Rel: "pr", Href: "https://bitbucket.mycorp.com/r/1"}, allow); err != nil {
		t.Errorf("allowlisted host rejected: %v", err)
	}
	if err := ValidateLink(Link{Rel: "pr", Href: "https://BitBucket.MyCorp.com/r/1"}, allow); err != nil {
		t.Errorf("allowlist host match should be case-insensitive: %v", err)
	}
	if err := ValidateLink(Link{Rel: "pr", Href: "https://github.com/o/r/pull/1"}, allow); err == nil {
		t.Error("off-allowlist host admitted, want rejection")
	}
	// Empty allowlist admits any (opt-in, off by default).
	if err := ValidateLink(Link{Rel: "pr", Href: "https://anywhere.example/x"}, nil); err != nil {
		t.Errorf("empty allowlist should admit any host: %v", err)
	}
}

// A link-bearing event round-trips through Marshal/Parse with its Links intact
// and recomputes to the same hash, so links are covered by the chain.
func TestLinksRoundTripAndHash(t *testing.T) {
	e := sample()
	e.Links = []Link{
		{Rel: "pr", Href: "https://github.com/o/r/pull/7"},
		{Rel: "diff", Href: "http://example.com/a...b.diff"},
	}
	e.Hash = e.ComputeHash()

	data, err := e.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := Parse(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got.Links) != 2 || got.Links[0] != e.Links[0] || got.Links[1] != e.Links[1] {
		t.Fatalf("links did not round-trip: %+v", got.Links)
	}
	if got.ComputeHash() != e.Hash {
		t.Errorf("parsed link event recomputes to a different hash")
	}
}

// Links participate in the hash: mutating a link changes the hash, so audit
// catches a tampered link.
func TestLinksAreHashed(t *testing.T) {
	e := sample()
	e.Links = []Link{{Rel: "pr", Href: "https://example.com/1"}}
	base := e.ComputeHash()

	e.Links[0].Href = "https://example.com/2"
	if e.ComputeHash() == base {
		t.Error("editing a link Href did not change the hash")
	}

	e.Links[0].Href = "https://example.com/1"
	e.Links[0].Rel = "mr"
	if e.ComputeHash() == base {
		t.Error("editing a link Rel did not change the hash")
	}
}

// omitempty: a link-less event emits no `links:` key — neither in its persisted
// on-disk form nor, critically, in the canonical projection that ComputeHash
// feeds. An unchanged canonical projection is exactly what makes a pre-Links
// (legacy) event hash identically under the new schema, so no migration is
// needed and old events still pass audit.
func TestNoLinksOmitsFieldFromDiskAndHashInput(t *testing.T) {
	e := sample() // sample() sets no Links: the legacy shape

	data, err := e.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if bytes.Contains(data, []byte("links:")) {
		t.Errorf("link-less event emitted a links: key on disk:\n%s", data)
	}

	// The hash input is yaml.Marshal(hashable). Marshal it here (same-package
	// access) and assert the Links field is omitted, i.e. the bytes fed to
	// sha256 are byte-identical to the pre-Links canonical form.
	canon, err := yaml.Marshal(hashable{
		Seq: e.Seq, Type: e.Type, TS: "2026-08-03T16:12:30Z",
		Actor: e.Actor, Ticket: e.Ticket, Attempt: e.Attempt, Prev: e.Prev,
	})
	if err != nil {
		t.Fatalf("marshal hashable: %v", err)
	}
	if bytes.Contains(canon, []byte("links:")) {
		t.Errorf("nil Links leaked into the hash input:\n%s", canon)
	}
}
