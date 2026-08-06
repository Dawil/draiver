package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// lastEvent returns the highest-seq event on PROJ-1/0001.
func lastEvent(t *testing.T, dir string) (typ string, links [][2]string) {
	t.Helper()
	events, err := ticketlog.Read(store.Root{Dir: dir}, "PROJ-1", "0001")
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	e := events[len(events)-1]
	for _, l := range e.Links {
		links = append(links, [2]string{l.Rel, l.Href})
	}
	return e.Type, links
}

// review --url attaches a pr-rel link; --link rel=uri attaches an explicit rel.
// The event round-trips through the log and the chain still audits clean.
func TestReviewAttachesLinksAndAudits(t *testing.T) {
	dir := newTicket(t)
	// Force the default (empty) host allowlist regardless of the dev machine's
	// ~/.draiver/config.json.
	t.Setenv("DRAIVER_CONFIG", filepath.Join(dir, "no-config.json"))

	out, code := run(t, "--data", dir, "--actor", "agent:x", "review", "PROJ-1", "done",
		"--url", "https://github.com/o/r/pull/7",
		"--link", "diff=https://example.com/a...b.diff")
	if code != 0 {
		t.Fatalf("review exited %d: %s", code, out)
	}

	typ, links := lastEvent(t, dir)
	if typ != "review" {
		t.Fatalf("last event type = %q, want review", typ)
	}
	want := [][2]string{
		{"pr", "https://github.com/o/r/pull/7"},
		{"diff", "https://example.com/a...b.diff"},
	}
	if len(links) != len(want) {
		t.Fatalf("links = %v, want %v", links, want)
	}
	for i := range want {
		if links[i] != want[i] {
			t.Errorf("link[%d] = %v, want %v", i, links[i], want[i])
		}
	}

	// The hash chain must remain intact with links on the log.
	if out, code := run(t, "--data", dir, "audit", "PROJ-1"); code != 0 {
		t.Fatalf("audit exited %d after link append: %s", code, out)
	}
}

// log --url attaches an interim link to a note; the review claim is not the only
// carrier.
func TestLogAttachesLink(t *testing.T) {
	dir := newTicket(t)
	t.Setenv("DRAIVER_CONFIG", filepath.Join(dir, "no-config.json"))

	out, code := run(t, "--data", dir, "--actor", "agent:x", "log", "PROJ-1", "draft up",
		"--type", "note", "--url", "https://example.com/pr/1")
	if code != 0 {
		t.Fatalf("log exited %d: %s", code, out)
	}
	typ, links := lastEvent(t, dir)
	if typ != "note" || len(links) != 1 || links[0] != [2]string{"pr", "https://example.com/pr/1"} {
		t.Fatalf("note link = (%q, %v), want note/pr", typ, links)
	}
}

// A disallowed scheme fails the command and writes nothing — the event count is
// unchanged (matches new's reject-writes-nothing posture).
func TestLinkSchemeRejectionWritesNothing(t *testing.T) {
	dir := newTicket(t)
	t.Setenv("DRAIVER_CONFIG", filepath.Join(dir, "no-config.json"))

	before, _ := ticketlog.Read(store.Root{Dir: dir}, "PROJ-1", "0001")
	out, code := run(t, "--data", dir, "--actor", "agent:x", "review", "PROJ-1", "done",
		"--url", "javascript:alert(1)")
	if code == 0 {
		t.Fatalf("review with a javascript: url should fail, got 0: %s", out)
	}
	after, _ := ticketlog.Read(store.Root{Dir: dir}, "PROJ-1", "0001")
	if len(after) != len(before) {
		t.Errorf("rejected link wrote an event: %d -> %d", len(before), len(after))
	}
}

// A malformed --link (no rel=uri) is rejected before any write.
func TestLinkFlagMalformedRejected(t *testing.T) {
	dir := newTicket(t)
	if _, code := run(t, "--data", dir, "review", "PROJ-1", "done", "--link", "noequals"); code == 0 {
		t.Error("expected nonzero exit for a --link without rel=uri")
	}
	if _, code := run(t, "--data", dir, "review", "PROJ-1", "done", "--link", "=https://x.com"); code == 0 {
		t.Error("expected nonzero exit for a --link with an empty rel")
	}
}

// The host allowlist admits an on-list host and denies an off-list one, driven
// by the deployment config the append path loads.
func TestLinkHostAllowlistFromConfig(t *testing.T) {
	dir := newTicket(t)
	cfg := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfg, []byte(`{"review_link_hosts": ["bitbucket.mycorp.com"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DRAIVER_CONFIG", cfg)

	// Off-list host: rejected, writes nothing.
	before, _ := ticketlog.Read(store.Root{Dir: dir}, "PROJ-1", "0001")
	if _, code := run(t, "--data", dir, "--actor", "agent:x", "review", "PROJ-1", "done",
		"--url", "https://github.com/o/r/pull/1"); code == 0 {
		t.Error("off-allowlist host should be rejected")
	}
	after, _ := ticketlog.Read(store.Root{Dir: dir}, "PROJ-1", "0001")
	if len(after) != len(before) {
		t.Errorf("denied link wrote an event: %d -> %d", len(before), len(after))
	}

	// On-list host: admitted.
	out, code := run(t, "--data", dir, "--actor", "agent:x", "review", "PROJ-1", "done",
		"--url", "https://bitbucket.mycorp.com/r/1")
	if code != 0 {
		t.Fatalf("allowlisted host rejected: %d %s", code, out)
	}
	if typ, links := lastEvent(t, dir); typ != "review" || len(links) != 1 ||
		!strings.Contains(links[0][1], "bitbucket.mycorp.com") {
		t.Errorf("allowlisted link not stored: %q %v", typ, links)
	}
}
