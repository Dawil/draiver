package acceptance

import (
	"fmt"
	"strings"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// ---- markdown-tables.feature: GFM pipe tables render on the detail page (drvweb-024) ----

func givenBoardWithPipeTable(w *World, sr *StepRun) error {
	b, err := seedBoard(w, greenPyramid)
	if err != nil {
		return err
	}
	// A note whose body is a GFM pipe table with a javascript: link in a cell: it
	// exercises both the table extension (rendering) and linkPolicy (scheme floor)
	// on the one shared goldmark instance the detail page uses.
	body := "| Col A | Col B |\n| --- | --- |\n| one | [x](javascript:alert(1)) |\n"
	if _, err := ticketlog.Append(b.root, accTicket, accAtt, event.Event{Type: "note", Actor: "agent:acceptance", Body: body}); err != nil {
		return err
	}
	w.set("board", b)
	return nil
}

func thenPageRendersPipeTable(w *World, sr *StepRun) error {
	b, err := currentBoard(w)
	if err != nil {
		return err
	}
	html, err := getBody(b.url("/ticket/" + accTicket + "/" + accAtt))
	if err != nil {
		return err
	}
	for _, want := range []string{"<table>", "<th>Col A</th>", "<td>one</td>"} {
		if !strings.Contains(html, want) {
			return fmt.Errorf("attempt page does not render the pipe table as HTML (missing %q)", want)
		}
	}
	if strings.Contains(html, "| Col A | Col B |") || strings.Contains(html, "| --- | --- |") {
		return fmt.Errorf("pipe-table source leaked literally; table extension not applied")
	}
	return shoot(w, sr, "/ticket/"+accTicket+"/"+accAtt, "pipe-table-rendered", true, "table", "")
}

func thenPageKeepsSchemeFloorInTable(w *World, sr *StepRun) error {
	b, err := currentBoard(w)
	if err != nil {
		return err
	}
	html, err := getBody(b.url("/ticket/" + accTicket + "/" + accAtt))
	if err != nil {
		return err
	}
	if strings.Contains(html, "javascript:alert(1)") {
		return fmt.Errorf("javascript: link inside a table cell leaked; scheme floor not preserved")
	}
	return nil
}
