package pyramid

import (
	"strings"
	"testing"
)

// A promotions: section parses: each promotion keeps its environment, base, and qa,
// and resolves via Promotion(env). An absent section is a no-op (nil slice), so older
// files stay valid — the forward/backward-compatibility AC.
func TestLoad_PromotionsParse(t *testing.T) {
	body := `
environments:
  - name: dev
    url: https://dev.example.internal/
    healthchecks:
      - name: reachable
        script: "true"
  - name: staging
    healthchecks: []
levels:
  - name: unit
    run: go test ./...
promotions:
  - environment: dev
    base: dev-branch
    qa: go test -tags=e2e ./...
  - environment: staging
    qa: "true"
`
	p, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("valid promotions should parse: %v", err)
	}
	if len(p.Promotions) != 2 {
		t.Fatalf("want 2 promotions, got %d", len(p.Promotions))
	}
	dev, ok := p.Promotion("dev")
	if !ok {
		t.Fatal("Promotion(dev) should resolve")
	}
	if dev.Base != "dev-branch" || dev.QA != "go test -tags=e2e ./..." {
		t.Errorf("dev promotion fields wrong: %+v", dev)
	}
	// A blank name and an undeclared name do not resolve.
	if _, ok := p.Promotion("  "); ok {
		t.Error("blank promotion name must not resolve")
	}
	if _, ok := p.Promotion("ghost"); ok {
		t.Error("undeclared promotion name must not resolve")
	}
}

// A file with no promotions: key is valid and yields no promotions — the
// backward-compatibility case (older binaries / older repos).
func TestLoad_NoPromotionsIsValid(t *testing.T) {
	body := `
levels:
  - name: unit
    run: go test ./...
`
	p, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("a pyramid with no promotions must be valid: %v", err)
	}
	if len(p.Promotions) != 0 {
		t.Errorf("want no promotions, got %d", len(p.Promotions))
	}
}

func TestLoad_InvalidPromotions(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantSub string
	}{
		{
			name: "promotion names an undeclared environment",
			body: `
environments:
  - name: dev
    healthchecks: []
levels:
  - name: unit
    run: go test ./...
promotions:
  - environment: ghost
    qa: "true"
`,
			wantSub: `environment "ghost" is not declared`,
		},
		{
			name: "blank environment",
			body: `
environments:
  - name: dev
    healthchecks: []
levels:
  - name: unit
    run: go test ./...
promotions:
  - environment: "  "
    qa: "true"
`,
			wantSub: "environment is blank",
		},
		{
			name: "blank qa",
			body: `
environments:
  - name: dev
    healthchecks: []
levels:
  - name: unit
    run: go test ./...
promotions:
  - environment: dev
    qa: "  "
`,
			wantSub: "qa is blank",
		},
		{
			name: "duplicate environment",
			body: `
environments:
  - name: dev
    healthchecks: []
levels:
  - name: unit
    run: go test ./...
promotions:
  - environment: dev
    qa: "true"
  - environment: dev
    qa: "false"
`,
			wantSub: `duplicate environment "dev"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Parse([]byte(tc.body))
			if err == nil {
				t.Fatalf("invalid promotion should error, got %+v", p)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not mention %q", err.Error(), tc.wantSub)
			}
		})
	}
}

// PromotedAtHEAD folds promotion results into the set of environments green at the
// current commit: a result at HEAD counts, a stale (older-commit) result does not, and
// a result naming an environment with no declared promotion is ignored.
func TestPromotedAtHEAD(t *testing.T) {
	const head = "cafef00d"
	const old = "deadbeef"
	p := &Pyramid{
		Environments: []Environment{{Name: "dev"}, {Name: "staging"}},
		Promotions: []Promotion{
			{Environment: "dev", QA: "true"},
			{Environment: "staging", QA: "true"},
		},
	}
	cases := []struct {
		name    string
		results []PromotionResult
		want    []string // environments expected green at HEAD
	}{
		{name: "nothing recorded", want: nil},
		{
			name:    "a pass at HEAD counts",
			results: []PromotionResult{{Environment: "dev", Commit: head}},
			want:    []string{"dev"},
		},
		{
			name:    "a stale pass does not count",
			results: []PromotionResult{{Environment: "dev", Commit: old}},
			want:    nil,
		},
		{
			name: "fresh and stale together — only the fresh one",
			results: []PromotionResult{
				{Environment: "dev", Commit: head},
				{Environment: "staging", Commit: old},
			},
			want: []string{"dev"},
		},
		{
			name:    "a result for an undeclared promotion is ignored",
			results: []PromotionResult{{Environment: "ghost", Commit: head}},
			want:    nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := p.PromotedAtHEAD(tc.results, head)
			if len(got) != len(tc.want) {
				t.Fatalf("green=%v, want %v", got, tc.want)
			}
			for _, env := range tc.want {
				if !got[env] {
					t.Errorf("expected %q green at HEAD, got %v", env, got)
				}
			}
		})
	}
}
