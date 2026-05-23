package cmd

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/polecat"
)

// fakeMRFinder is a test stub for the mrFinder interface used by applyMQCheck.
type fakeMRFinder struct {
	issue *beads.Issue
	err   error
}

func (f fakeMRFinder) FindMRForBranchAny(branch string) (*beads.Issue, error) {
	return f.issue, f.err
}

type fakeIssueShower struct {
	issue *beads.Issue
	err   error
}

func (f fakeIssueShower) Show(issueID string) (*beads.Issue, error) {
	return f.issue, f.err
}

func TestApplyMQCheck(t *testing.T) {
	tests := []struct {
		name           string
		finder         mrFinder
		beadTerminal   bool
		hasWork        bool
		initialVerdict string
		wantVerdict    string
		wantMQStatus   string
		wantNeedsRecov bool
	}{
		{
			// The regression this change fixes: assigned bead is CLOSED
			// (e.g. aa-xtee no-op audit). Must NOT return NEEDS_MQ_SUBMIT
			// because there is nothing to submit — the work is terminal.
			name:           "closed bead skips MQ submit check",
			finder:         fakeMRFinder{issue: nil, err: nil},
			beadTerminal:   true,
			hasWork:        true,
			initialVerdict: "SAFE_TO_NUKE",
			wantVerdict:    "SAFE_TO_NUKE",
			wantMQStatus:   "submitted",
			wantNeedsRecov: false,
		},
		{
			name:           "no submittable work skips MQ submit check",
			finder:         fakeMRFinder{issue: nil, err: nil},
			beadTerminal:   false,
			hasWork:        false,
			initialVerdict: "SAFE_TO_NUKE",
			wantVerdict:    "SAFE_TO_NUKE",
			wantMQStatus:   "not_required",
			wantNeedsRecov: false,
		},
		{
			name:           "open bead with no MR escalates to NEEDS_MQ_SUBMIT",
			finder:         fakeMRFinder{issue: nil, err: nil},
			beadTerminal:   false,
			hasWork:        true,
			initialVerdict: "SAFE_TO_NUKE",
			wantVerdict:    "NEEDS_MQ_SUBMIT",
			wantMQStatus:   "not_submitted",
			wantNeedsRecov: true,
		},
		{
			name:           "open bead with MR stays SAFE_TO_NUKE",
			finder:         fakeMRFinder{issue: &beads.Issue{ID: "mr-1"}, err: nil},
			beadTerminal:   false,
			hasWork:        true,
			initialVerdict: "SAFE_TO_NUKE",
			wantVerdict:    "SAFE_TO_NUKE",
			wantMQStatus:   "submitted",
			wantNeedsRecov: false,
		},
		{
			name:           "MR lookup error fails closed",
			finder:         fakeMRFinder{issue: nil, err: errors.New("bd exploded")},
			beadTerminal:   false,
			hasWork:        true,
			initialVerdict: "SAFE_TO_NUKE",
			wantVerdict:    "NEEDS_RECOVERY",
			wantMQStatus:   "unknown",
			wantNeedsRecov: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := RecoveryStatus{
				Verdict: tt.initialVerdict,
				Branch:  "polecat/test",
			}
			applyMQCheck(&status, tt.finder, tt.beadTerminal, tt.hasWork)

			if status.Verdict != tt.wantVerdict {
				t.Errorf("Verdict = %q, want %q", status.Verdict, tt.wantVerdict)
			}
			if status.MQStatus != tt.wantMQStatus {
				t.Errorf("MQStatus = %q, want %q", status.MQStatus, tt.wantMQStatus)
			}
			if status.NeedsRecovery != tt.wantNeedsRecov {
				t.Errorf("NeedsRecovery = %v, want %v", status.NeedsRecovery, tt.wantNeedsRecov)
			}
		})
	}
}

