package reconcile

import (
	"context"

	"github.com/Dawil/draiver/internal/reviewenv"
)

// sweepReviewEnvs runs one pass of the review-env reaper (drv-020): it tears down
// every active review env whose attempt is Done or has idled past ReviewEnvMaxAge,
// then re-runs the env's healthchecks to confirm it actually went away (a still-green
// probe is surfaced as a leak rather than silently assumed gone). It is a best-effort
// move — like sweepHealth it never fails the tick; the reviewenv.Manager logs and
// skips a bad record so one wedged env never stalls the sweep.
//
// The Manager is stateless beyond its Options, so one is constructed per tick and
// re-derives truth from each attempt's on-disk record plus git. Now is threaded
// through so a test can trip the reaper deterministically.
func (r *Reconciler) sweepReviewEnvs(ctx context.Context) {
	m := reviewenv.New(reviewenv.Options{
		Root:            r.opt.Root,
		ReviewLinkHosts: nil, // teardown reveals no URL; the host floor only gates launch
		MaxAge:          r.opt.ReviewEnvMaxAge,
		Now:             r.opt.Now,
		Logf:            r.opt.Logf,
	})
	reaped, err := m.Sweep(ctx)
	if err != nil {
		r.opt.Logf("reconcile: review-env sweep: %v", err)
		return
	}
	for _, k := range reaped {
		r.opt.Logf("reconcile: reaped review env %s/%s", k.Ticket, k.Attempt)
	}
}
