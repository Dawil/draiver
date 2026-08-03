package attempt

import (
	"testing"

	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

func TestCreateAllocatesSequentialIDsWithGenesis(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	id := "PROJ-1"
	if err := root.EnsureTicketDir(id); err != nil {
		t.Fatal(err)
	}

	a1, err := Create(root, id, New{Tool: "claude-code", Model: "opus-4.8", Actor: "agent:x"})
	if err != nil {
		t.Fatalf("create 1: %v", err)
	}
	a2, err := Create(root, id, New{Tool: "aider", Actor: "agent:y"})
	if err != nil {
		t.Fatalf("create 2: %v", err)
	}
	if a1.ID != "0001" || a2.ID != "0002" {
		t.Fatalf("ids = %q,%q want 0001,0002", a1.ID, a2.ID)
	}

	// Each attempt gets its own genesis "created" event at seq 1.
	for _, aid := range []string{"0001", "0002"} {
		events, err := ticketlog.Read(root, id, aid)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 1 || events[0].Type != "created" || events[0].Seq != 1 {
			t.Errorf("attempt %s genesis wrong: %+v", aid, events)
		}
	}

	// Meta round-trips.
	m, err := LoadMeta(root, id, "0001")
	if err != nil {
		t.Fatal(err)
	}
	if m.Tool != "claude-code" || m.Model != "opus-4.8" || m.Actor != "agent:x" {
		t.Errorf("meta not persisted: %+v", m)
	}

	latest, ok, err := Latest(root, id)
	if err != nil || !ok || latest != "0002" {
		t.Errorf("latest = %q,%v,%v want 0002,true,nil", latest, ok, err)
	}
}

func TestCreateRejectsUnknownTicket(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	if _, err := Create(root, "NOPE-1", New{Actor: "a"}); err == nil {
		t.Error("expected error creating attempt on nonexistent ticket")
	}
}
