package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/Dawil/draiver/internal/attempt"
)

var (
	newTitle    string
	newProject  string
	newTeam     string
	newAssignee string
	newSpecFile string
	newTool     string
	newModel    string
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

		// Build (and validate the title of) the spec before any side effect:
		// a rejected `new` must leave no orphan ticket dir behind.
		spec, err := buildSpec(id)
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

// scaffoldSpec renders a fresh spec.md with identity frontmatter and a stub body.
func scaffoldSpec(id, title string) []byte {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "id: %s\n", id)
	fmt.Fprintf(&b, "title: %s\n", title)
	fmt.Fprintf(&b, "project: %s\n", newProject)
	fmt.Fprintf(&b, "team: %s\n", newTeam)
	fmt.Fprintf(&b, "assignee: %s\n", newAssignee)
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
	titleLine := "title: " + title
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
	rootCmd.AddCommand(newCmd)
}
