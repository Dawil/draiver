package web

import (
	"bytes"
	"fmt"
	"html/template"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
)

// stateLabels overrides how a control state is shown in the human-facing web UI.
// The domain vocabulary (project.State, surfaced by the CLI, state.md, and brief)
// is unchanged; only the board column and the detail badge read differently.
var stateLabels = map[project.State]string{
	project.NeedsMe: "Stuck",
}

// stateLabel is the presentation label for a state on the board and detail views.
func stateLabel(s project.State) string {
	if l, ok := stateLabels[s]; ok {
		return l
	}
	return string(s)
}

// badgeVM drives the reusable "state-badge" partial. OOB marks the copy the live
// fragment emits with hx-swap-oob so a single poll updates the header badge that
// lives outside the swapped log region.
type badgeVM struct {
	State project.State
	OOB   bool
}

// cardHref is a board card's link target. A Stuck or Review card deep-links to
// the log entry that put it there — the latest event, which is the open
// escalation (Stuck) or the review claim (Review) — so one click lands the human
// on and highlights the exact entry needing attention. Running/Done cards link
// to the attempt with no fragment.
func cardHref(a project.Attempt) string {
	base := "/ticket/" + a.Ticket + "/" + a.ID
	switch a.State {
	case project.NeedsMe, project.Review:
		if n := len(a.Events); n > 0 {
			return fmt.Sprintf("%s#event-%d", base, a.Events[n-1].Seq)
		}
	}
	return base
}

// linkVM is a hyperlink surfaced on a served page — a board-card review action or
// a detail-timeline chip. Rel is the opaque, forge-neutral label (pr, mr, diff,
// ci, …) used only to pick a primary and to label a chip; Href is the URL, always
// re-checked against the render-time scheme floor before it becomes a live href.
type linkVM struct {
	Rel  string
	Href string
}

// allowedHREFScheme reports whether raw may be emitted as a live href on the
// board: an absolute http/https URL. This is re-applied at render for every link
// the board serves, independent of the append-time event.ValidateLink check —
// defense in depth, so a link that reached disk by any path (an older event, a
// hand-edited file) still cannot produce a javascript:/data:/file: href. The
// board never fetches or previews raw; this is a scheme test, not an integration.
func allowedHREFScheme(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return true
	}
	return false
}

// primaryLink picks the review link to feature on a card: the first whose rel is
// "pr" or "mr" (a merge/pull request outranks a bare diff/ci link), else the
// first link — considering only links that pass the render-time scheme floor. It
// returns nil when no link survives.
func primaryLink(links []event.Link) *linkVM {
	var first *linkVM
	for _, l := range links {
		if !allowedHREFScheme(l.Href) {
			continue
		}
		vm := linkVM{Rel: l.Rel, Href: l.Href}
		if first == nil {
			first = &vm
		}
		switch strings.ToLower(l.Rel) {
		case "pr", "mr":
			return &vm
		}
	}
	return first
}

// reviewLink returns the primary review link to surface as a board card action,
// or nil when there is none. It is scoped to the Review column: only a Review
// attempt's latest `review` event is consulted, so a reopened (Running) or
// blocked (Stuck) attempt never shows a stale PR button. A direct-merge flow with
// no PR carries no link and the card degrades to no action.
func reviewLink(a project.Attempt) *linkVM {
	if a.State != project.Review {
		return nil
	}
	for i := len(a.Events) - 1; i >= 0; i-- {
		if a.Events[i].Type == "review" {
			return primaryLink(a.Events[i].Links)
		}
	}
	return nil
}

// safeLinks maps an event's links to chip view models, dropping any whose scheme
// fails the render-time floor. The chips render as live hrefs, so the floor is
// re-applied here (defense in depth) rather than trusting the stored value.
func safeLinks(links []event.Link) []linkVM {
	var out []linkVM
	for _, l := range links {
		if allowedHREFScheme(l.Href) {
			out = append(out, linkVM{Rel: l.Rel, Href: l.Href})
		}
	}
	return out
}

// linkPolicy is a goldmark AST transformer enforcing the render-time scheme floor
// on markdown body hyperlinks. goldmark's default already blanks javascript:/
// vbscript:/file: and non-image data: destinations, but it admits data:image/
// {png,gif,jpeg,webp}; this closes that carve-out so a Link/Image destination is
// http/https (or a scheme-less relative URL) only, and a disallowed autolink is
// demoted to plain text. It is defense in depth, independent of the append-time
// event.ValidateLink check.
type linkPolicy struct{}

