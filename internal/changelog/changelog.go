// Package changelog renders this repo's CHANGELOG.md from per-ticket fragments
// under changelog.d/ (drv-052), and validates those fragments for the `changelog`
// test rung.
//
// Layout of the fragment directory (see changelog.d/README.md, the user-facing
// contract):
//
//	changelog.d/
//	  _header.md              the preamble rendered above every release section
//	  README.md               documentation; never a fragment
//	  <name>.md               an UNRELEASED fragment (name = ticket id, e.g. drv-052)
//	  <x.y.z>/_release.md     a released version: frontmatter `date:`, optional intro prose
//	  <x.y.z>/<name>.md       a fragment released in that version
//
// A fragment is a small frontmatter block naming its Keep-a-Changelog section, then
// the entry body (house style: a bold one-line summary closing in the ticket id,
// then prose). A fragment with `skip: <reason>` and no body satisfies the rung but
// renders nothing.
//
// Rendering is a pure function of the fragment tree — deterministic and idempotent —
// so regenerating CHANGELOG.md on any commit can never conflict with itself.
// Ordering within a section is by ticket id: prefix (drv < drvctl < drvweb, i.e.
// lexical), then number ascending; non-ticket names sort after every ticket id,
// lexically. Releases render newest-first by semantic version.
//
// This package is repo tooling, not part of the draiver product: draiver itself
// knows nothing about changelogs (it only exports DRAIVER_TICKET to rung commands).
package changelog

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Dir is the fragment directory, relative to the repo root.
const Dir = "changelog.d"

// HeaderFile and ReleaseFile are the reserved, non-fragment files. Any name that
// starts with "_" is reserved; README.md is documentation.
const (
	HeaderFile  = "_header.md"
	ReleaseFile = "_release.md"
	ReadmeFile  = "README.md"
)

// Sections are the Keep-a-Changelog 1.1.0 change types, in render order.
var Sections = []string{"Added", "Changed", "Deprecated", "Removed", "Fixed", "Security"}

func knownSection(s string) bool {
	for _, k := range Sections {
		if k == s {
			return true
		}
	}
	return false
}

// TicketRE matches a ticket id as a whole fragment name.
var TicketRE = regexp.MustCompile(`^(drv(?:ctl|web)?)-([0-9]+)$`)

// entryNameRE matches a ticket fragment name with an optional ".N" continuation
// suffix — drvctl-016.2 is drvctl-016's second entry (a ticket whose change spans
// several bullets). The rung only ever requires the bare <ticket>.md.
var entryNameRE = regexp.MustCompile(`^(drv(?:ctl|web)?)-([0-9]+)(?:\.([0-9]+))?$`)

var versionRE = regexp.MustCompile(`^([0-9]+)\.([0-9]+)\.([0-9]+)$`)

// Fragment is one parsed changelog.d entry.
type Fragment struct {
	Name    string // file base name without ".md" (the ticket id for a ticket fragment)
	Path    string // path as read (for messages)
	Section string // Keep-a-Changelog section; "" only for a skip fragment
	Skip    string // non-empty: a skip fragment with this reason
	Body    string // entry markdown, trimmed; "" for a skip fragment
}

// Release is one released version: its fragments plus the release metadata.
type Release struct {
	Version   string
	Date      string // YYYY-MM-DD
	Intro     string // optional prose rendered under the version heading
	Fragments []Fragment
}

// Tree is the whole parsed fragment directory.
type Tree struct {
	Header     string
	Unreleased []Fragment
	Releases   []Release // newest first
}

type frontmatter struct {
	Section string  `yaml:"section"`
	Skip    *string `yaml:"skip"`
	Date    string  `yaml:"date"`
}

// splitFrontmatter separates a leading `---` block from the body. ok is false when
// the file does not open with a frontmatter fence or never closes it.
func splitFrontmatter(src []byte) (fm, body string, ok bool) {
	s := strings.ReplaceAll(string(src), "\r\n", "\n")
	if !strings.HasPrefix(s, "---\n") {
		return "", "", false
	}
	rest := s[len("---\n"):]
	if strings.HasPrefix(rest, "---\n") || rest == "---" {
		return "", strings.TrimPrefix(rest, "---"), true
	}
	i := strings.Index(rest, "\n---\n")
	if i < 0 {
		if strings.HasSuffix(rest, "\n---") {
			return rest[:len(rest)-len("\n---")], "", true
		}
		return "", "", false
	}
	return rest[:i+1], rest[i+len("\n---\n"):], true
}

