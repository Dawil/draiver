package metrics

import (
	"errors"
	"fmt"
	"os/exec"
)

// Split is the --split-at cohorting of codebase rows (drv-057): a clean before/after
// for one change, with no date window. An attempt is After when its recorded (or
// inferred) base_commit contains the split commit, Before when it does not, and
// Unknown when its base is not known or is not a commit in the codebase's repo —
// those are counted, never guessed into a cohort.
type Split struct {
	Commit  string // as given
	SHA     string // resolved in the codebase's repo
	Before  []Row
	After   []Row
	Unknown []Row
}

// SplitAt resolves commit in repo and partitions rows by whether their base contains
// it. An unresolvable commit is an error: a typo must not silently put every attempt
// in "before".
func SplitAt(repo, commit string, rows []Row) (Split, error) {
	sha, err := git(repo, "rev-parse", "--verify", "--quiet", commit+"^{commit}")
	if err != nil || sha == "" {
		return Split{}, fmt.Errorf("metrics: --split-at %q is not a commit in %s", commit, repo)
	}
	s := Split{Commit: commit, SHA: sha}
	for _, r := range rows {
		switch containsCommit(repo, r.Provenance.BaseCommit.Value, sha) {
		case cohortAfter:
			s.After = append(s.After, r)
		case cohortBefore:
			s.Before = append(s.Before, r)
		default:
			s.Unknown = append(s.Unknown, r)
		}
	}
	return s, nil
}

type cohort int

const (
	cohortUnknown cohort = iota
	cohortBefore
	cohortAfter
)

// containsCommit reports whether base has sha in its history (sha is base or an
// ancestor of it). An empty base, or one the repo does not have, is unknown.
func containsCommit(repo, base, sha string) cohort {
	if base == "" {
		return cohortUnknown
	}
	if b, err := git(repo, "rev-parse", "--verify", "--quiet", base+"^{commit}"); err != nil || b == "" {
		return cohortUnknown
	}
	_, err := git(repo, "merge-base", "--is-ancestor", sha, base)
	if err == nil {
		return cohortAfter
	}
	// --is-ancestor exits 1 for "no"; anything else is a git failure, not an answer.
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return cohortBefore
	}
	return cohortUnknown
}
