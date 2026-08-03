// Package ticketlog reads and appends an attempt's write-once, hash-chained
// event log. Appends are strictly additive: seq is monotonic within the attempt,
// prev links to the last event's hash, and files are created with O_EXCL so
// nothing is ever clobbered.
package ticketlog

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/store"
)

// Read returns an attempt's events in causal (seq) order.
func Read(root store.Root, id, attempt string) ([]event.Event, error) {
	dir := root.LogDir(id, attempt)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read log %s/%s: %w", id, attempt, err)
	}
	var events []event.Event
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read event %s: %w", e.Name(), err)
		}
		ev, err := event.Parse(data)
		if err != nil {
			return nil, fmt.Errorf("parse event %s: %w", e.Name(), err)
		}
		events = append(events, ev)
	}
	sort.Slice(events, func(i, j int) bool { return events[i].Seq < events[j].Seq })
	return events, nil
}

// Last returns the highest-seq event in an attempt, or ok=false if empty.
func Last(root store.Root, id, attempt string) (event.Event, bool, error) {
	events, err := Read(root, id, attempt)
	if err != nil {
		return event.Event{}, false, err
	}
	if len(events) == 0 {
		return event.Event{}, false, nil
	}
	return events[len(events)-1], true, nil
}

// Append writes a new event to the attempt's log. The caller supplies Type,
// Actor, Refs, Artefacts, and Body; Append allocates Seq, links Prev to the
// current tail, stamps Ticket/Attempt/TS, computes Hash, and creates the file
// with O_EXCL. On a seq collision from a concurrent writer it re-reads and
// retries. It returns the persisted event.
func Append(root store.Root, id, attempt string, e event.Event) (event.Event, error) {
	if !root.AttemptExists(id, attempt) {
		return event.Event{}, fmt.Errorf("append: attempt %s/%s does not exist", id, attempt)
	}
	if e.Type == "" {
		return event.Event{}, fmt.Errorf("append: event type is required")
	}
	e.Ticket = id
	e.Attempt = attempt
	if e.TS.IsZero() {
		e.TS = time.Now()
	}
	e.TS = e.TS.UTC().Truncate(time.Second)

	const maxRetries = 8
	for attemptN := 0; attemptN < maxRetries; attemptN++ {
		last, ok, err := Last(root, id, attempt)
		if err != nil {
			return event.Event{}, err
		}
		if ok {
			e.Seq = last.Seq + 1
			e.Prev = last.Hash
		} else {
			e.Seq = 1
			e.Prev = ""
		}
		e.Hash = e.ComputeHash()

		data, err := e.Marshal()
		if err != nil {
			return event.Event{}, err
		}
		path := filepath.Join(root.LogDir(id, attempt), e.Filename())
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			if errors.Is(err, fs.ErrExist) {
				// Another writer took this filename; re-read and retry.
				continue
			}
			return event.Event{}, fmt.Errorf("create event: %w", err)
		}
		if _, err := f.Write(data); err != nil {
			f.Close()
			return event.Event{}, fmt.Errorf("write event: %w", err)
		}
		if err := f.Close(); err != nil {
			return event.Event{}, fmt.Errorf("close event: %w", err)
		}
		return e, nil
	}
	return event.Event{}, fmt.Errorf("append: gave up after %d seq collisions", maxRetries)
}
