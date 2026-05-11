package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

func TestFirstActiveAssignedWork(t *testing.T) {
	tests := []struct {
		name   string
		issues []*beads.Issue
		wantID string
	}{
		{
			name: "nil list",
		},
		{
			name: "ignores inactive statuses",
			issues: []*beads.Issue{
				{ID: "gt-open", Status: "open"},
				{ID: "gt-closed", Status: "closed"},
			},
		},
		{
			name: "returns in progress when no hooked work exists",
			issues: []*beads.Issue{
				{ID: "gt-ip", Status: statusInProgress},
			},
			wantID: "gt-ip",
		},
		{
			name: "prefers hooked over earlier in progress",
			issues: []*beads.Issue{
				{ID: "gt-ip", Status: statusInProgress},
				{ID: "gt-hook", Status: beads.StatusHooked},
			},
			wantID: "gt-hook",
		},
		{
			name: "preserves first hooked work",
			issues: []*beads.Issue{
				{ID: "gt-hook-1", Status: beads.StatusHooked},
				{ID: "gt-hook-2", Status: beads.StatusHooked},
			},
			wantID: "gt-hook-1",
		},
		{
			name: "skips nil entries",
			issues: []*beads.Issue{
				nil,
				{ID: "gt-ip", Status: statusInProgress},
			},
			wantID: "gt-ip",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := firstActiveAssignedWork(tt.issues)
			if tt.wantID == "" {
				if got != nil {
					t.Fatalf("expected nil, got %s", got.ID)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected %s, got nil", tt.wantID)
			}
			if got.ID != tt.wantID {
				t.Fatalf("expected %s, got %s", tt.wantID, got.ID)
			}
		})
	}
}

func TestActiveWorkAssigneeUsesCanonicalHookAddress(t *testing.T) {
	tests := []struct {
		name     string
		ctx      RoleContext
		fallback string
		want     string
	}{
		{
			name:     "mayor uses trailing slash",
			ctx:      RoleContext{Role: RoleMayor},
			fallback: "mayor",
			want:     "mayor/",
		},
		{
			name:     "deacon uses trailing slash",
			ctx:      RoleContext{Role: RoleDeacon},
			fallback: "deacon",
			want:     "deacon/",
		},
		{
			name:     "boot uses deacon boot address",
			ctx:      RoleContext{Role: RoleBoot},
			fallback: "boot",
			want:     "deacon/boot",
		},
		{
			name:     "unknown falls back",
			ctx:      RoleContext{Role: RoleUnknown},
			fallback: "custom",
			want:     "custom",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := activeWorkAssignee(tt.ctx, tt.fallback); got != tt.want {
				t.Fatalf("activeWorkAssignee() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCanonicalActiveWorkTarget(t *testing.T) {
	tests := []struct {
		target string
		want   string
	}{
		{target: "mayor", want: "mayor/"},
		{target: "mayor/", want: "mayor/"},
		{target: "deacon", want: "deacon/"},
		{target: "deacon/", want: "deacon/"},
		{target: "boot", want: "deacon/boot"},
		{target: "deacon-boot", want: "deacon/boot"},
		{target: "gastown/witness", want: "gastown/witness"},
	}

	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			if got := canonicalActiveWorkTarget(tt.target); got != tt.want {
				t.Fatalf("canonicalActiveWorkTarget(%q) = %q, want %q", tt.target, got, tt.want)
			}
		})
	}
}

func TestActiveWorkTargetAliasesIncludeLegacyTownAssignees(t *testing.T) {
	tests := []struct {
		target string
		want   []string
	}{
		{target: "mayor", want: []string{"mayor/", "mayor"}},
		{target: "mayor/", want: []string{"mayor/", "mayor"}},
		{target: "deacon", want: []string{"deacon/", "deacon"}},
		{target: "deacon/", want: []string{"deacon/", "deacon"}},
		{target: "boot", want: []string{"deacon/boot", "boot", "deacon-boot"}},
		{target: "gastown/witness", want: []string{"gastown/witness"}},
	}

	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			got := activeWorkTargetAliases(tt.target)
			if len(got) != len(tt.want) {
				t.Fatalf("aliases = %#v, want %#v", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("aliases = %#v, want %#v", got, tt.want)
				}
			}
		})
	}
}

func TestFindAgentWorkOnceUsesCanonicalTownAssignee(t *testing.T) {
	workDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(workDir, ".beads"), 0700); err != nil {
		t.Fatalf("create .beads: %v", err)
	}

	binDir := filepath.Join(workDir, "bin")
	if err := os.Mkdir(binDir, 0700); err != nil {
		t.Fatalf("create bin dir: %v", err)
	}
	logPath := filepath.Join(workDir, "bd-args.log")
	bdPath := filepath.Join(binDir, "bd")
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %q
case "$*" in
  *"--allow-stale version"*) echo "bd test"; exit 0 ;;
  *"--status=hooked,in_progress"*"--assignee=mayor/"*)
    printf '%%s\n' '[{"id":"hq-work","title":"Hooked work","status":"hooked","priority":2,"issue_type":"task","assignee":"mayor/"}]'
    exit 0
    ;;
