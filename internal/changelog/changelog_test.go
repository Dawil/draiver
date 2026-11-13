package changelog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func frag(section, body string) string { return "---\nsection: " + section + "\n---\n" + body + "\n" }

func TestParseFragment(t *testing.T) {
	cases := []struct {
		name, src, wantErr string
	}{
		{"valid", frag("Added", "**X (drv-1).** prose"), ""},
		{"comment in frontmatter", "---\nsection: Fixed   # Added | Fixed\n---\n**Y.**\n", ""},
		{"skip", "---\nskip: internal refactor only\n---\n", ""},
		{"no frontmatter", "**X.**\n", "missing frontmatter"},
		{"bad section", frag("Improved", "**X.**"), `unknown section "Improved"`},
		{"no section", "---\n---\n**X.**\n", "names no `section:`"},
		{"empty body", frag("Added", "   "), "empty body"},
		{"empty skip", "---\nskip:\n---\n", "non-empty reason"},
		{"blank skip", "---\nskip: \"  \"\n---\n", "non-empty reason"},
		{"skip with body", "---\nskip: nothing\n---\n**X.**\n", "must have no body"},
		{"unknown key", "---\nsectoin: Added\n---\n**X.**\n", `unknown frontmatter key "sectoin"`},
		{"bad yaml", "---\nsection: [\n---\n**X.**\n", "not valid YAML"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseFragment("drv-1", "changelog.d/drv-1.md", []byte(c.src))
			switch {
			case c.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
				t.Fatalf("error = %v, want containing %q", err, c.wantErr)
			}
		})
	}
}

func seedTree(t *testing.T) string {
	root := t.TempDir()
	write(t, root, "changelog.d/_header.md", "# Changelog\n\nIntro.\n")
	write(t, root, "changelog.d/README.md", "docs, not a fragment\n")
	write(t, root, "changelog.d/drvweb-2.md", frag("Added", "**Web (drvweb-2).**\nmore"))
	write(t, root, "changelog.d/drv-10.md", frag("Added", "**Ten (drv-10).**"))
	write(t, root, "changelog.d/drv-9.md", frag("Added", "**Nine (drv-9).**\n\n  - nested"))
	write(t, root, "changelog.d/drvctl-3.md", frag("Fixed", "**Ctl (drvctl-3).**"))
	write(t, root, "changelog.d/drv-11.md", "---\nskip: nothing user-visible\n---\n")
	write(t, root, "changelog.d/drv-9.2.md", frag("Added", "**Nine again (drv-9).**"))
	write(t, root, "changelog.d/0.1.0/_release.md", "---\ndate: 2026-01-01\n---\n\nFirst.\n")
	write(t, root, "changelog.d/0.1.0/02-b.md", frag("Added", "**B.**"))
	write(t, root, "changelog.d/0.1.0/01-a.md", frag("Added", "**A.**"))
	write(t, root, "changelog.d/0.10.0/_release.md", "---\ndate: 2026-02-01\n---\n")
	write(t, root, "changelog.d/0.10.0/drv-1.md", frag("Security", "**One (drv-1).**"))
	write(t, root, "changelog.d/0.9.0/_release.md", "---\ndate: 2026-01-15\n---\n")
	write(t, root, "changelog.d/0.9.0/drv-2.md", frag("Changed", "**Two (drv-2).**"))
	return root
}

const wantRender = `# Changelog

Intro.

## [Unreleased]

### Added

- **Nine (drv-9).**

    - nested
- **Nine again (drv-9).**
- **Ten (drv-10).**
- **Web (drvweb-2).**
  more

### Fixed

- **Ctl (drvctl-3).**

## [0.10.0] - 2026-02-01

### Security

- **One (drv-1).**

## [0.9.0] - 2026-01-15

### Changed

- **Two (drv-2).**

## [0.1.0] - 2026-01-01

First.

### Added

- **A.**
- **B.**
`

func TestRenderOrderSkipAndIdempotence(t *testing.T) {
	root := seedTree(t)
	tr, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	got := string(Render(tr))
	if got != wantRender {
		t.Fatalf("render mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, wantRender)
	}
	tr2, _ := Load(root)
	if string(Render(tr2)) != got {
		t.Fatal("render is not deterministic")
	}
}

func TestLoadCollectsEveryError(t *testing.T) {
	root := seedTree(t)
	write(t, root, "changelog.d/drv-20.md", frag("Bogus", "**x**"))
	write(t, root, "changelog.d/0.9.0/drv-21.md", frag("Added", ""))
	write(t, root, "changelog.d/notaversion/x.md", frag("Added", "x"))
	_, err := Load(root)
	if err == nil {
		t.Fatal("want errors")
	}
	for _, want := range []string{"drv-20.md", "drv-21.md", "notaversion"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %s: %v", want, err)
		}
	}
}

func TestCheck(t *testing.T) {
	root := seedTree(t)
	if _, err := Check(root, "drv-10"); err != nil {
		t.Fatalf("valid fragment: %v", err)
	}
	if fr, err := Check(root, "drv-11"); err != nil || fr.Skip == "" {
		t.Fatalf("skip fragment: %+v %v", fr, err)
	}
	if _, err := Check(root, "drv-1"); err != nil {
		t.Fatalf("released fragment should satisfy the rung: %v", err)
	}
	_, err := Check(root, "drv-99")
	if err == nil || !strings.Contains(err.Error(), "changelog.d/drv-99.md") {
		t.Fatalf("missing fragment error must name the expected path: %v", err)
	}
	if _, err := Check(root, ""); err == nil || !strings.Contains(err.Error(), "DRAIVER_TICKET") {
		t.Fatalf("no ticket: %v", err)
	}
	// A malformed *other* fragment fails the rung even when the ticket's is fine.
	write(t, root, "changelog.d/drv-30.md", "---\nskip:\n---\n")
	if _, err := Check(root, "drv-10"); err == nil || !strings.Contains(err.Error(), "drv-30.md") {
		t.Fatalf("malformed sibling must fail the rung: %v", err)
	}
	// The ticket's own malformed fragment is reported as malformed, not missing.
	if _, err := Check(root, "drv-30"); err == nil || strings.Contains(err.Error(), "missing changelog fragment") {
		t.Fatalf("malformed own fragment: %v", err)
	}
}

func TestTicketFromBranch(t *testing.T) {
	if got, ok := TicketFromBranch("draiver/drv-052/0001\n"); !ok || got != "drv-052" {
		t.Fatalf("got %q %v", got, ok)
	}
	for _, b := range []string{"main", "draiver/drv-052", "feature/x/y"} {
		if _, ok := TicketFromBranch(b); ok {
			t.Errorf("%q should not parse", b)
		}
	}
}

// The committed changelog.d/ tree loads with no malformed fragment.
func TestRepoChangelogLoads(t *testing.T) {
	root := filepath.Join("..", "..")
	if _, err := Load(root); err != nil {
		t.Fatalf("repo changelog.d does not load: %v", err)
	}
}
