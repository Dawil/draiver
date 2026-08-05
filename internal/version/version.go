// Package version resolves draiver's version from the project changelog, so the
// reported version *is* the changelog's latest released section and the two
// cannot drift. The CLI embeds CHANGELOG.md at the repo root and hands the text
// to FromChangelog; there is no hand-maintained version constant to forget.
package version

import (
	"bufio"
	"errors"
	"regexp"
	"strings"
)

// headerRE matches a Keep-a-Changelog section header — `## [<label>]` — and
// captures the label. The label is a version for a released section
// (`## [0.2.1] - 2026-08-05`) or the literal `Unreleased`, which FromChangelog
// skips.
var headerRE = regexp.MustCompile(`^##\s+\[([^\]]+)\]`)

// FromChangelog returns the latest released version in a Keep-a-Changelog
// document: the topmost `## [x.y.z]` header that is not `## [Unreleased]`. A dev
// build between releases still reports the last released version — that is what
// "same as the changelog" means; it does not invent an Unreleased version. It
// errors if the document has no released section.
func FromChangelog(changelog string) (string, error) {
	sc := bufio.NewScanner(strings.NewReader(changelog))
	for sc.Scan() {
		m := headerRE.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		if strings.EqualFold(m[1], "Unreleased") {
			continue
		}
		return m[1], nil
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", errors.New("changelog has no released version section")
}
