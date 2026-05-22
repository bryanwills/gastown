package beads

import (
	"errors"
	"fmt"
)

// SourceTransition names the durable state transition for a work source bead.
type SourceTransition string

const (
	SourceTransitionSlingAssign     SourceTransition = "sling-assign"
	SourceTransitionDoneSubmittedMR SourceTransition = "done-submitted-mr"
	SourceTransitionMergeSucceeded  SourceTransition = "merge-succeeded"
	SourceTransitionDirectDone      SourceTransition = "direct-done"
	SourceTransitionDoneDeferred    SourceTransition = "done-deferred"
	SourceTransitionDoneEscalated   SourceTransition = "done-escalated"
	SourceTransitionRecoveryReset   SourceTransition = "recovery-reset"
)

// SourceTransitionOptions describes a single idempotent source issue transition.
type SourceTransitionOptions struct {
	Transition    SourceTransition
	SourceIssueID string
	Assignee      string
	Reason        string
	MRID          string
	TargetBranch  string
	CommitSHA     string
}

// SourceTransitionResult reports what TransitionSourceIssue changed.
type SourceTransitionResult struct {
	SourceIssueID          string
	Transition             SourceTransition
	SourceUpdated          bool
	SourceClosed           bool
	SourceAlreadyTerminal  bool
	SourceNotFound         bool
	AttachedMoleculeID     string
	AttachedMoleculeClosed bool
	DescendantsClosed      int
}

// TransitionSourceIssue applies the canonical source-bead lifecycle transition.
// It is safe to call repeatedly: terminal source beads are treated as already done.
func (b *Beads) TransitionSourceIssue(opts SourceTransitionOptions) (*SourceTransitionResult, error) {
	result := &SourceTransitionResult{SourceIssueID: opts.SourceIssueID, Transition: opts.Transition}
	if opts.SourceIssueID == "" {
		return result, nil
	}

	issue, err := b.Show(opts.SourceIssueID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			result.SourceNotFound = true
			return result, nil
		}
		return result, err
	}
	if issue == nil {
		result.SourceNotFound = true
		return result, nil
	}

	if IssueStatus(issue.Status).IsTerminal() {
		result.SourceAlreadyTerminal = true
		return result, nil
	}

	switch opts.Transition {
	case SourceTransitionSlingAssign:
		return result, b.updateSourceState(result, opts.SourceIssueID, string(IssueStatusHooked), opts.Assignee)
	case SourceTransitionDoneSubmittedMR:
		return result, b.updateSourceState(result, opts.SourceIssueID, string(StatusInProgress), "")
	case SourceTransitionDoneDeferred:
		return result, b.updateSourceState(result, opts.SourceIssueID, string(StatusOpen), "")
	case SourceTransitionDoneEscalated:
		return result, b.updateSourceState(result, opts.SourceIssueID, string(StatusBlocked), "")
	case SourceTransitionRecoveryReset:
		return result, b.updateSourceState(result, opts.SourceIssueID, string(StatusInProgress), opts.Assignee)
	case SourceTransitionMergeSucceeded, SourceTransitionDirectDone:
		if err := b.closeAttachedMolecule(result, issue); err != nil {
			return result, err
		}
		reason := opts.Reason
		if reason == "" {
			reason = sourceCloseReason(opts)
		}
		if err := b.ForceCloseWithReason(reason, opts.SourceIssueID); err != nil {
			if refreshed, showErr := b.Show(opts.SourceIssueID); showErr == nil && refreshed != nil && IssueStatus(refreshed.Status).IsTerminal() {
				result.SourceAlreadyTerminal = true
				return result, nil
			}
			return result, err
		}
		result.SourceClosed = true
		return result, nil
	default:
		return result, fmt.Errorf("unknown source transition %q", opts.Transition)
	}
}

func (b *Beads) updateSourceState(result *SourceTransitionResult, sourceIssueID, status, assignee string) error {
	if err := b.Update(sourceIssueID, UpdateOptions{Status: &status, Assignee: &assignee}); err != nil {
		return err
	}
	result.SourceUpdated = true
	return nil
}

func (b *Beads) closeAttachedMolecule(result *SourceTransitionResult, issue *Issue) error {
	attachment := ParseAttachmentFields(issue)
	if attachment == nil || attachment.AttachedMolecule == "" {
		return nil
	}
	result.AttachedMoleculeID = attachment.AttachedMolecule
	closed, err := b.forceCloseDescendants(attachment.AttachedMolecule)
	result.DescendantsClosed = closed
	if err != nil {
		return err
	}
	if err := b.ForceCloseWithReason("done", attachment.AttachedMolecule); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	result.AttachedMoleculeClosed = true
	return nil
}

func (b *Beads) forceCloseDescendants(parentID string) (int, error) {
	children, err := b.List(ListOptions{Parent: parentID, Status: "all"})
	if err != nil {
		return 0, fmt.Errorf("listing children of %s: %w", parentID, err)
	}
	if len(children) == 0 {
		return 0, nil
	}

	total := 0
	var errs []error
	for _, child := range children {
		closed, childErr := b.forceCloseDescendants(child.ID)
		total += closed
		if childErr != nil {
			errs = append(errs, childErr)
		}
	}

	var ids []string
	for _, child := range children {
		if !IssueStatus(child.Status).IsTerminal() {
			ids = append(ids, child.ID)
		}
	}
	if len(ids) > 0 {
		if err := b.ForceCloseWithReason("burned: force-close descendants", ids...); err != nil {
			errs = append(errs, fmt.Errorf("closing children of %s: %w", parentID, err))
		} else {
			total += len(ids)
		}
	}

	return total, errors.Join(errs...)
}

func sourceCloseReason(opts SourceTransitionOptions) string {
	switch opts.Transition {
	case SourceTransitionMergeSucceeded:
		reason := "Merged"
		if opts.MRID != "" {
			reason = fmt.Sprintf("Merged in %s", opts.MRID)
		}
		if opts.CommitSHA != "" {
			reason = fmt.Sprintf("%s\ntarget_branch: %s\ncommit_sha: %s", reason, opts.TargetBranch, opts.CommitSHA)
		}
		return reason
	case SourceTransitionDirectDone:
		if opts.TargetBranch != "" {
			return fmt.Sprintf("Direct merge to %s", opts.TargetBranch)
		}
		return "Completed without merge request"
	default:
		return string(opts.Transition)
	}
}
