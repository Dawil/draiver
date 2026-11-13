package acceptance

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// steps_changelog_drv051.go realises drv-051's acceptance criteria
// (features/changelog/changelog-coverage.feature): every ticket id that prefixes a
// commit subject in the repo's history is the id of exactly one CHANGELOG.md entry,
// and those entries keep the shape drv-052 splits on. It reads the real repo — the
// git history reachable from HEAD and the checked-in CHANGELOG.md — so the check
// cannot drift from what ships. No browser, no screenshots.

func init() {
	drv051 := map[string]StepFunc{
		"the repository's git history and its CHANGELOG.md":                                   givenChangelogRepo,
		"I collect the ticket ids prefixing commit subjects and the ids of CHANGELOG entries": whenCollectChangelogIDs,
		"every commit-prefix ticket id is the id of exactly one CHANGELOG entry":              thenEveryTicketHasOneEntry,
		"every ticket entry sits under a Keep-a-Changelog heading inside a release section":   thenEntriesUnderHeadings,
		"no ticket entry's bold summary names a second ticket id":                             thenNoSecondIDInSummary,
	}
	for text, fn := range drv051 {
		registry[text] = fn
	}
}

var (
	// commitTicketRE is the ticket-id prefix of a commit subject (drv-051 spec, scope 1).
	commitTicketRE = regexp.MustCompile(`^drv(?:ctl|web)?-[0-9]+`)
	// anyTicketRE finds ticket ids anywhere in a bold summary.
	anyTicketRE = regexp.MustCompile(`drv(?:ctl|web)?-[0-9]+`)
	// entryIDRE is the id closing a bold summary: "… (drvweb-027)." or the older house
	// variant "… (`draiverctl`, drvctl-020).".
	entryIDRE = regexp.MustCompile(`(drv(?:ctl|web)?-[0-9]+)\)\.?$`)
)

// clEntry is one top-level CHANGELOG bullet.
type clEntry struct {
	line    int    // 1-based line of the "- **" opener
	summary string // bold summary text, newlines folded to spaces
	id      string // ticket id closing the summary ("" if none)
	release string // enclosing "## [...]" header
	heading string // enclosing "### ..." heading
}

type changelogWorld struct {
	root      string
	changelog string
	commitIDs []string // sorted, unique
	entries   []clEntry
	idEntries map[string][]clEntry
}

func givenChangelogRepo(w *World, sr *StepRun) error {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return fmt.Errorf("locate repo root: %v", err)
	}
	root := strings.TrimSpace(string(out))
	b, err := os.ReadFile(filepath.Join(root, "CHANGELOG.md"))
	if err != nil {
		return fmt.Errorf("read CHANGELOG.md: %v", err)
	}
	w.set("changelog", &changelogWorld{root: root, changelog: string(b)})
	return nil
}

func currentChangelog(w *World) (*changelogWorld, error) {
	cw, _ := w.get("changelog").(*changelogWorld)
	if cw == nil {
		return nil, fmt.Errorf("no changelog world: a Given step must run first")
	}
	return cw, nil
}

func whenCollectChangelogIDs(w *World, sr *StepRun) error {
	cw, err := currentChangelog(w)
	if err != nil {
		return err
	}
	out, err := exec.Command("git", "-C", cw.root, "log", "--format=%s", "HEAD").Output()
	if err != nil {
		return fmt.Errorf("git log: %v", err)
	}
	seen := map[string]bool{}
	for _, subj := range strings.Split(string(out), "\n") {
		if id := commitTicketRE.FindString(subj); id != "" && !seen[id] {
			seen[id] = true
			cw.commitIDs = append(cw.commitIDs, id)
		}
	}
	sort.Strings(cw.commitIDs)
	if len(cw.commitIDs) == 0 {
		return fmt.Errorf("no ticket-prefixed commit subjects found in the history (shallow clone?)")
	}
	cw.entries = parseChangelogEntries(cw.changelog)
	cw.idEntries = map[string][]clEntry{}
	for _, e := range cw.entries {
		if e.id != "" {
			cw.idEntries[e.id] = append(cw.idEntries[e.id], e)
		}
	}
	return nil
}