func decodeFrontmatter(fm string, allowed ...string) (frontmatter, error) {
	var f frontmatter
	if strings.TrimSpace(fm) == "" {
		return f, nil
	}
	// Strict keys: a typo'd `sectoin:` must fail loudly, not silently drop the entry.
	var raw map[string]any
	if err := yaml.Unmarshal([]byte(fm), &raw); err != nil {
		return f, fmt.Errorf("frontmatter is not valid YAML: %v", err)
	}
	for k := range raw {
		ok := false
		for _, a := range allowed {
			ok = ok || k == a
		}
		if !ok {
			return f, fmt.Errorf("unknown frontmatter key %q (allowed: %s)", k, strings.Join(allowed, ", "))
		}
	}
	if err := yaml.Unmarshal([]byte(fm), &f); err != nil {
		return f, fmt.Errorf("frontmatter is not valid YAML: %v", err)
	}
	if _, present := raw["skip"]; present && f.Skip == nil {
		empty := "" // `skip:` with no value — present, but with an empty reason
		f.Skip = &empty
	}
	return f, nil
}

// ParseFragment parses and validates one fragment file's contents. name is the
// fragment's base name without ".md"; path is used only in error messages.
func ParseFragment(name, path string, src []byte) (Fragment, error) {
	fr := Fragment{Name: name, Path: path}
	fm, body, ok := splitFrontmatter(src)
	if !ok {
		return fr, fmt.Errorf("%s: missing frontmatter: a fragment must open with a `---` block naming its `section:` (or `skip:`)", path)
	}
	f, err := decodeFrontmatter(fm, "section", "skip")
	if err != nil {
		return fr, fmt.Errorf("%s: %v", path, err)
	}
	fr.Body = strings.TrimSpace(body)
	fr.Section = strings.TrimSpace(f.Section)
	if f.Skip != nil {
		fr.Skip = strings.TrimSpace(*f.Skip)
		if fr.Skip == "" {
			return fr, fmt.Errorf("%s: `skip:` needs a non-empty reason", path)
		}
		if fr.Body != "" {
			return fr, fmt.Errorf("%s: a `skip:` fragment must have no body (it renders nothing); drop the body or the skip", path)
		}
		if fr.Section != "" && !knownSection(fr.Section) {
			return fr, fmt.Errorf("%s: unknown section %q (want one of %s)", path, fr.Section, strings.Join(Sections, ", "))
		}
		return fr, nil
	}
	if fr.Section == "" {
		return fr, fmt.Errorf("%s: frontmatter names no `section:` (want one of %s, or `skip: <reason>`)", path, strings.Join(Sections, ", "))
	}
	if !knownSection(fr.Section) {
		return fr, fmt.Errorf("%s: unknown section %q (want one of %s)", path, fr.Section, strings.Join(Sections, ", "))
	}
	if fr.Body == "" {
		return fr, fmt.Errorf("%s: empty body — write the entry (bold one-line summary ending in the ticket id, then prose), or use `skip: <reason>`", path)
	}
	return fr, nil
}

// isFragmentFile reports whether a directory entry name is a fragment file.
func isFragmentFile(name string) bool {
	return strings.HasSuffix(name, ".md") && name != ReadmeFile && !strings.HasPrefix(name, "_")
}

// Load parses the fragment tree under root/changelog.d. It collects every
// validation error (not just the first), so one run names every broken fragment.
func Load(root string) (*Tree, error) {
	dir := filepath.Join(root, Dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	t := &Tree{}
	var errs []error
	if b, err := os.ReadFile(filepath.Join(dir, HeaderFile)); err != nil {
		errs = append(errs, fmt.Errorf("%s: %v", filepath.Join(Dir, HeaderFile), err))
	} else {
		t.Header = strings.TrimSpace(string(b))
	}
	for _, e := range entries {
		name := e.Name()
		rel := filepath.Join(Dir, name)
		switch {
		case e.IsDir():
			if !versionRE.MatchString(name) {
				errs = append(errs, fmt.Errorf("%s: unexpected directory (released versions are x.y.z)", rel))
				continue
			}
			r, rerrs := loadRelease(dir, name)
			errs = append(errs, rerrs...)
			t.Releases = append(t.Releases, r)
		case isFragmentFile(name):
			b, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				errs = append(errs, err)
				continue
			}
			fr, err := ParseFragment(strings.TrimSuffix(name, ".md"), rel, b)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			t.Unreleased = append(t.Unreleased, fr)
		}
	}
	sort.Slice(t.Releases, func(i, j int) bool { return versionLess(t.Releases[j].Version, t.Releases[i].Version) })
	return t, errors.Join(errs...)
}