esac
printf '%%s\n' '[]'
`, logPath)
	if err := os.WriteFile(bdPath, []byte(script), 0700); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	beads.ResetBdAllowStaleCacheForTest()
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GT_BD_TIMEOUT_SEC", "5")

	issue, err := findAgentWorkOnce(RoleContext{Role: RoleMayor, WorkDir: workDir, TownRoot: workDir}, "mayor")
	if err != nil {
		t.Fatalf("findAgentWorkOnce: %v", err)
	}
	if issue == nil || issue.ID != "hq-work" {
		t.Fatalf("findAgentWorkOnce issue = %#v, want hq-work", issue)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd args log: %v", err)
	}
	logText := string(logData)
	if !strings.Contains(logText, "--assignee=mayor/") {
		t.Fatalf("bd args did not use canonical mayor/ assignee:\n%s", logText)
	}
}

func TestDetectSessionStateUsesTownRootForMayorHome(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(townRoot, ".beads"), 0700); err != nil {
		t.Fatalf("create town .beads: %v", err)
	}
	mayorHome := filepath.Join(townRoot, "mayor")
	if err := os.Mkdir(mayorHome, 0700); err != nil {
		t.Fatalf("create mayor home: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.Mkdir(binDir, 0700); err != nil {
		t.Fatalf("create bin dir: %v", err)
	}
	logPath := filepath.Join(townRoot, "bd-args.log")
	bdPath := filepath.Join(binDir, "bd")
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %q
case "$*" in
  *"--allow-stale version"*) echo "bd test"; exit 0 ;;
  *"show hq-mayor"*) echo "unexpected legacy agent lookup" >&2; exit 2 ;;
  *"--status=hooked --assignee=mayor/"*) echo "unexpected serial hooked lookup" >&2; exit 3 ;;
  *"--status=in_progress --assignee=mayor/"*) echo "unexpected serial in_progress lookup" >&2; exit 4 ;;
  *"--status=hooked,in_progress"*"--assignee=mayor/"*)
    printf '%%s\n' '[{"id":"hq-work","title":"Hooked work","status":"hooked","priority":2,"issue_type":"task","assignee":"mayor/"}]'
    exit 0
    ;;
esac
printf '%%s\n' '[]'
`, logPath)
	if err := os.WriteFile(bdPath, []byte(script), 0700); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	beads.ResetBdAllowStaleCacheForTest()
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GT_BD_TIMEOUT_SEC", "5")

	state := detectSessionState(RoleContext{Role: RoleMayor, WorkDir: mayorHome, TownRoot: townRoot})
	if state.State != "autonomous" {
		t.Fatalf("state.State = %q, want autonomous", state.State)
	}
	if state.HookedBead != "hq-work" {
		t.Fatalf("state.HookedBead = %q, want hq-work", state.HookedBead)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd args log: %v", err)
	}
	logText := string(logData)
	if !strings.Contains(logText, "--assignee=mayor/") {
		t.Fatalf("bd args did not use canonical mayor/ assignee:\n%s", logText)
	}
	if strings.Contains(logText, "show hq-mayor") {
		t.Fatalf("detectSessionState used legacy agent-bead lookup:\n%s", logText)
	}
}

func TestDetectSessionStateFallsBackToLegacyBareTownAssignee(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(townRoot, ".beads"), 0700); err != nil {
		t.Fatalf("create town .beads: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.Mkdir(binDir, 0700); err != nil {
		t.Fatalf("create bin dir: %v", err)
	}
	logPath := filepath.Join(townRoot, "bd-args.log")
	bdPath := filepath.Join(binDir, "bd")
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %q
case "$*" in
  *"--allow-stale version"*) echo "bd test"; exit 0 ;;
  *"--status=hooked,in_progress"*"--assignee=deacon/"*) printf '%%s\n' '[]'; exit 0 ;;
  *"--status=hooked,in_progress"*"--assignee=deacon"*)
    printf '%%s\n' '[{"id":"hq-legacy","title":"Legacy work","status":"hooked","priority":2,"issue_type":"task","assignee":"deacon"}]'
    exit 0
    ;;
esac
printf '%%s\n' '[]'
`, logPath)
	if err := os.WriteFile(bdPath, []byte(script), 0700); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	beads.ResetBdAllowStaleCacheForTest()
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GT_BD_TIMEOUT_SEC", "5")

	state := detectSessionState(RoleContext{Role: RoleDeacon, WorkDir: townRoot, TownRoot: townRoot})
	if state.State != "autonomous" {
		t.Fatalf("state.State = %q, want autonomous", state.State)
	}
	if state.HookedBead != "hq-legacy" {
		t.Fatalf("state.HookedBead = %q, want hq-legacy", state.HookedBead)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd args log: %v", err)
	}
	logText := string(logData)
	if !strings.Contains(logText, "--assignee=deacon/") || !strings.Contains(logText, "--assignee=deacon") {
		t.Fatalf("bd args did not query canonical and legacy deacon assignees:\n%s", logText)
	}
}
