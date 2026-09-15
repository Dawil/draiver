package pyramid

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writePyramid drops body into a fresh temp worktree as .test-pyramid.yaml and
// returns the worktree path, mirroring config_test.go's writeConfig helper.
func writePyramid(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

const validLadder = `
levels:
  - name: unit
    run: make test
  - name: integration
    run: docker compose up -d db && go test -tags=integration ./... ; docker compose down -v
`

func TestLoad_MissingFileIsNoPyramid(t *testing.T) {
	// A worktree with no .test-pyramid.yaml is a valid, non-error state: "no pyramid".
	p, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if p != nil {
		t.Fatalf("missing file should yield a nil pyramid, got %+v", p)
	}
}

func TestLoad_ValidLadder(t *testing.T) {
	p, err := Load(writePyramid(t, validLadder))
	if err != nil {
		t.Fatalf("valid ladder should load: %v", err)
	}
	if p == nil {
		t.Fatal("valid ladder yielded nil pyramid")
	}
	if len(p.Levels) != 2 {
		t.Fatalf("got %d levels, want 2", len(p.Levels))
	}
	// Order is preserved bottom-to-top.
	if p.Levels[0].Name != "unit" || p.Levels[1].Name != "integration" {
		t.Errorf("levels out of order: %q then %q", p.Levels[0].Name, p.Levels[1].Name)
	}
	if p.Levels[0].Run != "make test" {
		t.Errorf("unit run = %q, want %q", p.Levels[0].Run, "make test")
	}
	// Target is the top (last) rung.
	if got := p.Target(); got.Name != "integration" {
		t.Errorf("Target().Name = %q, want integration (the top rung)", got.Name)
	}
}

func TestLoad_ForwardCompatibleUnknownKeys(t *testing.T) {
	// A newer file carrying not-yet-known keys still loads under this binary: the
	// forward-compat contract that lets up/down/healthcheck/timeout land later.
	body := `
version: 2
levels:
  - name: unit
    run: make test
    timeout: 60s
    healthcheck: curl localhost
`
	p, err := Load(writePyramid(t, body))
	if err != nil {
		t.Fatalf("unknown keys should be ignored, not error: %v", err)
	}
	if p == nil || len(p.Levels) != 1 || p.Levels[0].Name != "unit" {
		t.Fatalf("unknown-key file did not parse to the expected single level: %+v", p)
	}
}

func TestLoad_Invalid(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantSub string // a substring the error message should contain
	}{
		{
			name:    "empty levels",
			body:    "levels: []\n",
			wantSub: "empty",
		},
		{
			name:    "no levels key",
			body:    "# just a comment, no levels\n",
			wantSub: "empty",
		},
		{
			name: "duplicate names",
			body: `
levels:
  - name: unit
    run: make test
  - name: unit
    run: go test ./...
`,
			wantSub: "duplicate",
		},
		{
			name: "blank name",
			body: `
levels:
  - name: "   "
    run: make test
`,
			wantSub: "name is blank",
		},
		{
			name: "blank run",
			body: `
levels:
  - name: unit
    run: "   "
`,
			wantSub: "run is blank",
		},
		{
			name:    "malformed yaml",
			body:    "levels: [unit\n  - broken",
			wantSub: "parse pyramid",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Load(writePyramid(t, tc.body))
			if err == nil {
				t.Fatalf("present-but-invalid file should error, got pyramid %+v", p)
			}
			if p != nil {
				t.Errorf("invalid file should yield a nil pyramid, got %+v", p)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not mention %q", err.Error(), tc.wantSub)
			}
		})
	}
}