func TestRecoverySourceDoesNotRequireMQ(t *testing.T) {
	tests := []struct {
		name  string
		issue *beads.Issue
		want  bool
	}{
		{name: "missing source requires verification", want: false},
		{name: "open source requires MQ", issue: &beads.Issue{Status: "open"}, want: false},
		{name: "closed source skips MQ", issue: &beads.Issue{Status: "closed"}, want: true},
		{name: "deferred source skips MQ", issue: &beads.Issue{Status: "deferred"}, want: true},
		{name: "escalated source skips MQ", issue: &beads.Issue{Status: "escalated"}, want: true},
		{name: "no merge source skips MQ", issue: &beads.Issue{Status: "open", Description: "no_merge: true"}, want: true},
		{name: "review only source skips MQ", issue: &beads.Issue{Status: "open", Description: "review_only: true"}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := recoverySourceDoesNotRequireMQ(tt.issue); got != tt.want {
				t.Errorf("recoverySourceDoesNotRequireMQ() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRecoveryIssueIDFallsBackToBranch(t *testing.T) {
	tests := []struct {
		name     string
		assigned string
		branch   string
		want     string
	}{
		{name: "assigned issue wins", assigned: "gt-live", branch: "polecat/chrome/gt-old@stamp", want: "gt-live"},
		{name: "polecat branch issue", branch: "polecat/chrome/gt-12-action-leases@mpheebbc", want: "gt-12-action-leases"},
		{name: "non polecat branch issue prefix", branch: "fix/gt-12-action-leases", want: "gt-12"},
		{name: "no issue", branch: "main", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := recoveryIssueID(tt.assigned, tt.branch); got != tt.want {
				t.Errorf("recoveryIssueID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLivePolecatFixtureDispositions(t *testing.T) {
	tests := []struct {
		name           string
		branch         string
		source         *beads.Issue
		gitState       *GitState
		activeMR       string
		activeMRBD     issueShower
		finder         mrFinder
		wantVerdict    string
		wantMQStatus   string
		wantNeedsRecov bool
	}{
		{
			name:         "chrome clean terminal source inferred from branch unblocks",
			branch:       "polecat/chrome/gt-12-action-leases@mpheebbc",
			source:       &beads.Issue{ID: "gt-12-action-leases", Status: "closed"},
			gitState:     &GitState{Clean: true, HasBranchWork: true},
			finder:       fakeMRFinder{},
			wantVerdict:  "SAFE_TO_NUKE",
			wantMQStatus: "submitted",
		},
		{
			name:         "brahmin no merge fork PR source skips internal MQ",
			branch:       "polecat/brahmin/gt-rca-routing-convergence@mpfr891z",
			source:       &beads.Issue{ID: "gt-rca-routing-convergence", Status: "open", Description: "no_merge: true"},
			gitState:     &GitState{Clean: true, HasBranchWork: true},
			finder:       fakeMRFinder{},
			wantVerdict:  "SAFE_TO_NUKE",
			wantMQStatus: "submitted",
		},
		{
			name:           "dust dirty worktree remains blocked",
			branch:         "polecat/dust/gt-12-duplicate-gt-bead-audit@mpigcdab",
			source:         &beads.Issue{ID: "gt-12-duplicate-gt-bead-audit", Status: "open"},
			gitState:       &GitState{UncommittedFiles: []string{"file.go"}},
			wantVerdict:    "NEEDS_RECOVERY",
			wantNeedsRecov: true,
		},
		{
			name:           "guzzle open active MR remains blocked",
			branch:         "polecat/guzzle/gt-12-active-mr-ownership@mphfo6qp",
			source:         &beads.Issue{ID: "gt-12-active-mr-ownership", Status: "closed"},
			gitState:       &GitState{Clean: true, HasBranchWork: true},
			activeMR:       "gt-wisp-ekp",
			activeMRBD:     fakeIssueShower{issue: &beads.Issue{ID: "gt-wisp-ekp", Status: "open"}},
			finder:         fakeMRFinder{},
			wantVerdict:    "NEEDS_RECOVERY",
			wantNeedsRecov: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := evaluateLivePolecatFixtureForTest(tt.branch, tt.source, tt.gitState, tt.activeMR, tt.activeMRBD, tt.finder)
			if status.Verdict != tt.wantVerdict {
				t.Errorf("Verdict = %q, want %q", status.Verdict, tt.wantVerdict)
			}
			if status.MQStatus != tt.wantMQStatus {
				t.Errorf("MQStatus = %q, want %q", status.MQStatus, tt.wantMQStatus)
			}
			if status.NeedsRecovery != tt.wantNeedsRecov {
				t.Errorf("NeedsRecovery = %v, want %v", status.NeedsRecovery, tt.wantNeedsRecov)
			}
		})
	}
}

func evaluateLivePolecatFixtureForTest(branch string, source *beads.Issue, gitState *GitState, activeMR string, activeMRBD issueShower, finder mrFinder) RecoveryStatus {
	status := RecoveryStatus{
		Rig:      "gastown",
		Polecat:  "fixture",
		Branch:   branch,
		Issue:    recoveryIssueID("", branch),
		ActiveMR: activeMR,
	}

	status.GitState = gitState
	status.CleanupStatus = cleanupStatusFromGitState(gitState)
	status.NeedsRecovery = !status.CleanupStatus.IsSafe()
	if status.NeedsRecovery {
		status.Verdict = "NEEDS_RECOVERY"
	} else {
		status.Verdict = "SAFE_TO_NUKE"
	}

	if status.Verdict == "SAFE_TO_NUKE" && activeMR != "" {
		pending, err := activeMRPending(activeMRBD, activeMR)
		if err != nil {
			status.NeedsRecovery = true
			status.Verdict = "NEEDS_RECOVERY"
			status.Reason = err.Error()
		} else if pending {
			status.NeedsRecovery = true
			status.Verdict = "NEEDS_RECOVERY"
			status.Reason = "active_mr " + activeMR + " is still open"
		}
	}

	if status.Verdict == "SAFE_TO_NUKE" && status.Branch != "" {
		beadTerminal := isAssignedBeadTerminal(fakeIssueShower{issue: source}, status.Issue)
		hasSubmittableWork := gitState != nil && (gitState.UnpushedCommits > 0 || gitState.HasBranchWork)
		applyMQCheck(&status, finder, beadTerminal, hasSubmittableWork)
	}

	return status
}

func TestRecoveryPermutationMatrix(t *testing.T) {
	tests := []struct {
		name           string
		hookPresent    bool
		sourceStatus   string
		sessionRunning bool
		gitState       *GitState
		gitErr         error
		activeMR       string
		activeMRBD     issueShower
		finder         mrFinder
		wantCleanup    polecat.CleanupStatus
		wantVerdict    string
		wantMQStatus   string
		wantNeedsRecov bool
	}{
		{
			name:           "hook open running clean no branch work has no MQ requirement",
			hookPresent:    true,
			sourceStatus:   "open",
			sessionRunning: true,
			gitState:       &GitState{Clean: true},
			finder:         fakeMRFinder{},
			wantCleanup:    polecat.CleanupClean,
			wantVerdict:    "SAFE_TO_NUKE",
			wantMQStatus:   "not_required",
		},
		{
			name:         "no hook closed stopped pushed branch without MR is a false positive",
			sourceStatus: "closed",
			gitState:     &GitState{Clean: true, HasBranchWork: true},
			finder:       fakeMRFinder{},
			wantCleanup:  polecat.CleanupClean,
			wantVerdict:  "SAFE_TO_NUKE",
			wantMQStatus: "submitted",
		},
		{
			name:         "hook deferred stopped pushed branch without MR is a false positive",
			hookPresent:  true,
			sourceStatus: "deferred",
			gitState:     &GitState{Clean: true, HasBranchWork: true},
			finder:       fakeMRFinder{},
			wantCleanup:  polecat.CleanupClean,
			wantVerdict:  "SAFE_TO_NUKE",
			wantMQStatus: "submitted",
		},
		{
			name:         "hook escalated stopped pushed branch without MR is a false positive",
			hookPresent:  true,
			sourceStatus: "escalated",
			gitState:     &GitState{Clean: true, HasBranchWork: true},
			finder:       fakeMRFinder{},
			wantCleanup:  polecat.CleanupClean,
			wantVerdict:  "SAFE_TO_NUKE",
			wantMQStatus: "submitted",
		},
		{
			name:           "hook open stopped pushed branch without MR is true risk",
			hookPresent:    true,
			sourceStatus:   "open",
			sessionRunning: false,
			gitState:       &GitState{Clean: true, HasBranchWork: true},
			finder:         fakeMRFinder{},
			wantCleanup:    polecat.CleanupClean,
			wantVerdict:    "NEEDS_MQ_SUBMIT",
			wantMQStatus:   "not_submitted",
			wantNeedsRecov: true,
		},
		{
			name:           "hook open stopped pushed branch with open active MR is true risk",
			hookPresent:    true,
			sourceStatus:   "open",
			sessionRunning: false,
			gitState:       &GitState{Clean: true, HasBranchWork: true},
			activeMR:       "gt-mr-open",
			activeMRBD:     fakeIssueShower{issue: &beads.Issue{ID: "gt-mr-open", Status: "open"}},
			finder:         fakeMRFinder{},
			wantCleanup:    polecat.CleanupClean,
			wantVerdict:    "NEEDS_RECOVERY",
			wantNeedsRecov: true,
		},
		{
			name:           "hook open stopped pushed branch with terminal active MR and MR evidence is safe",
			hookPresent:    true,
			sourceStatus:   "open",
			sessionRunning: false,
			gitState:       &GitState{Clean: true, HasBranchWork: true},
			activeMR:       "gt-mr-closed",
			activeMRBD:     fakeIssueShower{issue: &beads.Issue{ID: "gt-mr-closed", Status: "closed"}},
			finder:         fakeMRFinder{issue: &beads.Issue{ID: "gt-mr-closed", Status: "closed"}},
			wantCleanup:    polecat.CleanupClean,
			wantVerdict:    "SAFE_TO_NUKE",
			wantMQStatus:   "submitted",
		},
		{
			name:           "hook open stopped pushed branch with missing active MR falls back to missing MQ risk",
			hookPresent:    true,
			sourceStatus:   "open",
			sessionRunning: false,
			gitState:       &GitState{Clean: true, HasBranchWork: true},
			activeMR:       "gt-mr-missing",
			activeMRBD:     fakeIssueShower{err: beads.ErrNotFound},
			finder:         fakeMRFinder{},
			wantCleanup:    polecat.CleanupClean,
			wantVerdict:    "NEEDS_MQ_SUBMIT",
			wantMQStatus:   "not_submitted",
			wantNeedsRecov: true,
		},
		{
			name:           "hook open stopped unpushed branch is true risk",
			hookPresent:    true,
			sourceStatus:   "open",
			sessionRunning: false,
			gitState:       &GitState{UnpushedCommits: 1},
			wantCleanup:    polecat.CleanupUnpushed,
			wantVerdict:    "NEEDS_RECOVERY",
			wantNeedsRecov: true,
		},
		{
			name:           "no hook open stopped diverged branch is true risk",
			sourceStatus:   "open",
			sessionRunning: false,
			gitState:       &GitState{NeedsReconcile: true},
			wantCleanup:    polecat.CleanupUnpushed,
			wantVerdict:    "NEEDS_RECOVERY",
			wantNeedsRecov: true,
		},
		{
			name:           "hook open running dirty worktree is true risk",
			hookPresent:    true,
			sourceStatus:   "open",
			sessionRunning: true,
			gitState:       &GitState{UncommittedFiles: []string{"file.go"}},
			wantCleanup:    polecat.CleanupUncommitted,
			wantVerdict:    "NEEDS_RECOVERY",
			wantNeedsRecov: true,
		},
		{
			name:           "hook open running untracked worktree is true risk",
			hookPresent:    true,
			sourceStatus:   "open",
			sessionRunning: true,
			gitState:       &GitState{UncommittedFiles: []string{"new-file.go"}},
			wantCleanup:    polecat.CleanupUncommitted,
			wantVerdict:    "NEEDS_RECOVERY",
			wantNeedsRecov: true,
		},
		{
			name:           "hook open stopped stash is true risk",
			hookPresent:    true,
			sourceStatus:   "open",
			sessionRunning: false,
			gitState:       &GitState{StashCount: 1},
			wantCleanup:    polecat.CleanupStash,
			wantVerdict:    "NEEDS_RECOVERY",
			wantNeedsRecov: true,
		},
		{
			name:           "hook open stopped unknown git state fails closed",
			hookPresent:    true,
			sourceStatus:   "open",
			sessionRunning: false,
			gitErr:         errors.New("status denied"),
			wantCleanup:    polecat.CleanupUnknown,
			wantVerdict:    "NEEDS_RECOVERY",
			wantNeedsRecov: true,
		},
		{
			name:           "hook open stopped pushed branch with unverifiable MQ fails closed",
			hookPresent:    true,
			sourceStatus:   "open",
			sessionRunning: false,
			gitState:       &GitState{Clean: true, HasBranchWork: true},
			finder:         fakeMRFinder{err: errors.New("bd unavailable")},
			wantCleanup:    polecat.CleanupClean,
			wantVerdict:    "NEEDS_RECOVERY",
			wantMQStatus:   "unknown",
			wantNeedsRecov: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.sourceStatus == "" || tt.wantVerdict == "" {
				t.Fatalf("matrix row must explicitly name sourceStatus and wantVerdict")
			}

			status := evaluateRecoveryPermutationForTest(tt.gitState, tt.gitErr, tt.sourceStatus, tt.activeMR, tt.activeMRBD, tt.finder)

			if status.CleanupStatus != tt.wantCleanup {
				t.Errorf("CleanupStatus = %q, want %q", status.CleanupStatus, tt.wantCleanup)
			}
			if status.Verdict != tt.wantVerdict {
				t.Errorf("Verdict = %q, want %q", status.Verdict, tt.wantVerdict)
			}
			if status.MQStatus != tt.wantMQStatus {
				t.Errorf("MQStatus = %q, want %q", status.MQStatus, tt.wantMQStatus)
			}
			if status.NeedsRecovery != tt.wantNeedsRecov {
				t.Errorf("NeedsRecovery = %v, want %v", status.NeedsRecovery, tt.wantNeedsRecov)
			}
		})
	}
}

func evaluateRecoveryPermutationForTest(gitState *GitState, gitErr error, sourceStatus, activeMR string, activeMRBD issueShower, finder mrFinder) RecoveryStatus {
	status := RecoveryStatus{
		Rig:      "gastown",
		Polecat:  "shiny",
		Branch:   "polecat/test",
		ActiveMR: activeMR,
		Issue:    "gt-work",
	}

	if gitErr != nil {
		status.CleanupStatus = polecat.CleanupUnknown
		status.NeedsRecovery = true
		status.Verdict = "NEEDS_RECOVERY"
		status.Reason = gitErr.Error()
	} else {
		status.GitState = gitState
		status.CleanupStatus = cleanupStatusFromGitState(gitState)
		status.NeedsRecovery = !status.CleanupStatus.IsSafe()
		if status.NeedsRecovery {
			status.Verdict = "NEEDS_RECOVERY"
		} else {
			status.Verdict = "SAFE_TO_NUKE"
		}
	}

	if status.Verdict == "SAFE_TO_NUKE" && activeMR != "" {
		pending, err := activeMRPending(activeMRBD, activeMR)
		if err != nil {
			status.NeedsRecovery = true
			status.Verdict = "NEEDS_RECOVERY"
			status.Reason = err.Error()
		} else if pending {
			status.NeedsRecovery = true
			status.Verdict = "NEEDS_RECOVERY"
			status.Reason = "active_mr " + activeMR + " is still open"
		}
	}

	if status.Verdict == "SAFE_TO_NUKE" && status.Branch != "" {
		sourceDoesNotRequireMQ := recoverySourceDoesNotRequireMQ(&beads.Issue{Status: sourceStatus})
		hasSubmittableWork := gitErr != nil || (gitState != nil && (gitState.UnpushedCommits > 0 || gitState.HasBranchWork))
		applyMQCheck(&status, finder, sourceDoesNotRequireMQ, hasSubmittableWork)
	}

	return status
}

func TestRecoveryStatusJSONIncludesBranchAndMREvidence(t *testing.T) {
	status := RecoveryStatus{
		Rig:           "gastown",
		Polecat:       "shiny",
		CleanupStatus: polecat.CleanupClean,
		Verdict:       "SAFE_TO_NUKE",
		Branch:        "polecat/shiny/gt-12-recovery-permutation-tests",
		ActiveMR:      "gt-mr-123",
		GitState:      &GitState{Clean: true, HasBranchWork: true},
		Issue:         "gt-work",
		MQStatus:      "submitted",
	}

	data, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got["branch"] != status.Branch {
		t.Fatalf("branch evidence = %v, want %q", got["branch"], status.Branch)
	}
	if got["active_mr"] != status.ActiveMR {
		t.Fatalf("active_mr evidence = %v, want %q", got["active_mr"], status.ActiveMR)
	}
	gitEvidence, ok := got["git_state"].(map[string]any)
	if !ok {
		t.Fatalf("git_state evidence missing or wrong type: %#v", got["git_state"])
	}
	if gitEvidence["has_branch_work"] != true {
		t.Fatalf("has_branch_work evidence = %v, want true", gitEvidence["has_branch_work"])
	}
}

func TestIsActiveMRTerminal(t *testing.T) {
	tests := []struct {
		name string
		mrID string
		bd   issueShower
		want bool
	}{
		{
			name: "empty active MR is terminal",
			want: true,
		},
		{
			name: "closed active MR is terminal",
			mrID: "mr-1",
			bd:   fakeIssueShower{issue: &beads.Issue{ID: "mr-1", Status: "closed"}},
			want: true,
		},
		{
			name: "open active MR is not terminal",
			mrID: "mr-1",
			bd:   fakeIssueShower{issue: &beads.Issue{ID: "mr-1", Status: "open"}},
			want: false,
		},
		{
			name: "missing active MR is terminal",
			mrID: "mr-1",
			bd:   fakeIssueShower{issue: nil},
			want: true,
		},
		{
			name: "reaped active MR is terminal",
			mrID: "mr-1",
			bd:   fakeIssueShower{err: beads.ErrNotFound},
			want: true,
		},
		{
			name: "lookup error is not terminal",
			mrID: "mr-1",
			bd:   fakeIssueShower{err: errors.New("bd exploded")},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isActiveMRTerminal(tt.bd, tt.mrID); got != tt.want {
				t.Errorf("isActiveMRTerminal() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCleanupStatusFromGitState(t *testing.T) {
	tests := []struct {
		name  string
		state *GitState
		want  string
	}{
		{name: "clean", state: &GitState{Clean: true}, want: "clean"},
		{name: "unpushed", state: &GitState{UnpushedCommits: 1}, want: "has_unpushed"},
		{name: "remote ahead reconcile", state: &GitState{NeedsReconcile: true}, want: "has_unpushed"},
		{name: "stash", state: &GitState{StashCount: 1}, want: "has_stash"},
		{name: "uncommitted", state: &GitState{UncommittedFiles: []string{"file.go"}}, want: "has_uncommitted"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(cleanupStatusFromGitState(tt.state)); got != tt.want {
				t.Errorf("cleanupStatusFromGitState() = %q, want %q", got, tt.want)
			}
		})
	}
}
