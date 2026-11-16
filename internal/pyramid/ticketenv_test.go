package pyramid

import (
	"reflect"
	"testing"
)

func TestTicketEnv(t *testing.T) {
	base := []string{"DRAIVER_CODEBASE_DIR=/x"}
	got := TicketEnv(base, "drv-052")
	want := []string{"DRAIVER_CODEBASE_DIR=/x", "DRAIVER_TICKET=drv-052"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TicketEnv = %v, want %v", got, want)
	}
	if len(base) != 1 {
		t.Fatalf("TicketEnv mutated its input: %v", base)
	}
	if got := TicketEnv(base, "  "); !reflect.DeepEqual(got, base) {
		t.Fatalf("blank ticket added env: %v", got)
	}
	if got := TicketEnv(nil, ""); got != nil {
		t.Fatalf("blank ticket on nil env = %v, want nil", got)
	}
}
