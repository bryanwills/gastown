package cmd

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
)

type polecatAdmissionSlot struct {
	HasAgent         bool
	OrphanSession    bool
	ActiveAssignment bool
	AgentState       string
	HookBead         string
	CleanupStatus    string
	ActiveMR         string
	PushFailed       bool
	MRFailed         bool
}

func countAdmissionOccupiedSlots(slots []polecatAdmissionSlot) int {
	occupied := 0
	for _, slot := range slots {
		if polecatAdmissionSlotOccupiesCapacity(slot) {
			occupied++
		}
	}
	return occupied
}

func polecatAdmissionSlotOccupiesCapacity(slot polecatAdmissionSlot) bool {
	if slot.OrphanSession || !slot.HasAgent || slot.ActiveAssignment {
		return true
	}

	state := strings.ToLower(strings.TrimSpace(slot.AgentState))
	switch state {
	case "idle", "nuked":
		// Idle/nuked slots are free only when cleanup metadata proves reuse is safe.
	default:
		return true
	}

	if slot.HookBead != "" || slot.PushFailed || slot.MRFailed {
		return true
	}
	return !polecat.CleanupStatus(slot.CleanupStatus).IsSafe()
}

func countAdmissionOccupyingPolecats(townRoot string) (int, error) {
	rigDirs, err := admissionCapacityRigDirs(townRoot)
	if err != nil {
		return 0, err
	}

	agents, err := beads.New(townRoot).ForAgentBead().ListAgentBeads()
	if err != nil {
		return 0, err
	}
	activeAssignments, err := admissionActivePolecatAssignments(rigDirs)
	if err != nil {
		return 0, err
	}

	slots := make([]polecatAdmissionSlot, 0, len(agents)+len(activeAssignments))
	accounted := make(map[string]bool)
	for _, rigDir := range rigDirs {
		rigName := filepath.Base(rigDir)
		entries, err := os.ReadDir(filepath.Join(rigDir, "polecats"))
		if err != nil {
			return 0, err
		}
		prefix := beads.GetPrefixForRig(townRoot, rigName)
		for _, entry := range entries {
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			name := entry.Name()
			key := rigName + "/" + name
			accounted[key] = true

			agentID := beads.PolecatBeadIDWithPrefix(prefix, rigName, name)
			issue := agents[agentID]
			if issue == nil {
				slots = append(slots, polecatAdmissionSlot{ActiveAssignment: activeAssignments[key]})
				continue
			}

			fields := beads.ParseAgentFields(issue.Description)
			slots = append(slots, polecatAdmissionSlot{
				HasAgent:         true,
				ActiveAssignment: activeAssignments[key],
				AgentState:       beads.ResolveAgentState(issue.Description, issue.AgentState),
				HookBead:         fields.HookBead,
				CleanupStatus:    fields.CleanupStatus,
				ActiveMR:         fields.ActiveMR,
				PushFailed:       fields.PushFailed,
				MRFailed:         fields.MRFailed,
			})
		}
	}

	for key := range activeAssignments {
		if accounted[key] {
			continue
		}
		slots = append(slots, polecatAdmissionSlot{ActiveAssignment: true})
		accounted[key] = true
	}
	slots = append(slots, admissionOrphanSessionSlots(accounted)...)

	return countAdmissionOccupiedSlots(slots), nil
}

func admissionCapacityRigDirs(townRoot string) ([]string, error) {
	entries, err := os.ReadDir(townRoot)
	if err != nil {
		return nil, err
	}
	var dirs []string
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || entry.Name() == "mayor" || entry.Name() == "settings" {
			continue
		}
		rigDir := filepath.Join(townRoot, entry.Name())
		if info, err := os.Stat(filepath.Join(rigDir, "polecats")); err == nil && info.IsDir() {
			dirs = append(dirs, rigDir)
		}
	}
	return dirs, nil
}

func admissionActivePolecatAssignments(rigDirs []string) (map[string]bool, error) {
	active := make(map[string]bool)
	for _, rigDir := range rigDirs {
		rigName := filepath.Base(rigDir)
		bd := beads.New(rigDir)
		for _, status := range []string{"open", "in_progress", beads.StatusHooked} {
			issues, err := bd.List(beads.ListOptions{Status: status, Priority: -1, Limit: 0})
			if err != nil {
				return nil, err
			}
			for _, issue := range issues {
				prefix := rigName + "/polecats/"
				if !strings.HasPrefix(issue.Assignee, prefix) {
					continue
				}
				name := strings.TrimPrefix(issue.Assignee, prefix)
				if name != "" {
					active[rigName+"/"+name] = true
				}
			}
		}
	}
	return active, nil
}

func admissionOrphanSessionSlots(accounted map[string]bool) []polecatAdmissionSlot {
	listCmd := tmux.BuildCommand("list-sessions", "-F", "#{session_name}")
	out, err := listCmd.Output()
	if err != nil {
		return nil
	}

	var slots []polecatAdmissionSlot
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		identity, err := session.ParseSessionName(line)
		if err != nil || identity.Role != session.RolePolecat {
			continue
		}
		key := identity.Rig + "/" + identity.Name
		if !accounted[key] {
			slots = append(slots, polecatAdmissionSlot{HasAgent: true, OrphanSession: true})
		}
	}
	return slots
}
