package project

import (
	"path/filepath"
	"strings"

	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/store"
)

// CodebaseLedger is a pointer to a `type: codebase` ticket (drv-037) bound to a
// repo. The cold-start brief surfaces it so a worker knows a repo-wide belief
// store exists to consult — the read side of the codebase-ledger epic (drv-035).
// It is a pointer, not the ledger's facts: those are pulled on demand via
// `recall`, so cold-start cost stays flat whether or not a ledger is bound.
type CodebaseLedger struct {
	Ticket string
	Title  string
}

// CodebaseLedgersFor resolves the codebase ledgers bound to repo, excluding
// selfTicket (so a ledger briefed in its own right never lists itself).
//
// The binding is **repo-derived** (drv-038, resolution #4): a ledger is a
// `type: codebase` ticket whose *defining* (earliest) attempt targets the same
// repo — the `--repo` captured by `new --type codebase`. Later perspective
// attempts (an agent recording findings under its own scoped attempt) share that
// repo, so the earliest attempt is the ledger's identity repo. Repo paths are
// compared `filepath.Clean`-normalised.
//
// More than one match is not an error: the human framed ledgers as a library the
// worker may draw on, so every bound ledger is returned (sorted by ticket id, the
// ListTickets order). A ledger whose defining attempt is archived, or carries no
// repo, is skipped; an empty repo or no match returns nil — the brief then renders
// no ledger section and behaviour is unchanged (AC4).
func CodebaseLedgersFor(root store.Root, repo, selfTicket string) ([]CodebaseLedger, error) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return nil, nil
	}
	want := filepath.Clean(repo)
	tickets, err := root.ListTickets()
	if err != nil {
		return nil, err
	}
	var out []CodebaseLedger
	for _, t := range tickets {
		if t == selfTicket {
			continue
		}
		spec, err := loadSpecMeta(root.SpecPath(t))
		if err != nil {
			return nil, err
		}
		tt, err := NormalizeType(spec.Type)
		if err != nil {
			// An unrelated ticket with a malformed `type:` can never be a valid binding
			// target; skip it rather than fail every brief on this repo over its typo.
			continue
		}
		if tt != TypeCodebase {
			continue
		}
		ids, err := root.ListAttempts(t)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			continue
		}
		// The ledger's identity repo lives on its defining (earliest) attempt's
		// attempt.md, so a cheap meta read decides the match before loading the log.
		meta, err := attempt.LoadMeta(root, t, ids[0])
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(meta.Repo) == "" || filepath.Clean(meta.Repo) != want {
			continue
		}
		// Load the derived attempt only for a confirmed match — for the title and to
		// drop a retired (archived) ledger.
		a, err := LoadAttempt(root, t, ids[0])
		if err != nil {
			return nil, err
		}
		if a.Archived {
			continue
		}
		out = append(out, CodebaseLedger{Ticket: t, Title: a.Title})
	}
	return out, nil
}
