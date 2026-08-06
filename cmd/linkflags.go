package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/event"
)

// defaultLinkRel is the Rel assigned to a bare --url link. Review links are
// overwhelmingly pull/merge requests, so --url is the ergonomic common case and
// --link rel=uri the escape hatch for other rels (diff, ci, …).
const defaultLinkRel = "pr"

// addLinkFlags registers the shared --url / --link flags on a command that can
// carry review links (review, log). Both are repeatable; the bound slices are
// package-level so the test harness can reset them between runs.
func addLinkFlags(cmd *cobra.Command, urls, links *[]string) {
	cmd.Flags().StringArrayVar(urls, "url", nil,
		fmt.Sprintf("a review link (repeatable); rel defaults to %q", defaultLinkRel))
	cmd.Flags().StringArrayVar(links, "link", nil,
		"an explicit review link as rel=uri (repeatable), for rels other than the --url default")
}

// buildLinks turns the raw --url and --link flag values into event.Links. A
// --url becomes {Rel: "pr", Href: v}; a --link must be rel=uri with a non-empty
// rel. It does NOT validate the Href scheme/host — that is the append-time
// safety floor (event.ValidateLink), enforced once for every write path in
// appendEvent. Order is url-derived links first, then link-derived, preserving
// each flag's given order.
func buildLinks(urls, links []string) ([]event.Link, error) {
	var out []event.Link
	for _, u := range urls {
		out = append(out, event.Link{Rel: defaultLinkRel, Href: u})
	}
	for _, l := range links {
		rel, href, ok := strings.Cut(l, "=")
		if !ok {
			return nil, fmt.Errorf("--link %q is not in rel=uri form", l)
		}
		rel = strings.TrimSpace(rel)
		if rel == "" {
			return nil, fmt.Errorf("--link %q has an empty rel", l)
		}
		out = append(out, event.Link{Rel: rel, Href: href})
	}
	return out, nil
}
