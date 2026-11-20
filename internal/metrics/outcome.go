package metrics

import (
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Dawil/draiver/internal/agentmsg"
	"github.com/Dawil/draiver/internal/project"
)

// Outcomes. They are derived from the log and the control state, never from the
// transcript.
const (
	OutcomeRatified            = "ratified"
	OutcomeInReview            = "in-review"
	OutcomeRejected            = "rejected"
	OutcomeEscalatedUnresolved = "escalated-unresolved"
	OutcomeAbandoned           = "abandoned"
	OutcomeRunning             = "running"
)

// LogFacts is the log-derived (outcome) family of an attempt's metrics.
type LogFacts struct {
	Outcome          string
	Running          bool // excluded by default: its numbers are partial
	ReviewRoundTrips int
	Escalations      []string
	Messages         int // human→agent messages posted to the session
	ContextAutoStops int // escalations tagged context-auto-stop (the limit gate's)
	// Endings are the log events that can end a session, in log order.
	Endings    []Ending
	RoundTrips []RoundTrip
}

// Ending is a log event that can end a session: a review claim, an escalation (a
// context-auto-stop is one, tagged), a park, or done.
type Ending struct {
	TS   time.Time
	Kind string // context-auto-stop | review | escalation | park | done
}

// TagContextAutoStop is the tag the limit gate puts on the escalation it raises when
// a session runs out of context (internal/limit.TagContextAutoStop; not imported, to
// keep metrics free of the gate's dependencies).
const TagContextAutoStop = "context-auto-stop"

// SessionEndSlack is how far before a session's last transcript event a log ending
// may fall and still count as that session's end. An agent's own `draiver review`
// or `escalate` is logged while its turn is still finishing, so its timestamp
// precedes the transcript's closing frames.
const SessionEndSlack = 10 * time.Minute

// EndedBy picks what ended a session whose transcript ran until end, given the start
// of the next session (zero for the last). It considers log endings with a timestamp
// in [end-SessionEndSlack, nextStart). A context-auto-stop among them wins; otherwise
// the latest one does; with none it is "none".
func EndedBy(endings []Ending, end, nextStart time.Time) string {
	if end.IsZero() {
		return "none"
	}
	lo := end.Add(-SessionEndSlack)
	got := "none"
	for _, e := range endings {
		if e.TS.Before(lo) || (!nextStart.IsZero() && !e.TS.Before(nextStart)) {
			continue
		}
		if e.Kind == TagContextAutoStop {
			return e.Kind
		}
		got = e.Kind
	}
	return got
}

// FromLog derives the outcome family from a loaded attempt.
//
//   - ratified: the attempt reached Done.
//   - in-review: its latest lifecycle event is an open review claim.
//   - escalated-unresolved: an escalation is still open.
//   - rejected: a review claim was followed by more work (the attempt is back to
//     Running) or by abandonment (archived without Done). A rejected claim whose
//     rework later lands is ratified, and shows as review_round_trips > 1. A
//     rejected attempt that is back at work is still running, so it is partial.
//   - abandoned: archived without Done and without ever claiming review.
//   - running: none of the above. Work is still under way, so the numbers are
//     partial.
func FromLog(a project.Attempt) LogFacts {
	var f LogFacts
	superseded := map[int]bool{}
	for _, e := range a.Events {
		for _, s := range e.Supersedes {
			superseded[s] = true
		}
	}
	var cur *RoundTrip
	for _, e := range a.Events {
		switch {
		case e.Type == "review":
			f.ReviewRoundTrips++
			f.Endings = append(f.Endings, Ending{e.TS, "review"})
			if cur != nil {
				cur.NextReviewSeq = e.Seq
			}
			f.RoundTrips = append(f.RoundTrips, RoundTrip{ReviewSeq: e.Seq})
			cur = &f.RoundTrips[len(f.RoundTrips)-1]
		case e.Type == "escalation":
			f.Escalations = append(f.Escalations, e.Body)
			kind := "escalation"
			if slices.Contains(e.Tags, TagContextAutoStop) {
				f.ContextAutoStops++
				kind = TagContextAutoStop
			}
			f.Endings = append(f.Endings, Ending{e.TS, kind})
		case e.Type == "park" || e.Type == "done":
			f.Endings = append(f.Endings, Ending{e.TS, e.Type})
		case agentmsg.IsMessage(e):
			f.Messages++
		}
		if cur != nil && e.Seq > cur.ReviewSeq && strings.HasPrefix(e.Actor, "human:") &&
			(e.Type == "decision" || e.Type == "note") {
			cur.HumanEvents++
			if superseded[e.Seq] {
				cur.HumanSuperseded++
			}
		}
	}
	sort.SliceStable(f.Endings, func(i, j int) bool { return f.Endings[i].TS.Before(f.Endings[j].TS) })
	switch {
	case a.State == project.Done:
		f.Outcome = OutcomeRatified
	case a.Archived && f.ReviewRoundTrips > 0:
		f.Outcome = OutcomeRejected
	case a.Archived:
		f.Outcome = OutcomeAbandoned
	case a.State == project.Review:
		f.Outcome = OutcomeInReview
	case a.State == project.NeedsMe:
		f.Outcome = OutcomeEscalatedUnresolved
	case f.ReviewRoundTrips > 0:
		f.Outcome = OutcomeRejected
		f.Running = true
	default:
		f.Outcome = OutcomeRunning
		f.Running = true
	}
	return f
}
