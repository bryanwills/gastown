package cmd

import (
	"fmt"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
)

const (
	activeWorkStatusFilter = "hooked,in_progress"
	statusInProgress       = "in_progress"
)

// findActiveAssignedWork returns the first hooked or in-progress issue for an
// agent with hooked work taking precedence. It intentionally performs one bd
// list call; gt prime and gt hook are startup hot paths.
func findActiveAssignedWork(b *beads.Beads, agentID string) (*beads.Issue, error) {
	if agentID == "" {
		return nil, nil
	}

	issues, err := b.List(beads.ListOptions{
		Status:   activeWorkStatusFilter,
		Assignee: agentID,
		Priority: -1,
	})
	if err != nil {
		return nil, err
	}

	return firstActiveAssignedWork(issues), nil
}

func findActiveAssignedWorkForAssignees(b *beads.Beads, assignees []string) (*beads.Issue, error) {
	for _, assignee := range assignees {
		issue, err := findActiveAssignedWork(b, assignee)
		if err != nil {
			return nil, err
		}
		if issue != nil {
			return issue, nil
		}
	}
	return nil, nil
}

func activeWorkAssignee(ctx RoleContext, fallback string) string {
	assignees := activeWorkAssignees(ctx, fallback)
	if len(assignees) == 0 {
		return ""
	}
	return assignees[0]
}

func activeWorkAssignees(ctx RoleContext, fallback string) []string {
	if assignee := buildAgentIdentity(ctx); assignee != "" {
		return activeWorkTargetAliases(assignee)
	}
	return activeWorkTargetAliases(fallback)
}

func canonicalActiveWorkTarget(target string) string {
	switch strings.TrimRight(target, "/") {
	case "mayor":
		return "mayor/"
	case "deacon":
		return "deacon/"
	case "boot", "deacon-boot":
		return "deacon/boot"
	default:
		return target
	}
}

func activeWorkTargetAliases(target string) []string {
	canonical := canonicalActiveWorkTarget(target)
	aliases := []string{canonical}
	switch canonical {
	case "mayor/":
		aliases = append(aliases, "mayor")
	case "deacon/":
		aliases = append(aliases, "deacon")
	case "deacon/boot":
		aliases = append(aliases, "boot", "deacon-boot")
	}
	return uniqueStrings(aliases)
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		unique = append(unique, value)
	}
	return unique
}

func firstActiveAssignedWork(issues []*beads.Issue) *beads.Issue {
	var firstInProgress *beads.Issue
	for _, issue := range issues {
		if issue == nil {
			continue
		}
		switch issue.Status {
		case beads.StatusHooked:
			return issue
		case statusInProgress:
			if firstInProgress == nil {
				firstInProgress = issue
			}
		}
	}
	return firstInProgress
}

func isActiveWorkStatus(status string) bool {
	return status == beads.StatusHooked || status == statusInProgress
}

func shouldCheckLegacyHookBead(ctx RoleContext) bool {
	switch ctx.Role {
	case RolePolecat, RoleCrew, RoleWitness, RoleRefinery:
		return true
	default:
		return false
	}
}

func shouldCheckLegacyHookBeadBeforeActive(ctx RoleContext) bool {
	return ctx.Role == RolePolecat || ctx.Role == RoleCrew
}

// lookupLegacyAgentHookBead follows the deprecated agent-bead hook_bead slot.
// Normal hook state is issue status + assignee; this is retained only for older
// worker hooks and unresolvable-hook diagnostics.
func lookupLegacyAgentHookBead(ctx RoleContext, agentID string) (*beads.Issue, error) {
	agentBeadID := buildAgentBeadID(agentID, ctx.Role, ctx.TownRoot)
	if agentBeadID == "" {
		return nil, nil
	}

	agentBeadDir := beads.ResolveHookDir(ctx.TownRoot, agentBeadID, ctx.WorkDir)
	agentB := beads.New(agentBeadDir)
	agentBead, err := agentB.Show(agentBeadID)
	if err != nil || agentBead == nil || agentBead.HookBead == "" {
		return nil, nil
	}

	hookBeadDir := beads.ResolveHookDir(ctx.TownRoot, agentBead.HookBead, ctx.WorkDir)
	hookB := beads.New(hookBeadDir)
	hookBead, showErr := hookB.Show(agentBead.HookBead)
	if showErr == nil && hookBead != nil && isActiveWorkStatus(hookBead.Status) {
		return hookBead, nil
	}
	if hookBead == nil || isBeadNotFound(showErr) {
		return nil, fmt.Errorf("%w: agent=%s hook_bead=%s cwd=%s: %v",
			ErrHookUnresolvable, agentID, agentBead.HookBead, ctx.WorkDir, showErr)
	}
	return nil, nil
}