// parseChangelogEntries walks CHANGELOG.md, tracking the enclosing "## [..]" release
// and "### .." heading, and returns every top-level "- **…**" bullet. A bold summary
// may wrap across lines; it ends at the first closing "**".
func parseChangelogEntries(text string) []clEntry {
	lines := strings.Split(text, "\n")
	var (
		entries          []clEntry
		release, heading string
	)
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		switch {
		case strings.HasPrefix(l, "## "):
			release, heading = strings.TrimSpace(l[3:]), ""
			continue
		case strings.HasPrefix(l, "### "):
			heading = strings.TrimSpace(l[4:])
			continue
		case !strings.HasPrefix(l, "- **"):
			continue
		}
		rest := l[len("- **"):]
		var sb strings.Builder
		for j := i; ; {
			if k := strings.Index(rest, "**"); k >= 0 {
				sb.WriteString(rest[:k])
				break
			}
			sb.WriteString(rest)
			j++
			if j >= len(lines) || lines[j] == "" {
				break // unterminated bold: take what we have
			}
			sb.WriteString(" ")
			rest = strings.TrimSpace(lines[j])
		}
		sum := strings.TrimSpace(sb.String())
		e := clEntry{line: i + 1, summary: sum, release: release, heading: heading}
		if m := entryIDRE.FindStringSubmatch(sum); m != nil {
			e.id = m[1]
		}
		entries = append(entries, e)
	}
	return entries
}

func thenEveryTicketHasOneEntry(w *World, sr *StepRun) error {
	cw, err := currentChangelog(w)
	if err != nil {
		return err
	}
	var missing, dup []string
	for _, id := range cw.commitIDs {
		switch es := cw.idEntries[id]; {
		case len(es) == 0:
			missing = append(missing, id)
		case len(es) > 1:
			var at []string
			for _, e := range es {
				at = append(at, fmt.Sprintf("L%d", e.line))
			}
			dup = append(dup, id+" ("+strings.Join(at, ",")+")")
		}
	}
	var problems []string
	if len(missing) > 0 {
		problems = append(problems, fmt.Sprintf("%d ticket(s) with no CHANGELOG entry: %s",
			len(missing), strings.Join(missing, " ")))
	}
	if len(dup) > 0 {
		problems = append(problems, fmt.Sprintf("ticket(s) with more than one entry: %s",
			strings.Join(dup, " ")))
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}

// keepAChangelogHeadings are the change types Keep a Changelog 1.1.0 defines.
var keepAChangelogHeadings = map[string]bool{
	"Added": true, "Changed": true, "Deprecated": true,
	"Removed": true, "Fixed": true, "Security": true,
}

var releaseRE = regexp.MustCompile(`^\[(Unreleased|[0-9]+\.[0-9]+\.[0-9]+)\]`)

func thenEntriesUnderHeadings(w *World, sr *StepRun) error {
	cw, err := currentChangelog(w)
	if err != nil {
		return err
	}
	var bad []string
	for _, e := range cw.entries {
		if e.id == "" {
			continue
		}
		if !releaseRE.MatchString(e.release) || !keepAChangelogHeadings[e.heading] {
			bad = append(bad, fmt.Sprintf("%s at L%d (release %q, heading %q)", e.id, e.line, e.release, e.heading))
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("entries outside a Keep-a-Changelog heading: %s", strings.Join(bad, "; "))
	}
	return nil
}

func thenNoSecondIDInSummary(w *World, sr *StepRun) error {
	cw, err := currentChangelog(w)
	if err != nil {
		return err
	}
	var bad []string
	for _, e := range cw.entries {
		if e.id == "" {
			continue
		}
		for _, id := range anyTicketRE.FindAllString(e.summary, -1) {
			if id != e.id {
				bad = append(bad, fmt.Sprintf("%s at L%d also names %s", e.id, e.line, id))
			}
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("bold summaries naming a second ticket id: %s", strings.Join(bad, "; "))
	}
	return nil
}
