package cmd

import (
	"errors"
	"fmt"
	"testing"
)

func TestReservePolecatAdmissionHoldsSlotUntilRelease(t *testing.T) {
	townRoot := t.TempDir()
	prevCount := countPolecatSlotsForAdmission
	t.Cleanup(func() { countPolecatSlotsForAdmission = prevCount })
	countPolecatSlotsForAdmission = func(string) (int, error) { return 0, nil }

	reservation, err := reservePolecatAdmission(townRoot, 1)
	if err != nil {
		t.Fatalf("first reservePolecatAdmission: %v", err)
	}

	second, err := reservePolecatAdmission(townRoot, 1)
	if err == nil {
		second.Release()
		t.Fatal("second reservePolecatAdmission succeeded while first reservation was held")
	}
	if !errors.Is(err, ErrPolecatAdmissionDenied) {
		t.Fatalf("second reservePolecatAdmission error = %v, want ErrPolecatAdmissionDenied", err)
	}

	reservation.Release()
	reservation.Release() // idempotent; rollback and success paths may both attempt release.

	third, err := reservePolecatAdmission(townRoot, 1)
	if err != nil {
		t.Fatalf("third reservePolecatAdmission after release: %v", err)
	}
	third.Release()
}

func TestReservePolecatAdmissionCountsDurableAssignments(t *testing.T) {
	townRoot := t.TempDir()
	prevCount := countPolecatSlotsForAdmission
	t.Cleanup(func() { countPolecatSlotsForAdmission = prevCount })
	countPolecatSlotsForAdmission = func(string) (int, error) { return 1, nil }

	reservation, err := reservePolecatAdmission(townRoot, 1)
	if err == nil {
		reservation.Release()
		t.Fatal("reservePolecatAdmission succeeded despite full durable capacity")
	}
	if !errors.Is(err, ErrPolecatAdmissionDenied) {
		t.Fatalf("reservePolecatAdmission error = %v, want ErrPolecatAdmissionDenied", err)
	}
}

func TestCountAdmissionOccupiedSlots(t *testing.T) {
	tests := []struct {
		name  string
		slots []polecatAdmissionSlot
		want  int
	}{
		{
			name: "missing agent occupies capacity",
			slots: []polecatAdmissionSlot{
				{},
			},
			want: 1,
		},
		{
			name: "recovery blocked idle slots occupy capacity",
			slots: []polecatAdmissionSlot{
				{HasAgent: true, AgentState: "idle", CleanupStatus: "has_uncommitted"},
				{HasAgent: true, AgentState: "idle", CleanupStatus: "has_stash"},
				{HasAgent: true, AgentState: "idle", CleanupStatus: "has_unpushed"},
				{HasAgent: true, AgentState: "idle", CleanupStatus: "unknown"},
				{HasAgent: true, AgentState: "idle"},
				{HasAgent: true, AgentState: "idle", CleanupStatus: "clean", PushFailed: true},
				{HasAgent: true, AgentState: "idle", CleanupStatus: "clean", MRFailed: true},
			},
			want: 7,
		},
		{
			name: "submitted preserved clean slot releases capacity",
			slots: []polecatAdmissionSlot{
				{HasAgent: true, AgentState: "idle", CleanupStatus: "clean", ActiveMR: "gt-mr-abc"},
			},
			want: 0,
		},
		{
			name: "idle clean slot releases capacity",
			slots: []polecatAdmissionSlot{
				{HasAgent: true, AgentState: "idle", CleanupStatus: "clean"},
			},
			want: 0,
		},
		{
			name: "nuked clean slot releases capacity",
			slots: []polecatAdmissionSlot{
				{HasAgent: true, AgentState: "nuked", CleanupStatus: "clean"},
			},
			want: 0,
		},
		{
			name: "orphan session occupies capacity",
			slots: []polecatAdmissionSlot{
				{HasAgent: true, OrphanSession: true},
			},
			want: 1,
		},
		{
			name: "active assignment occupies capacity despite stale hook",
			slots: []polecatAdmissionSlot{
				{HasAgent: true, ActiveAssignment: true, AgentState: "idle", CleanupStatus: "clean"},
				{HasAgent: true, AgentState: "idle", CleanupStatus: "clean", HookBead: "gt-work"},
			},
			want: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := countAdmissionOccupiedSlots(tt.slots); got != tt.want {
				t.Fatalf("countAdmissionOccupiedSlots() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestAdmissionDenialRecognizesWrappedErrors(t *testing.T) {
	err := fmt.Errorf("sling failed: %w", fmt.Errorf("spawning polecat: %w", ErrPolecatAdmissionDenied))
	if !isAdmissionDenial(err) {
		t.Fatalf("isAdmissionDenial(%v) = false, want true", err)
	}
}
