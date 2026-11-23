package web

import (
	"bytes"
	"net/http"
	"strings"

	"github.com/Dawil/draiver/internal/land"
	"github.com/Dawil/draiver/internal/systemlog"
)

// handleGitPush pushes the attempt's branch to its default remote (POST
// /ticket/{id}/{attempt}/git/push) — the first of the drv-013 Git controls that
// drive the attempt's local git folder from the board. Like every web write it
// goes through internal/land in-process (land.Push — git only, no forge API, so it
// opens no PR), honouring the package's invariant that the web layer never runs
// git itself. Push records a durable `note`; the response carries the re-rendered
// log region plus an OOB result banner with the push report. Non-terminal: it does
// not change the attempt's state.
func (s *Server) handleGitPush(w http.ResponseWriter, r *http.Request) {
	s.handleGitVerb(w, r, "push", func(id, att string) (gitResultVM, error) {
		// Bare remote (""): the button offers no NAME field, so land picks the sole
		// remote or the config's primary_remote (the drv-011 default-remote stopgap).
		res, err := land.Push(r.Context(), s.gw, id, att, "", "")
		if err != nil {
			// Push keeps its existing behaviour (drv-022 hardens Sync, not Push): a
			// failure is a server fault surfaced via s.fail, so return the error rather
			// than a banner.
			return gitResultVM{}, err
		}
		return gitResultVM{Kind: "ok", Message: strings.Join(land.FormatPushReport(id, att, res), "\n")}, nil
	})
}

// handleGitSync pulls the remote's default branch into the local base and
// back-merges it into the attempt's branch (POST /ticket/{id}/{attempt}/git/sync)
// — the Sync of the drv-013 Git controls, over internal/land Sync (the same
// operation `draiver ctl sync` runs). It fetches the base from the default remote
// and fast-forwards the local base (best-effort), then back-merges that base into
// the branch; the back-merge needs a clean checkout (enforced in worktree.Sync) and
// on conflict aborts and surfaces the error. On success it records a `note` (unless
// nothing moved) and returns the re-rendered log region plus an OOB result banner.
// Non-terminal: the attempt stays in its current state.
func (s *Server) handleGitSync(w http.ResponseWriter, r *http.Request) {
	s.handleGitVerb(w, r, "sync", func(id, att string) (gitResultVM, error) {
		// Bare remote (""): land picks the sole remote or the config's primary_remote
		// (the drv-011 default-remote stopgap); no config path uses the default.
		res, err := land.Sync(r.Context(), s.gw, id, att, "", "")
		if err != nil {
			// drv-022: a failing sync (dirty checkout, merge conflict, bookkeeping) is an
			// expected *outcome* of the control, not a server fault — map it into the same
			// git-result banner, styled as a failure carrying the error, at HTTP 200 so
			// the htmx swap lands. This is the fix for the old s.fail(500) that left the
			// panel blank. Returning no error keeps s.fail for genuine faults only.
			return gitResultVM{Kind: "fail", Message: "Sync failed — " + cleanGitErr(err)}, nil
		}
		// A pure no-op (nothing pulled, nothing back-merged) records no note; show an
		// explicit "already up to date" success rather than the technical report lines,
		// so the banner reads as a deliberate outcome, never "nothing happened".
		if !res.Recorded {
			return gitResultVM{Kind: "noop", Message: "Already up to date — nothing to pull or back-merge."}, nil
		}
		return gitResultVM{Kind: "ok", Message: strings.Join(land.FormatSyncReport(id, att, res), "\n")}, nil
	})
}

// cleanGitErr renders a land/worktree error for the failure banner: it trims the
// internal "worktree: " / "land: " prefixes so the user-facing message leads with
// the cause (a dirty checkout, a conflict) rather than package plumbing.
func cleanGitErr(err error) string {
	msg := err.Error()
	for _, p := range []string{"worktree: ", "land: "} {
		msg = strings.TrimPrefix(msg, p)
	}
	return msg
}

