package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/worktree"
)

var (
	newTitle    string
	newProject  string
	newTeam     string
	newAssignee string
	newSpecFile string
	newTool     string
	newModel    string
	newRepo     string
	newBase     string
)

var newCmd = &cobra.Command{
	Use:   "new TICKET",
	Short: "Create a ticket (spec.md) and its first attempt",
	Long: `Create a ticket (spec.md) and its first attempt.

A ticket must have a title, from exactly one source:

  draiver new X --title "T"              scaffold a spec titled T
  draiver new X --spec f.md --title "T"  import f.md, titling it T
                                         (f.md's frontmatter must not already have a title)
  draiver new X --spec f.md              import f.md; its frontmatter must carry a title:

Creation fails if no source supplies a non-empty title, or if both --title and
the imported frontmatter do (ambiguous). A rejected new writes nothing.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		root, err := resolveRoot()
		if err != nil {
			return err
		}
		if root.Exists(id) {
			return fmt.Errorf("ticket %q already exists", id)
		}

		// Build (and validate the title of) the spec, and validate --repo, before
		// any side effect: a rejected `new` must leave no orphan ticket dir behind.
		spec, err := buildSpec(id)
		if err != nil {
			return err
		}
		if strings.TrimSpace(newRepo) == "" {
			return fmt.Errorf("%s", errNoRepo)
		}
		// Resolve the base branch before any side effect (like the title/repo checks),
		// so a detached-HEAD repo with no --base is rejected leaving no orphan ticket.
		base, err := resolveBase(cmd.Context(), newRepo, newBase)
		if err != nil {
			return err
		}
		if err := root.EnsureTicketDir(id); err != nil {
			return err
		}
		if err := os.WriteFile(root.SpecPath(id), spec, 0o644); err != nil {
			return fmt.Errorf("write spec: %w", err)
		}

		m, err := attempt.Create(root, id, attempt.New{
			Tool:  newTool,
			Model: newModel,
			Repo:  newRepo,
			Base:  base,
			Actor: resolveActor(),
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "created %s attempt %s at %s\n", id, m.ID, root.TicketDir(id))
		return nil
	},
}

// errNoTitle is returned when no creation path supplied a meaningful title.
// Keeping it one message keeps the two inflow paths (scaffold, import) in sync.
const errNoTitle = "a ticket title is required: pass --title, or --spec a file whose frontmatter carries a non-empty title:"

// errNoRepo is returned when creation supplies no repo path. Every attempt must
// record the local git working tree it targets (drvctl-017), so the daemon knows
// where to cut its worktree; a repo-less attempt would only fail later, at the
// daemon, far from the person who could fix it here.
const errNoRepo = "a repo path is required: pass --repo <local git working tree> so the supervisor knows where to cut this attempt's worktree"

// resolveBase resolves the base branch a new attempt lands back into: an explicit
// --base wins; otherwise it defaults to the branch the bound repo currently has
// checked out (drvctl-021, the sibling of drvctl-017's repo capture). A
// detached-HEAD repo has no branch to default from, so with no --base it errors
// here — at creation, near the person who can fix it — rather than deferring the
// failure to the first `ctl merge`. flagBase is trimmed; an explicit --base is
// trusted as given (it need not exist yet — sync can create history under it).
func resolveBase(ctx context.Context, repo, flagBase string) (string, error) {
	if b := strings.TrimSpace(flagBase); b != "" {
		return b, nil
	}
	head, ok, err := worktree.HeadBranch(ctx, repo)
	switch {
	case err != nil:
		// The repo path is not a resolvable git working tree (git missing, or the
		// path is not a repo). drvctl-017 keeps repo *validity* a daemon-time concern,
		// not a creation-time one, so base defaulting follows suit: record no base and
		// let `ctl merge`/`sync` surface it later, rather than hard-fail creation on a
		// path the daemon would reject anyway.
		return "", nil
	case !ok:
		// A real repo, but on a detached HEAD — there is a repo to consult yet no
		// branch to default from, so require an explicit --base here, near the person
		// who can fix it, per the spec.
		return "", fmt.Errorf("%s", errNoBase)
	default:
		return head, nil
	}
}

// errNoBase is returned when the base cannot be defaulted (the repo is on a
// detached HEAD) and no --base was given. The base is the merge target / sync
// source, so an attempt must record one to be landable (drvctl-021).
const errNoBase = "a base branch is required: the repo is on a detached HEAD so it cannot be defaulted; pass --base <branch> (the branch this attempt lands back into)"

// buildSpec produces the spec.md bytes for a new ticket and validates that the
// title has exactly one source. It performs no side effects, so `new` can call
// it before EnsureTicketDir and reject a bad title without leaving an orphan.
//
// Title sources, treated as XOR (a source is "set" only when non-empty after
// trimming): spec frontmatter title, and --title. Neither → error (no more
// silent id fallback); both → error (ambiguous); exactly one → use it.
func buildSpec(id string) ([]byte, error) {
	flagTitle := strings.TrimSpace(newTitle)

	if newSpecFile != "" {
		data, err := os.ReadFile(newSpecFile)
		if err != nil {
			return nil, fmt.Errorf("read spec: %w", err)
		}
		specTitle := strings.TrimSpace(frontmatterTitle(data))
		switch {
		case specTitle == "" && flagTitle == "":
			return nil, fmt.Errorf("%s", errNoTitle)
		case specTitle != "" && flagTitle != "":
			return nil, fmt.Errorf("title set by both --title (%q) and the spec's frontmatter (%q); pass only one", flagTitle, specTitle)
		case flagTitle != "":
			return injectTitle(data, flagTitle), nil
		default:
			return data, nil
		}
	}

	// Scaffold path: the only possible title source is --title.
	if flagTitle == "" {
		return nil, fmt.Errorf("%s", errNoTitle)
	}
	return scaffoldSpec(id, flagTitle), nil
}

// yamlLine renders a single `key: value` frontmatter line with the value encoded
// YAML-safely. It marshals via yaml.v3 rather than hand-formatting, so a value
// carrying a colon, quote, leading `#`, or any other metacharacter can't produce
// invalid frontmatter (which every later status/brief/webui would choke on).
// Plain values marshal unquoted, so ordinary values stay verbatim.
func yamlLine(key, val string) string {
	out, err := yaml.Marshal(map[string]string{key: val})
	if err != nil {
		// Marshalling a string-valued map does not fail; guard defensively so a
		// future change here can never silently drop the value.
		return key + ": " + val
	}
	return strings.TrimRight(string(out), "\n")
}

// optionalSpecLine renders a spec.md metadata line (newline included): a set
// value as a real YAML line, an unset one as a commented example naming the key,
// a sample, and that it is optional board-grouping metadata. loadSpecMeta parses
// YAML, so the commented line is ignored on read — the field is simply unset.
func optionalSpecLine(key, val, example string) string {
	if strings.TrimSpace(val) != "" {
		return yamlLine(key, val) + "\n"
	}
	return fmt.Sprintf("# %s: %s  # optional board-grouping metadata\n", key, example)
}

// scaffoldSpec renders a fresh spec.md with identity frontmatter and a stub body.
func scaffoldSpec(id, title string) []byte {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "id: %s\n", id)
	// title/project/team/assignee are free-text (title and the latter three are
	// user-supplied flags), so each goes through yamlLine to stay valid YAML.
	b.WriteString(yamlLine("title", title) + "\n")
	// project/team/assignee are optional board-grouping metadata. When set they
	// render as real lines; when unset they render as commented examples so a
	// hand-editor sees the key and a sample rather than a mute blank line.
	b.WriteString(optionalSpecLine("project", newProject, "acme-web"))
	b.WriteString(optionalSpecLine("team", newTeam, "platform"))
	b.WriteString(optionalSpecLine("assignee", newAssignee, "alice"))
	fmt.Fprintf(&b, "created: %s\n", time.Now().UTC().Format(time.RFC3339))
	b.WriteString("---\n\n")
	fmt.Fprintf(&b, "# %s\n\n", title)
	b.WriteString("<!-- Frontloaded design goes here. This file is the immutable input,\n")
	b.WriteString("     shared by every attempt; everything mutable lives in the log. -->\n")
	return []byte(b.String())
}

