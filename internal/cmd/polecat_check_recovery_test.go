package cmd

import (
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
		name            string
		finder          mrFinder
		beadTerminal    bool
		hasWork         bool
		initialVerdict  string
		wantVerdict     string
		wantMQStatus    string
		wantDisposition polecat.Disposition
		wantNeedsMQ     bool
		wantNeedsRecov  bool
	}{
		{
			// The regression this change fixes: assigned bead is CLOSED
			// (e.g. aa-xtee no-op audit). Must NOT return NEEDS_MQ_SUBMIT
			// because there is nothing to submit — the work is terminal.
			name:            "closed bead skips MQ submit check",
			finder:          fakeMRFinder{issue: nil, err: nil},
			beadTerminal:    true,
			hasWork:         true,
			initialVerdict:  "SAFE_TO_NUKE",
			wantVerdict:     "SAFE_TO_NUKE",
			wantMQStatus:    "submitted",
			wantDisposition: polecat.DispositionAvailableClean,
			wantNeedsRecov:  false,
		},
		{
			name:            "no submittable work skips MQ submit check",
			finder:          fakeMRFinder{issue: nil, err: nil},
			beadTerminal:    false,
			hasWork:         false,
			initialVerdict:  "SAFE_TO_NUKE",
			wantVerdict:     "SAFE_TO_NUKE",
			wantMQStatus:    "not_required",
			wantDisposition: polecat.DispositionAvailableClean,
			wantNeedsRecov:  false,
		},
		{
			name:            "open bead with no MR escalates to NEEDS_MQ_SUBMIT",
			finder:          fakeMRFinder{issue: nil, err: nil},
			beadTerminal:    false,
			hasWork:         true,
			initialVerdict:  "SAFE_TO_NUKE",
			wantVerdict:     "NEEDS_MQ_SUBMIT",
			wantMQStatus:    "not_submitted",
			wantDisposition: polecat.DispositionSubmitRequired,
			wantNeedsMQ:     true,
		},
		{
			name:            "open bead with MR is submitted preserved",
			finder:          fakeMRFinder{issue: &beads.Issue{ID: "mr-1"}, err: nil},
			beadTerminal:    false,
			hasWork:         true,
			initialVerdict:  "SAFE_TO_NUKE",
			wantVerdict:     "SUBMITTED_PRESERVED",
			wantMQStatus:    "submitted",
			wantDisposition: polecat.DispositionSubmittedPreserved,
			wantNeedsRecov:  false,
		},
		{
			name:            "MR lookup error is blocked unknown",
			finder:          fakeMRFinder{issue: nil, err: errors.New("bd exploded")},
			beadTerminal:    false,
			hasWork:         true,
			initialVerdict:  "SAFE_TO_NUKE",
			wantVerdict:     "NEEDS_RECOVERY",
			wantMQStatus:    "unknown",
			wantDisposition: polecat.DispositionBlockedUnknown,
			wantNeedsRecov:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := RecoveryStatus{
				Verdict: tt.initialVerdict,
				Branch:  "polecat/test",
			}
			status.applyDisposition(polecat.DispositionAvailableClean)
			applyMQCheck(&status, tt.finder, tt.beadTerminal, tt.hasWork)

			if status.Verdict != tt.wantVerdict {
				t.Errorf("Verdict = %q, want %q", status.Verdict, tt.wantVerdict)
			}
			if status.MQStatus != tt.wantMQStatus {
				t.Errorf("MQStatus = %q, want %q", status.MQStatus, tt.wantMQStatus)
			}
			if status.Disposition != tt.wantDisposition {
				t.Errorf("Disposition = %q, want %q", status.Disposition, tt.wantDisposition)
			}
			if status.NeedsMQSubmit != tt.wantNeedsMQ {
				t.Errorf("NeedsMQSubmit = %v, want %v", status.NeedsMQSubmit, tt.wantNeedsMQ)
			}
			if status.NeedsRecovery != tt.wantNeedsRecov {
				t.Errorf("NeedsRecovery = %v, want %v", status.NeedsRecovery, tt.wantNeedsRecov)
			}
		})
	}
}

func TestActiveMRBlocker(t *testing.T) {
	tests := []struct {
		name string
		mrID string
		bd   issueShower
		want string
	}{
		{
			name: "empty active MR has no blocker",
		},
		{
			name: "closed active MR has no blocker",
			mrID: "mr-1",
			bd:   fakeIssueShower{issue: &beads.Issue{ID: "mr-1", Status: "closed"}},
		},
		{
			name: "open active MR blocks",
			mrID: "mr-1",
			bd:   fakeIssueShower{issue: &beads.Issue{ID: "mr-1", Status: "open"}},
			want: "active_mr=mr-1 status=open",
		},
		{
			name: "missing active MR has no blocker",
			mrID: "mr-1",
			bd:   fakeIssueShower{issue: nil},
		},
		{
			name: "reaped active MR has no blocker",
			mrID: "mr-1",
			bd:   fakeIssueShower{err: beads.ErrNotFound},
		},
		{
			name: "lookup error blocks unknown",
			mrID: "mr-1",
			bd:   fakeIssueShower{err: errors.New("bd exploded")},
			want: "active_mr=mr-1 status=lookup_error: bd exploded",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := activeMRBlocker(tt.bd, tt.mrID); got != tt.want {
				t.Errorf("activeMRBlocker() = %q, want %q", got, tt.want)
			}
		})
	}
}
