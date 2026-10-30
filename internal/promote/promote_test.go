package promote

import (
	"context"
	"io"
	"testing"

	"github.com/Dawil/draiver/internal/pyramid"
)

// RunQA maps the env-wrapped up→healthcheck→qa→down run onto the promotion fault
// taxonomy that mirrors `draiver test`: a green qa is StatusOK, a non-zero qa is
// StatusQAFailed (a code/promotion fault), a failed `up` is StatusEnvFault
// (infrastructure), and a red healthcheck is StatusBlocked (dependency not ready).
func TestRunQA_FaultTaxonomy(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		env   *pyramid.Environment
		promo pyramid.Promotion
		want  Status
		pass  bool
	}{
		{
			name:  "green qa, no env machinery",
			promo: pyramid.Promotion{Environment: "dev", QA: "true"},
			want:  StatusOK,
			pass:  true,
		},
		{
			name:  "qa fails → promotion fault",
			promo: pyramid.Promotion{Environment: "dev", QA: "exit 3"},
			want:  StatusQAFailed,
		},
		{
			name:  "up fails → infrastructure fault, qa not attempted",
			env:   &pyramid.Environment{Name: "dev", Up: "exit 1", Down: "true"},
			promo: pyramid.Promotion{Environment: "dev", QA: "true"},
			want:  StatusEnvFault,
		},
		{
			name: "red healthcheck → blocked, qa not attempted",
			env: &pyramid.Environment{Name: "dev", Up: "true",
				Healthchecks: []pyramid.Healthcheck{{Name: "ready", Script: "false"}}},
			promo: pyramid.Promotion{Environment: "dev", QA: "true"},
			want:  StatusBlocked,
		},
		{
			name: "green healthcheck then green qa",
			env: &pyramid.Environment{Name: "dev", Up: "true", Down: "true",
				Healthchecks: []pyramid.Healthcheck{{Name: "ready", Script: "true"}}},
			promo: pyramid.Promotion{Environment: "dev", QA: "true"},
			want:  StatusOK,
			pass:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := RunQA(ctx, io.Discard, io.Discard, t.TempDir(), tc.env, tc.promo, nil)
			if out.Status != tc.want {
				t.Fatalf("status = %q, want %q (lines: %v)", out.Status, tc.want, out.Lines)
			}
			if out.Status.Pass() != tc.pass {
				t.Errorf("Pass() = %v, want %v", out.Status.Pass(), tc.pass)
			}
		})
	}
}

// RunQA exports the DRAIVER_PROMOTE_* contract to every step so a repo's qa/healthcheck
// scripts can read the environment name, the tracked base branch, and the reachable URL
// without draiver hardcoding anything — the whole loose-coupling point.
func TestRunQA_ExportsPromoteContract(t *testing.T) {
	ctx := context.Background()
	env := &pyramid.Environment{Name: "dev", URL: "https://dev.example.internal/"}
	promo := pyramid.Promotion{
		Environment: "dev",
		Base:        "dev-branch",
		QA: `test "$DRAIVER_PROMOTE_ENV" = dev && ` +
			`test "$DRAIVER_PROMOTE_BASE" = dev-branch && ` +
			`test "$DRAIVER_PROMOTE_URL" = https://dev.example.internal/`,
	}
	out := RunQA(ctx, io.Discard, io.Discard, t.TempDir(), env, promo, nil)
	if out.Status != StatusOK {
		t.Fatalf("qa asserting the DRAIVER_PROMOTE_* contract should pass, got %q (%v)", out.Status, out.Lines)
	}
	if out.Environment != "dev" || out.Base != "dev-branch" {
		t.Errorf("outcome should carry env/base, got %+v", out)
	}
}

// extraEnv (the overlay execution contract) is layered beneath the DRAIVER_PROMOTE_*
// vars and visible to qa.
func TestRunQA_ExtraEnv(t *testing.T) {
	out := RunQA(context.Background(), io.Discard, io.Discard, t.TempDir(), nil,
		pyramid.Promotion{Environment: "dev", QA: `test "$OVERLAY_VAR" = present`},
		[]string{"OVERLAY_VAR=present"})
	if out.Status != StatusOK {
		t.Fatalf("extraEnv should reach qa, got %q (%v)", out.Status, out.Lines)
	}
}