// frontmatterTitle returns the trimmed title from an imported file's leading
// YAML frontmatter block, or "" if the file has no frontmatter, no title key, a
// blank title, or unparseable frontmatter. It mirrors project.loadSpecMeta's
// tolerant framing: a doc that opens straight into prose simply has no title.
func frontmatterTitle(data []byte) string {
	s := string(data)
	if !strings.HasPrefix(s, "---\n") {
		return ""
	}
	rest := s[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return ""
	}
	var m struct {
		Title string `yaml:"title"`
	}
	if err := yaml.Unmarshal([]byte(rest[:end]), &m); err != nil {
		return ""
	}
	return m.Title
}

// injectTitle returns data with a `title:` frontmatter key set to title. It is
// called only when the file carries no meaningful title of its own, so it may
// safely overwrite any existing (blank) title line rather than risk a duplicate
// key. A file with no frontmatter gets a fresh block prepended.
func injectTitle(data []byte, title string) []byte {
	s := string(data)
	titleLine := yamlLine("title", title)
	if !strings.HasPrefix(s, "---\n") {
		return []byte("---\n" + titleLine + "\n---\n\n" + s)
	}
	rest := s[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		// Opening fence with no close: treat as bodyless and prepend a block.
		return []byte("---\n" + titleLine + "\n---\n\n" + s)
	}
	lines := strings.Split(rest[:end], "\n")
	replaced := false
	for i, ln := range lines {
		if strings.HasPrefix(strings.TrimSpace(ln), "title:") {
			lines[i] = titleLine
			replaced = true
			break
		}
	}
	if !replaced {
		lines = append([]string{titleLine}, lines...)
	}
	return []byte("---\n" + strings.Join(lines, "\n") + rest[end:])
}

func init() {
	newCmd.Flags().StringVar(&newTitle, "title", "", "human-readable ticket title (required unless --spec's frontmatter carries a title:)")
	newCmd.Flags().StringVar(&newProject, "project", "", "project field")
	newCmd.Flags().StringVar(&newTeam, "team", "", "team field")
	newCmd.Flags().StringVar(&newAssignee, "assignee", "", "assignee field")
	newCmd.Flags().StringVar(&newSpecFile, "spec", "", "import spec.md from this file instead of scaffolding one; --title supplies the title if the file's frontmatter lacks one (supplying both errors)")
	newCmd.Flags().StringVar(&newTool, "tool", "", "coding-agent tool for the first attempt (e.g. claude-code)")
	newCmd.Flags().StringVar(&newModel, "model", "", "model for the first attempt (e.g. opus-4.8)")
	newCmd.Flags().StringVar(&newRepo, "repo", "", "required: local path to the git working tree this ticket's attempts target (the supervisor cuts each session's worktree from it)")
	newCmd.Flags().StringVar(&newBase, "base", "", "branch this attempt lands back into via `ctl merge`/`ctl sync` (default: the repo's current branch)")
	rootCmd.AddCommand(newCmd)
}
