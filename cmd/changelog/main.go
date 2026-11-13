// Command changelog is this repo's changelog tooling (drv-052): CHANGELOG.md is an
// output rendered from per-ticket fragments under changelog.d/, never edited by hand.
//
//	go run ./cmd/changelog check [--ticket ID]   the `changelog` test rung
//	go run ./cmd/changelog generate [--check]    render CHANGELOG.md (--check: fail if stale)
//	go run ./cmd/changelog release X.Y.Z [--date YYYY-MM-DD]
//	                                             move unreleased fragments into changelog.d/X.Y.Z/
//
// Every subcommand takes --root (default "."), the repo root holding changelog.d/.
// It lives in-repo (not in the draiver binary) because changelog policy is this
// repo's, not draiver's: draiver only exports DRAIVER_TICKET to rung commands.
// See changelog.d/README.md for the fragment format and the regeneration policy.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Dawil/draiver/internal/changelog"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

const usage = "usage: changelog check [--ticket ID] | generate [--check] | release X.Y.Z [--date YYYY-MM-DD]  (all take --root DIR)"

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("changelog "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", ".", "repo root holding changelog.d/")
	var err error
	switch sub {
	case "check":
		ticket := fs.String("ticket", "", "ticket id (default $DRAIVER_TICKET, else the draiver/<ticket>/<attempt> branch)")
		if fs.Parse(rest) != nil {
			return 2
		}
		err = check(*root, *ticket, stdout)
	case "generate":
		stale := fs.Bool("check", false, "do not write; exit 1 if CHANGELOG.md differs from the rendered fragments")
		if fs.Parse(rest) != nil {
			return 2
		}
		err = generate(*root, *stale, stdout)
	case "release":
		date := fs.String("date", time.Now().UTC().Format("2006-01-02"), "release date")
		if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
			fmt.Fprintln(stderr, usage)
			return 2
		}
		version := rest[0]
		if fs.Parse(rest[1:]) != nil {
			return 2
		}
		err = release(*root, version, *date, stdout)
	default:
		fmt.Fprintln(stderr, usage)
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "changelog %s: %v\n", sub, err)
		return 1
	}
	return 0
}

// resolveTicket picks the ticket id: an explicit flag, then $DRAIVER_TICKET (which
// `draiver test` exports to rung commands), then the draiver/<ticket>/<attempt>
// branch checked out at root — the fallback for a human running the check by hand.
func resolveTicket(root, flagVal string) (ticket, source string) {
	if v := strings.TrimSpace(flagVal); v != "" {
		return v, "--ticket"
	}
	if v := strings.TrimSpace(os.Getenv("DRAIVER_TICKET")); v != "" {
		return v, "$DRAIVER_TICKET"
	}
	out, err := exec.Command("git", "-C", root, "branch", "--show-current").Output()
	if err == nil {
		if t, ok := changelog.TicketFromBranch(string(out)); ok {
			return t, "branch " + strings.TrimSpace(string(out))
		}
	}
	return "", ""
}

func check(root, flagTicket string, stdout io.Writer) error {
	ticket, source := resolveTicket(root, flagTicket)
	if ticket == "" {
		return fmt.Errorf("cannot determine the ticket id: $DRAIVER_TICKET is unset and the current branch is not draiver/<ticket>/<attempt> (pass --ticket ID to check by hand)")
	}
	fr, err := changelog.Check(root, ticket)
	if err != nil {
		return err
	}
	if fr.Skip != "" {
		fmt.Fprintf(stdout, "changelog: %s ok (ticket %s from %s) — skip: %s\n", fr.Path, ticket, source, fr.Skip)
	} else {
		fmt.Fprintf(stdout, "changelog: %s ok (ticket %s from %s) — %s\n", fr.Path, ticket, source, fr.Section)
	}
	return nil
}

func generate(root string, stale bool, stdout io.Writer) error {
	t, err := changelog.Load(root)
	if err != nil {
		return err
	}
	out := changelog.Render(t)
	path := filepath.Join(root, "CHANGELOG.md")
	if stale {
		cur, _ := os.ReadFile(path)
		if !bytes.Equal(cur, out) {
			return fmt.Errorf("CHANGELOG.md is stale: run `go run ./cmd/changelog generate`")
		}
		fmt.Fprintln(stdout, "changelog: CHANGELOG.md is current")
		return nil
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "changelog: wrote %s (%d unreleased fragments, %d releases)\n", path, len(t.Unreleased), len(t.Releases))
	return nil
}

var versionRE = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

// release cuts a version: every unreleased fragment (skip fragments included, so
// the ticket stays accounted for) moves into changelog.d/<version>/ beside a new
// _release.md, then CHANGELOG.md is regenerated.
func release(root, version, date string, stdout io.Writer) error {
	if !versionRE.MatchString(version) {
		return fmt.Errorf("version %q is not x.y.z", version)
	}
	if !regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`).MatchString(date) {
		return fmt.Errorf("--date %q is not YYYY-MM-DD", date)
	}
	t, err := changelog.Load(root)
	if err != nil {
		return err
	}
	if len(t.Unreleased) == 0 {
		return fmt.Errorf("nothing to release: changelog.d/ holds no unreleased fragments")
	}
	vdir := filepath.Join(root, changelog.Dir, version)
	if _, err := os.Stat(vdir); err == nil {
		return fmt.Errorf("%s already exists", vdir)
	}
	if err := os.MkdirAll(vdir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(vdir, changelog.ReleaseFile), []byte("---\ndate: "+date+"\n---\n"), 0o644); err != nil {
		return err
	}
	for _, f := range t.Unreleased {
		if err := os.Rename(filepath.Join(root, f.Path), filepath.Join(vdir, f.Name+".md")); err != nil {
			return err
		}
	}
	fmt.Fprintf(stdout, "changelog: released %d fragments as %s (%s)\n", len(t.Unreleased), version, date)
	return generate(root, false, stdout)
}
