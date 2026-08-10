package attempt

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

func TestCreateAllocatesSequentialIDsWithGenesis(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	id := "PROJ-1"
	if err := root.EnsureTicketDir(id); err != nil {
		t.Fatal(err)
	}

	a1, err := Create(root, id, New{Tool: "claude-code", Model: "opus-4.8", Repo: "/repo", Actor: "agent:x"})
	if err != nil {
		t.Fatalf("create 1: %v", err)
	}
	a2, err := Create(root, id, New{Tool: "aider", Repo: "/repo", Actor: "agent:y"})
	if err != nil {
		t.Fatalf("create 2: %v", err)
	}
	if a1.ID != "0001" || a2.ID != "0002" {
		t.Fatalf("ids = %q,%q want 0001,0002", a1.ID, a2.ID)
	}

	// Each attempt gets its own genesis "created" event at seq 1.
	for _, aid := range []string{"0001", "0002"} {
		events, err := ticketlog.Read(root, id, aid)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 1 || events[0].Type != "created" || events[0].Seq != 1 {
			t.Errorf("attempt %s genesis wrong: %+v", aid, events)
		}
	}

	// Meta round-trips.
	m, err := LoadMeta(root, id, "0001")
	if err != nil {
		t.Fatal(err)
	}
	if m.Tool != "claude-code" || m.Model != "opus-4.8" || m.Actor != "agent:x" || m.Repo != "/repo" {
		t.Errorf("meta not persisted: %+v", m)
	}

	latest, ok, err := Latest(root, id)
	if err != nil || !ok || latest != "0002" {
		t.Errorf("latest = %q,%v,%v want 0002,true,nil", latest, ok, err)
	}
}

// A set load-bearing field renders as a real YAML line; an unset one renders as a
// commented example naming the key, a sample, and its consumer — so a hand-editor
// of a wedged (base-less) attempt.md sees the schema it must fill in.
func TestWriteMetaCommentsUnsetLoadBearingFields(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	id := "PROJ-1"
	if err := root.EnsureTicketDir(id); err != nil {
		t.Fatal(err)
	}
	// A partial attempt: repo set, base/tool/model unset.
	if _, err := Create(root, id, New{Repo: "/repo", Actor: "human:x"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(root.AttemptMetaPath(id, "0001"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "repo: /repo") {
		t.Errorf("set repo should render as a real line:\n%s", s)
	}
	for _, want := range []string{"# base: ", "# tool: ", "# model: "} {
		if !strings.Contains(s, want) {
			t.Errorf("unset field %q should render as a commented hint:\n%s", want, s)
		}
	}
	// The hints are YAML comments, so parseMeta ignores them: the fields read empty.
	m, err := LoadMeta(root, id, "0001")
	if err != nil {
		t.Fatal(err)
	}
	if m.Base != "" || m.Tool != "" || m.Model != "" {
		t.Errorf("commented hints must not parse as values: %+v", m)
	}
	if m.Repo != "/repo" {
		t.Errorf("repo lost across write/parse: %+v", m)
	}
}

// WriteMeta re-emits the canonical block, so a write→parse→write cycle is stable:
// commented hints for unset fields survive the round-trip unchanged.
func TestWriteMetaRoundTripIsIdempotent(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	id := "PROJ-1"
	if err := root.EnsureTicketDir(id); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(root, id, New{Repo: "/repo", Actor: "human:x"}); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(root.AttemptMetaPath(id, "0001"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := LoadMeta(root, id, "0001")
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteMeta(root, m); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(root.AttemptMetaPath(id, "0001"))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("write→parse→write not idempotent:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

// The metrics block folded in on retire (drvctl-031) renders as a nested YAML
// block and round-trips through write→parse: the raw fields, the derived metrics,
// and caching_active all survive.
func TestWriteMetaRoundTripsMetrics(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	id := "PROJ-1"
	if err := root.EnsureTicketDir(id); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(root, id, New{Repo: "/repo", Actor: "human:x"}); err != nil {
		t.Fatal(err)
	}
	m, err := LoadMeta(root, id, "0001")
	if err != nil {
		t.Fatal(err)
	}
	metrics := agent.Totals{InputTokens: 100, OutputTokens: 20, CacheReadTokens: 50, CacheCreationTokens: 30}.Metrics()
	m.Metrics = &metrics
	if err := WriteMeta(root, m); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(root.AttemptMetaPath(id, "0001"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"metrics:", "cache_read_tokens: 50", "caching_active: true", "normalized_work: 200"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("metrics block missing %q:\n%s", want, data)
		}
	}

	back, err := LoadMeta(root, id, "0001")
	if err != nil {
		t.Fatal(err)
	}
	if back.Metrics == nil {
		t.Fatalf("metrics lost across write/parse:\n%s", data)
	}
	if *back.Metrics != metrics {
		t.Errorf("metrics round-trip = %+v, want %+v", *back.Metrics, metrics)
	}

	// A second write→parse cycle stays stable.
	if err := WriteMeta(root, back); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(root.AttemptMetaPath(id, "0001"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(second) {
		t.Errorf("metrics write not idempotent:\nfirst:\n%s\nsecond:\n%s", data, second)
	}
}

func TestCreateRejectsUnknownTicket(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	if _, err := Create(root, "NOPE-1", New{Repo: "/repo", Actor: "a"}); err == nil {
		t.Error("expected error creating attempt on nonexistent ticket")
	}
}

// Create is the chokepoint that guarantees no attempt is minted without a repo
// (drvctl-017): an empty or whitespace-only Repo is refused, and nothing is
// written to the (existing) ticket.
func TestCreateRequiresRepo(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	id := "PROJ-1"
	if err := root.EnsureTicketDir(id); err != nil {
		t.Fatal(err)
	}
	for _, repo := range []string{"", "   "} {
		if _, err := Create(root, id, New{Actor: "agent:x", Repo: repo}); !errors.Is(err, ErrRepoRequired) {
			t.Errorf("Create(repo=%q) err = %v, want ErrRepoRequired", repo, err)
		}
	}
	// The refused creations wrote no attempt.
	if ids, _ := List(root, id); len(ids) != 0 {
		t.Errorf("a repo-less Create left an attempt behind: %v", ids)
	}
}
