package metrics

import (
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
	for _, e := range a.Events {
		switch {
		case e.Type == "review":
			f.ReviewRoundTrips++
		case e.Type == "escalation":
			f.Escalations = append(f.Escalations, e.Body)
		case agentmsg.IsMessage(e):
			f.Messages++
		}
	}
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