func loadRelease(dir, version string) (Release, []error) {
	r := Release{Version: version}
	var errs []error
	vdir := filepath.Join(dir, version)
	relRel := filepath.Join(Dir, version, ReleaseFile)
	if b, err := os.ReadFile(filepath.Join(vdir, ReleaseFile)); err != nil {
		errs = append(errs, fmt.Errorf("%s: %v", relRel, err))
	} else if fm, body, ok := splitFrontmatter(b); !ok {
		errs = append(errs, fmt.Errorf("%s: missing frontmatter with `date:`", relRel))
	} else if f, err := decodeFrontmatter(fm, "date"); err != nil {
		errs = append(errs, fmt.Errorf("%s: %v", relRel, err))
	} else if !regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`).MatchString(f.Date) {
		errs = append(errs, fmt.Errorf("%s: `date:` must be YYYY-MM-DD, got %q", relRel, f.Date))
	} else {
		r.Date, r.Intro = f.Date, strings.TrimSpace(body)
	}
	entries, err := os.ReadDir(vdir)
	if err != nil {
		return r, append(errs, err)
	}
	for _, e := range entries {
		if e.IsDir() || !isFragmentFile(e.Name()) {
			continue
		}
		rel := filepath.Join(Dir, version, e.Name())
		b, err := os.ReadFile(filepath.Join(vdir, e.Name()))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		fr, err := ParseFragment(strings.TrimSuffix(e.Name(), ".md"), rel, b)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		r.Fragments = append(r.Fragments, fr)
	}
	return r, errs
}

func versionLess(a, b string) bool {
	ma, mb := versionRE.FindStringSubmatch(a), versionRE.FindStringSubmatch(b)
	for i := 1; i <= 3; i++ {
		x, _ := strconv.Atoi(ma[i])
		y, _ := strconv.Atoi(mb[i])
		if x != y {
			return x < y
		}
	}
	return false
}

// nameLess is the documented within-section order: ticket ids first, by prefix
// (lexical: drv < drvctl < drvweb) then number ascending, then continuation suffix
// (bare < .2 < .3); other names after, lexical (pre-ticket history is named
// NN-slug so lexical order is its curated order).
func nameLess(a, b string) bool {
	ma, mb := entryNameRE.FindStringSubmatch(a), entryNameRE.FindStringSubmatch(b)
	switch {
	case ma != nil && mb != nil:
		if ma[1] != mb[1] {
			return ma[1] < mb[1]
		}
		for _, i := range []int{2, 3} {
			x, _ := strconv.Atoi(ma[i])
			y, _ := strconv.Atoi(mb[i])
			if x != y {
				return x < y
			}
		}
		return a < b
	case ma != nil:
		return true
	case mb != nil:
		return false
	}
	return a < b
}

// renderEntry turns a fragment body into a top-level Markdown bullet: "- " before
// the first line and a two-space indent on every following non-blank line.
func renderEntry(buf *bytes.Buffer, body string) {
	for i, l := range strings.Split(body, "\n") {
		switch {
		case i == 0:
			buf.WriteString("- " + l + "\n")
		case strings.TrimSpace(l) == "":
			buf.WriteString("\n")
		default:
			buf.WriteString("  " + l + "\n")
		}
	}
}

func renderSections(buf *bytes.Buffer, frs []Fragment) {
	for _, sec := range Sections {
		var in []Fragment
		for _, f := range frs {
			if f.Skip == "" && f.Section == sec {
				in = append(in, f)
			}
		}
		if len(in) == 0 {
			continue
		}
		sort.Slice(in, func(i, j int) bool { return nameLess(in[i].Name, in[j].Name) })
		buf.WriteString("\n### " + sec + "\n\n")
		for _, f := range in {
			renderEntry(buf, f.Body)
		}
	}
}

// Render produces CHANGELOG.md from the tree: the header, `## [Unreleased]` from the
// unreleased fragments, then every released version newest-first. Skip fragments
// are omitted. The output is a pure function of the tree.
func Render(t *Tree) []byte {
	var buf bytes.Buffer
	buf.WriteString(t.Header + "\n")
	buf.WriteString("\n## [Unreleased]\n")
	renderSections(&buf, t.Unreleased)
	for _, r := range t.Releases {
		buf.WriteString("\n## [" + r.Version + "] - " + r.Date + "\n")
		if r.Intro != "" {
			buf.WriteString("\n" + r.Intro + "\n")
		}
		renderSections(&buf, r.Fragments)
	}
	return buf.Bytes()
}

// Find locates the fragment for name (a ticket id) anywhere in the tree — unreleased
// first, then any release. ok is false when there is none.
func (t *Tree) Find(name string) (Fragment, bool) {
	for _, f := range t.Unreleased {
		if f.Name == name {
			return f, true
		}
	}
	for _, r := range t.Releases {
		for _, f := range r.Fragments {
			if f.Name == name {
				return f, true
			}
		}
	}
	return Fragment{}, false
}

// FragmentPath is the path (relative to the repo root) a ticket's fragment is
// expected at.
func FragmentPath(ticket string) string { return filepath.Join(Dir, ticket+".md") }

// branchRE is draiver's attempt-branch shape, draiver/<ticket>/<attempt>.
var branchRE = regexp.MustCompile(`^draiver/([^/]+)/([^/]+)$`)

// TicketFromBranch extracts the ticket id from a draiver/<ticket>/<attempt> branch.
func TicketFromBranch(branch string) (string, bool) {
	m := branchRE.FindStringSubmatch(strings.TrimSpace(branch))
	if m == nil {
		return "", false
	}
	return m[1], true
}