// gitResultVM drives the "git-result" banner a Git-control POST swaps onto the
// panel (drv-022). Kind — "ok" (a real action completed), "noop" (nothing to do),
// or "fail" (the control errored) — styles the banner so success, a no-op, and a
// failure are visually distinct; Message is the plain CLI report text, HTML-escaped
// by the template and never routed through markdown.
type gitResultVM struct {
	Kind    string
	Message string
}

// handleGitVerb is the shared machinery of the Git-control POSTs (push, sync): the
// same-origin guard, the attempt-exists check, the in-process land call (run), and
// the one-response-body double swap — the re-rendered attempt-live fragment (log
// region + OOB badge/count) plus the OOB "git-result" banner carrying run's result.
// These verbs are non-terminal — the log region re-renders but the attempt's state
// does not flip. run returns the banner to display (success, no-op, OR a handled
// failure — see handleGitSync), or an error reserved for a genuine server fault,
// surfaced via s.fail (500) exactly like the other writes.
func (s *Server) handleGitVerb(w http.ResponseWriter, r *http.Request, verb string, run func(id, att string) (gitResultVM, error)) {
	if !sameOrigin(r) {
		http.Error(w, "draiver: cross-origin request refused", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	att := r.PathValue("attempt")
	if !s.root.AttemptExists(id, att) {
		http.NotFound(w, r)
		return
	}
	result, err := run(id, att)
	// The verb may have moved the branch (push records, sync back-merges): drop the
	// cached git state so the re-render below reflects it (drvweb-030).
	s.invalidateGit(id, att)
	if err != nil {
		s.fail(w, err)
		return
	}
	// drv-045: record the git control as a system event — the verb and its outcome
	// (ok / noop / fail) from the result banner. This is the operational view of the
	// action; a non-noop sync still records its own durable `note` separately.
	s.emitSystem(id, att, gitSystemEvent(verb, result))
	// drv-048: the summary above keeps only the first line (truncateFirstLine); relay
	// the rest of the report — everything the old summary dropped — as its own system
	// lines so a failed push/sync shows its full output (a merge conflict, a reject)
	// in the panel, not just the truncated headline. A single-line result relays
	// nothing (no duplication of the headline). A failure reads red.
	if body := trimFirstLine(result.Message); body != "" {
		s.relaySystemOutput(id, att, systemlog.ClassGit, body, result.Kind == "fail")
	}
	vm, err := s.detail(id, att)
	if err != nil {
		s.fail(w, err)
		return
	}
	live, err := s.executeLive(vm)
	if err != nil {
		s.fail(w, err)
		return
	}
	var buf bytes.Buffer
	buf.Write(live)
	if err := s.tmpl.ExecuteTemplate(&buf, "git-result", result); err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(buf.Bytes())
}

// gitSystemEvent composes the drv-045 system-log record for a git control: a
// ClassGit event naming the verb (push/sync) and the outcome the banner reported.
// A "fail" outcome takes the error role so it reads red in the panel; ok/noop are
// neutral system lines. The first line of the banner message is kept for the log so
// the record stays a single skimmable line.
func gitSystemEvent(verb string, result gitResultVM) systemlog.SystemEvent {
	outcome := result.Kind
	if outcome == "" {
		outcome = "ok"
	}
	summary := truncateFirstLine(result.Message, 100)
	msg := "git " + verb + ": " + outcome
	if summary != "" {
		msg += " — " + summary
	}
	return systemlog.SystemEvent{
		Class:   systemlog.ClassGit,
		Message: msg,
		Fields:  map[string]string{"verb": verb, "outcome": outcome},
		Error:   result.Kind == "fail",
	}
}

// trimFirstLine returns everything AFTER the first line of s (the complement of
// truncateFirstLine's headline), with surrounding blank lines trimmed — the git
// report body the one-line summary event drops, relayed verbatim into the System
// Logs stream (drv-048). A single-line (or empty) report yields "", so nothing is
// relayed and the headline is not duplicated.
func trimFirstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return strings.Trim(s[i+1:], "\r\n")
	}
	return ""
}

// truncateFirstLine keeps the first line of s, trimmed, capped to max runes with an
// ellipsis if anything was dropped — so a multi-line git report collapses to one
// skimmable system-log line.
func truncateFirstLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}
