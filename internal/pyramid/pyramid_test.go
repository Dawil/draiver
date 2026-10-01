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

const validEnvLadder = `
environments:
  - name: local
    healthchecks:
      - name: postgres-up
        script: pg_isready -h localhost
  - name: staging
    up:   ./scripts/staging-up.sh
    down: ./scripts/staging-down.sh
    healthchecks:
      - name: api-reachable
        script: curl -fsS https://staging.example.com/healthz

levels:
  - name: unit
    run: go test ./...
  - name: integration
    run: go test -tags=integration ./...
    environment: local
  - name: e2e
    run: go test -tags=e2e ./...
    environment: staging
`

func TestLoad_ValidEnvironments(t *testing.T) {
	p, err := Load(writePyramid(t, validEnvLadder))
	if err != nil {
		t.Fatalf("valid env ladder should load: %v", err)
	}
	if len(p.Environments) != 2 {
		t.Fatalf("got %d environments, want 2", len(p.Environments))
	}
	// staging carries up/down and one healthcheck.
	staging := p.Environments[1]
	if staging.Name != "staging" || staging.Up == "" || staging.Down == "" {
		t.Errorf("staging env not parsed: %+v", staging)
	}
	if len(staging.Healthchecks) != 1 || staging.Healthchecks[0].Name != "api-reachable" {
		t.Errorf("staging healthchecks not parsed: %+v", staging.Healthchecks)
	}
	// local may omit up/down.
	if local := p.Environments[0]; local.Up != "" || local.Down != "" {
		t.Errorf("local env should have no up/down: %+v", local)
	}
}

func TestEnvironmentFor(t *testing.T) {
	p, err := Load(writePyramid(t, validEnvLadder))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	byName := map[string]Level{}
	for _, lv := range p.Levels {
		byName[lv.Name] = lv
	}

	// An unbound level (unit) resolves to the ambient context: ok is false.
	if env, ok := p.EnvironmentFor(byName["unit"]); ok {
		t.Errorf("unit binds no environment, got %+v", env)
	}
	// A bound level resolves to its named environment.
	if env, ok := p.EnvironmentFor(byName["integration"]); !ok || env.Name != "local" {
		t.Errorf("integration should resolve to local, got %+v ok=%v", env, ok)
	}
	if env, ok := p.EnvironmentFor(byName["e2e"]); !ok || env.Name != "staging" {
		t.Errorf("e2e should resolve to staging, got %+v ok=%v", env, ok)
	}
	// A hand-built level naming nothing-declared resolves to (nil, false).
	if env, ok := p.EnvironmentFor(Level{Name: "x", Environment: "ghost"}); ok {
		t.Errorf("unknown env name must not resolve, got %+v", env)
	}
}

func TestLoad_InvalidEnvironments(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantSub string
	}{
		{
			name: "level names an undeclared environment",
			body: `
levels:
  - name: unit
    run: go test ./...
    environment: nope
`,
			wantSub: `environment "nope" is not declared`,
		},
		{
			name: "duplicate environment names",
			body: `
environments:
  - name: local
    healthchecks: []
  - name: local
    healthchecks: []
levels:
  - name: unit
    run: go test ./...
`,
			wantSub: "duplicate name",
		},
		{
			name: "blank environment name",
			body: `
environments:
  - name: "  "
    healthchecks: []
levels:
  - name: unit
    run: go test ./...
`,
			wantSub: "environment 0: name is blank",
		},
		{
			name: "blank healthcheck name",
			body: `
environments:
  - name: local
    healthchecks:
      - name: "  "
        script: pg_isready
levels:
  - name: unit
    run: go test ./...
`,
			wantSub: "name is blank",
		},
		{
			name: "blank healthcheck script",
			body: `
environments:
  - name: local
    healthchecks:
      - name: db
        script: "  "
levels:
  - name: unit
    run: go test ./...
`,
			wantSub: "script is blank",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Load(writePyramid(t, tc.body))
			if err == nil {
				t.Fatalf("invalid env file should error, got %+v", p)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not mention %q", err.Error(), tc.wantSub)
			}
		})
	}
}

// An empty environments list, and a healthchecks list left empty, are both valid:
// backward compatibility and "an env may need no probes" respectively.
func TestLoad_EmptyEnvironmentsIsValid(t *testing.T) {
	body := `
environments:
  - name: local
    healthchecks: []
levels:
  - name: unit
    run: go test ./...
    environment: local
`
	if _, err := Load(writePyramid(t, body)); err != nil {
		t.Fatalf("an env with no healthchecks should be valid: %v", err)
	}
}

// threeRung is a bottom-to-top ladder used by the fold tests; "e2e" is the target.
var threeRung = &Pyramid{Levels: []Level{
	{Name: "unit", Run: "x"},
	{Name: "integration", Run: "x"},
	{Name: "e2e", Run: "x"},
}}

func TestHighestAtHEAD(t *testing.T) {
	const head = "cafef00d"
	const old = "deadbeef"
	cases := []struct {
		name    string
		results []Result
		want    string // rung name, or "" when ok is false
	}{
		{
			name: "no results at all",
			want: "",
		},
		{
			name:    "only a stale result — a later commit invalidated it",
			results: []Result{{Rung: "e2e", Commit: old}},
			want:    "",
		},
		{
			name:    "single rung green at HEAD",
			results: []Result{{Rung: "integration", Commit: head}},
			want:    "integration",
		},
		{
			name: "highest of several at HEAD wins regardless of log order",
			results: []Result{
				{Rung: "e2e", Commit: head},
				{Rung: "unit", Commit: head},
				{Rung: "integration", Commit: head},
			},
			want: "e2e",
		},
		{
			name: "a stale top rung does not outrank a fresh lower rung",
			results: []Result{
				{Rung: "e2e", Commit: old},          // stale — the claim-invalidating case
				{Rung: "integration", Commit: head}, // the real highest at HEAD
			},
			want: "integration",
		},
		{
			name:    "a rung not in this pyramid is ignored",
			results: []Result{{Rung: "smoke", Commit: head}},
			want:    "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lv, ok := threeRung.HighestAtHEAD(tc.results, head)
			if tc.want == "" {
				if ok {
					t.Fatalf("want no match, got rung %q", lv.Name)
				}
				return
			}
			if !ok {
				t.Fatalf("want rung %q, got no match", tc.want)
			}
			if lv.Name != tc.want {
				t.Errorf("highest at HEAD = %q, want %q", lv.Name, tc.want)
			}
		})
	}
}
