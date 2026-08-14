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
