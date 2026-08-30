package project

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Dawil/draiver/internal/store"
)

// writeSpec writes raw spec.md content for a ticket under root, creating the
// ticket dir. It bypasses the CLI so a test can pin the exact frontmatter form.
func writeSpec(t *testing.T, root store.Root, ticket, content string) {
	t.Helper()
	if err := os.MkdirAll(root.TicketDir(ticket), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root.SpecPath(ticket), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWantsClosure(t *testing.T) {
	// CAP wants SRC and INFRA; SRC wants DEEP (a grand-child). MID is unrelated.
	edges := map[string]Edges{
		"CAP": {Wants: []string{"SRC", "INFRA"}},
		"SRC": {Wants: []string{"DEEP"}},
		"MID": {Wants: []string{"OTHER"}},
	}
	got := WantsClosure(map[string]bool{"CAP": true}, edges)
	want := map[string]bool{"SRC": true, "INFRA": true, "DEEP": true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("WantsClosure = %v want %v", got, want)
	}
	// A source that also appears as a wanted child is not re-emitted as wanted.
	got = WantsClosure(map[string]bool{"CAP": true, "SRC": true}, edges)
	want = map[string]bool{"INFRA": true, "DEEP": true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("WantsClosure with SRC as source = %v want %v", got, want)
	}
	// A cyclic hand-edited graph must not spin (authoring refuses cycles, but the
	// guard is defence in depth).
	cyclic := map[string]Edges{"A": {Wants: []string{"B"}}, "B": {Wants: []string{"A"}}}
	got = WantsClosure(map[string]bool{"A": true}, cyclic)
	if !reflect.DeepEqual(got, map[string]bool{"B": true}) {
		t.Errorf("cyclic WantsClosure = %v want {B}", got)
	}
}

func TestDeriveDesired(t *testing.T) {
	att := func(ticket, id string, state State, enabled bool) Attempt {
		return Attempt{Ticket: ticket, ID: id, State: state, Enabled: enabled}
	}
	// CAP is directly enabled and wants SRC, INFRA, and GATED. SRC's latest attempt
	// is Running (admissible via parent); INFRA's latest is a Review claim (left
	// alone — parent desiredness never force-admits a non-Running child); GATED has
	// no attempt yet (the daemon would mint one — nothing to project). DIRECT is
	// enabled on its own. OFF is enabled but Done, so it is not a source. LONELY is
	// wanted by nobody enabled.
	all := []Attempt{
		att("CAP", "0001", Running, true),
		att("DIRECT", "0001", Running, true),
		att("INFRA", "0001", Review, false),
		att("LONELY", "0001", Running, false),
		att("OFF", "0001", Done, true),
		att("SRC", "0001", Done, false),    // an older, spent attempt
		att("SRC", "0002", Running, false), // the latest — admissible via CAP
	}
	edges := map[string]Edges{
		"CAP": {Wants: []string{"SRC", "INFRA", "GATED"}},
	}
	got := DeriveDesired(all, edges)
	want := map[Ref]bool{
		{"CAP", "0001"}:    true, // directly enabled + Running
		{"DIRECT", "0001"}: true, // directly enabled + Running
		{"SRC", "0002"}:    true, // enabled-via-parent: latest Running attempt targeted
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DeriveDesired = %v\nwant %v", got, want)
	}
	// The enabled-via-parent child, once folded through Control with no live agent,
	// is Pending — acceptance #1.
	if s := Control(Running, got[Ref{"SRC", "0002"}], false); s != Pending {
		t.Errorf("via-parent child Control = %q want Pending", s)
	}
	// A disabled parent sources nothing: SRC drops out of the desired set.
	if d := DeriveDesired([]Attempt{att("CAP", "0001", Running, false), att("SRC", "0002", Running, false)},
		edges); len(d) != 0 {
		t.Errorf("disabled parent should desire nothing, got %v", d)
	}
}

func TestWaitingReason(t *testing.T) {
	att := func(ticket, id string, state State) Attempt {
		return Attempt{Ticket: ticket, ID: id, State: state}
	}
	// E2E is admitted after SRC reaches Review and requires INFRA reach Done. SRC is
	// only Running and INFRA only Review, so both gates are shut. GATED has no
	// predecessors. DONE-DEP is a satisfied requires: predecessor.
	all := []Attempt{
		att("SRC", "0001", Running),
		att("INFRA", "0001", Review),
		att("SHIPPED", "0001", Done),
	}
	edges := map[string]Edges{
		"E2E":   {After: []string{"SRC"}, Requires: []string{"INFRA"}},
		"GATED": {},
		"OKDEP": {After: []string{"SRC"}, Requires: []string{"SHIPPED"}},
		"BOTH":  {After: []string{"SRC"}, Requires: []string{"SRC"}},
	}

	// after:SRC unmet (SRC only Running, not Review) and requires:INFRA unmet (INFRA
	// only Review, not Done) — both named, after: before requires:.
	if got := WaitingReason("E2E", all, edges); got != "waiting on SRC, INFRA" {
		t.Errorf("E2E reason = %q want %q", got, "waiting on SRC, INFRA")
	}
	// after:SRC unmet but requires:SHIPPED satisfied (Done) — only SRC named.
	if got := WaitingReason("OKDEP", all, edges); got != "waiting on SRC" {
		t.Errorf("OKDEP reason = %q want %q", got, "waiting on SRC")
	}
	// SRC appears in both after: and requires:; it is named once.
	if got := WaitingReason("BOTH", all, edges); got != "waiting on SRC" {
		t.Errorf("BOTH reason = %q want %q (de-duplicated)", got, "waiting on SRC")
	}
	// No ordering predecessors: Pending for want of admission, not an edge.
	if got := WaitingReason("GATED", all, edges); got != "waiting to start" {
		t.Errorf("GATED reason = %q want %q", got, "waiting to start")
	}
	// A ticket with no edges entry at all also falls back to the generic reason.
	if got := WaitingReason("UNKNOWN", all, edges); got != "waiting to start" {
		t.Errorf("UNKNOWN reason = %q want %q", got, "waiting to start")
	}

	// Once SRC reaches Review and INFRA reaches Done, E2E's gate opens — no reason.
	opened := []Attempt{
		att("SRC", "0001", Review),
		att("INFRA", "0001", Done),
	}
	if got := WaitingReason("E2E", opened, edges); got != "waiting to start" {
		t.Errorf("E2E reason after gate opens = %q want %q", got, "waiting to start")
	}
}

func TestLoadEdgesInlineAndBlock(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	writeSpec(t, root, "A", "---\nid: A\nwants: [B, C]\nafter: [D]\n---\n\nbody\n")
	writeSpec(t, root, "B", "---\nid: B\nrequires:\n  - E\n  - F\n---\n\nbody\n")

	a, err := LoadEdges(root, "A")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.Wants, []string{"B", "C"}) || !reflect.DeepEqual(a.After, []string{"D"}) {
		t.Errorf("A edges = %+v", a)
	}
	b, err := LoadEdges(root, "B")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(b.Requires, []string{"E", "F"}) {
		t.Errorf("B requires = %+v", b.Requires)
	}
}

func TestLoadEdgesTolerant(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	// No frontmatter, unknown keys, and no edge keys must all yield a zero Edges
	// with no error — omitted/unknown fields are fine.
	writeSpec(t, root, "PROSE", "Just prose, no frontmatter.\n")
	writeSpec(t, root, "OTHER", "---\nid: OTHER\nteam: platform\nnovel_key: 1\n---\n\nbody\n")
	// A ticket with no spec.md at all.
	if err := os.MkdirAll(root.TicketDir("BARE"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tk := range []string{"PROSE", "OTHER", "BARE"} {
		e, err := LoadEdges(root, tk)
		if err != nil {
			t.Fatalf("%s: %v", tk, err)
		}
		if len(e.All()) != 0 {
			t.Errorf("%s: expected no edges, got %+v", tk, e)
		}
	}
}

func TestReverseWants(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	writeSpec(t, root, "CAP", "---\nid: CAP\nwants: [SRC, INFRA, E2E]\n---\n")
	writeSpec(t, root, "OTHER", "---\nid: OTHER\nwants: [SRC]\n---\n")
	writeSpec(t, root, "SRC", "---\nid: SRC\n---\n")

	rev, err := ReverseWants(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := rev["SRC"]; !reflect.DeepEqual(got, []string{"CAP", "OTHER"}) {
		t.Errorf("wanters of SRC = %v, want [CAP OTHER]", got)
	}
	if got := rev["INFRA"]; !reflect.DeepEqual(got, []string{"CAP"}) {
		t.Errorf("wanters of INFRA = %v, want [CAP]", got)
	}
	// A ticket nobody wants is absent from the index.
	if _, ok := rev["CAP"]; ok {
		t.Errorf("CAP should have no wanters")
	}
}

func TestCheckCycle(t *testing.T) {
	cases := []struct {
		name string
		all  map[string]Edges
		want []string // nil ⇒ expect DAG (no cycle)
	}{
		{"empty", map[string]Edges{}, nil},
		{"dag", map[string]Edges{
			"A": {Wants: []string{"B", "C"}},
			"B": {After: []string{"C"}},
		}, nil},
		{"self-wants", map[string]Edges{"A": {Wants: []string{"A"}}}, []string{"A", "A"}},
		{"two-cycle", map[string]Edges{
			"A": {Wants: []string{"B"}},
			"B": {Wants: []string{"A"}},
		}, []string{"A", "B", "A"}},
		{"mixed-relation-cycle", map[string]Edges{
			"A": {After: []string{"B"}},
			"B": {Requires: []string{"A"}},
		}, []string{"A", "B", "A"}},
		{"three-cycle", map[string]Edges{
			"A": {Wants: []string{"B"}},
			"B": {After: []string{"C"}},
			"C": {Requires: []string{"A"}},
		}, []string{"A", "B", "C", "A"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CheckCycle(tc.all)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("CheckCycle = %v, want %v", got, tc.want)
			}
		})
	}
}

// specFrontmatter and loadSpecMeta must stay in lockstep on the fence framing;
// this guards the refactor that extracted the shared extractor.
func TestSpecFrontmatterFraming(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "spec.md")
	if err := os.WriteFile(p, []byte("---\nid: X\ntitle: Hi\n---\n\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	block, ok, err := specFrontmatter(p)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if block != "id: X\ntitle: Hi" {
		t.Errorf("block = %q", block)
	}
}