func (linkPolicy) Transform(doc *ast.Document, reader text.Reader, _ parser.Context) {
	src := reader.Source()
	var demote []*ast.AutoLink
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch v := n.(type) {
		case *ast.Link:
			if !bodyDestOK(v.Destination) {
				v.Destination = nil
			}
		case *ast.Image:
			if !bodyDestOK(v.Destination) {
				v.Destination = nil
			}
		case *ast.AutoLink:
			if !bodyDestOK(v.URL(src)) {
				demote = append(demote, v)
			}
		}
		return ast.WalkContinue, nil
	})
	// An autolink has no settable destination, so a disallowed one is replaced by
	// its literal label as escaped text — no live href survives. Done after the
	// walk so the tree is not mutated mid-traversal.
	for _, a := range demote {
		if p := a.Parent(); p != nil {
			p.ReplaceChild(p, a, ast.NewString(a.Label(src)))
		}
	}
}

// bodyDestOK reports whether a markdown link/image/autolink destination may render
// as a live href: a scheme-less relative URL (nothing to abuse) or an http/https
// absolute URL. Everything else — javascript:, data:, vbscript:, file: — is
// blanked or demoted by linkPolicy.
func bodyDestOK(raw []byte) bool {
	u, err := url.Parse(strings.TrimSpace(string(raw)))
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "", "http", "https":
		return true
	}
	return false
}

// groupInt formats a non-negative token count with thousands separators, so a
// six-figure token tally stays readable in the cache panel (1234567 → "1,234,567").
func groupInt(n int) string {
	s := strconv.Itoa(n)
	if n < 0 {
		return s // token counts are non-negative; don't try to group a sign
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// relativeAge renders a duration-since as a human "time ago" phrase. It is the
// server-side twin of static/reltime.js (which keeps the age live on the
// client); keep the two bucket boundaries and wording in sync. A future stamp
// (clock skew) clamps to "just now".
func relativeAge(d time.Duration) string {
	switch {
	case d < 5*time.Second:
		return "just now"
	case d < time.Minute:
		return agoPlural(int(d.Seconds()), "second")
	case d < time.Hour:
		return agoPlural(int(d.Minutes()), "minute")
	case d < 24*time.Hour:
		return agoPlural(int(d.Hours()), "hour")
	case d < 48*time.Hour:
		return "yesterday"
	case d < 7*24*time.Hour:
		return agoPlural(int(d.Hours()/24), "day")
	case d < 30*24*time.Hour:
		return agoPlural(int(d.Hours()/(24*7)), "week")
	case d < 365*24*time.Hour:
		return agoPlural(int(d.Hours()/(24*30)), "month")
	default:
		return agoPlural(int(d.Hours()/(24*365)), "year")
	}
}

// agoPlural formats "N unit(s) ago" with singular/plural agreement.
func agoPlural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit + " ago"
	}
	return strconv.Itoa(n) + " " + unit + "s ago"
}

// toHTML renders event/spec markdown to HTML. goldmark's default config does not
// pass through raw HTML, and linkPolicy (wired in New) blanks any link/image/
// autolink whose scheme is outside the http/https floor, so the result is safe to
// embed even though bodies are agent-supplied, attacker-influenceable content.
func (s *Server) toHTML(md string) template.HTML {
	var buf bytes.Buffer
	if err := s.md.Convert([]byte(md), &buf); err != nil {
		return template.HTML(template.HTMLEscapeString(md))
	}
	return template.HTML(buf.String())
}

// inlineMD renders a short, single-line markdown string (a board card's
// WaitingReason / wake reason — e.g. "escalated to `P`") to inline HTML, stripping
// goldmark's block <p> wrapper so it drops cleanly into a <span>. It fixes the
// drvweb-023 board bug where such a reason's backticks rendered literally because
// the template interpolated the raw string instead of running it through the
// markdown pipeline. Safety is identical to toHTML (goldmark passes no raw HTML;
// linkPolicy enforces the scheme floor); on a convert error it falls back to the
// HTML-escaped literal. Registered as a template func so the one-line reasons the
// board holds as plain strings render in place without a pre-rendered VM field.
func (s *Server) inlineMD(md string) template.HTML {
	if md == "" {
		return ""
	}
	var buf bytes.Buffer
	if err := s.md.Convert([]byte(md), &buf); err != nil {
		return template.HTML(template.HTMLEscapeString(md))
	}
	out := strings.TrimSpace(buf.String())
	out = strings.TrimPrefix(out, "<p>")
	out = strings.TrimSuffix(out, "</p>")
	return template.HTML(out)
}

func (s *Server) renderSpec(id string) template.HTML {
	data, err := readSpecBody(s.root.SpecPath(id))
	if err != nil {
		return template.HTML("<p class=\"muted\">(no spec.md)</p>")
	}
	return s.toHTML(data)
}
