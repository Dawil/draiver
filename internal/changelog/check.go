package changelog

import (
	"fmt"
	"os"
	"path/filepath"
)

// Check is the `changelog` rung: it validates every fragment in the tree (so a
// malformed fragment can never break a later Render) and requires the ticket's own
// fragment to exist. ticket must be non-empty; resolving it is the caller's job.
// It returns a nil error only when both hold. The returned message names the
// expected fragment path when it is missing.
func Check(root, ticket string) (Fragment, error) {
	if ticket == "" {
		return Fragment{}, fmt.Errorf("no ticket id: set DRAIVER_TICKET (draiver test exports it) or run from a draiver/<ticket>/<attempt> branch")
	}
	t, loadErr := Load(root)
	if t == nil {
		return Fragment{}, fmt.Errorf("read %s: %v", Dir, loadErr)
	}
	fr, found := t.Find(ticket)
	if !found {
		// A present-but-malformed fragment is reported by loadErr; only call it missing
		// when there is genuinely no file.
		if _, err := os.Stat(filepath.Join(root, FragmentPath(ticket))); err != nil {
			missing := fmt.Errorf("missing changelog fragment for %s: expected %s (format: %s)",
				ticket, FragmentPath(ticket), filepath.Join(Dir, ReadmeFile))
			if loadErr != nil {
				return Fragment{}, fmt.Errorf("%v\nalso, malformed fragments:\n%v", missing, loadErr)
			}
			return Fragment{}, missing
		}
	}
	if loadErr != nil {
		return fr, fmt.Errorf("malformed changelog fragment(s):\n%v", loadErr)
	}
	return fr, nil
}
