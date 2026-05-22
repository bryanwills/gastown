package polecat

import "errors"

// ErrPolecatNeedsRecovery marks an idle-looking polecat that must not be reset
// or advertised as reusable until its preserved work is recovered or submitted.
var ErrPolecatNeedsRecovery = errors.New("polecat needs recovery before reuse")

// SlotReuseInput is the shared input for deciding whether a polecat slot can be
// advertised as open and destructively reused for new work.
type SlotReuseInput struct {
	State              State
	HookBead           string
	CleanupStatus      CleanupStatus
	ActiveMR           string
	ActiveMRBlocks     bool
	PushFailed         bool
	MRFailed           bool
	Branch             string
	GitDirty           bool
	StashCount         int
	UnpushedCommits    int
	GitCheckFailed     bool
	HasSubmittableWork bool
	MQSubmitted        bool
	MQStatusUnknown    bool
}

// SlotReuseDecision explains whether a polecat can be reused and why not.
type SlotReuseDecision struct {
	Reusable bool
	Reason   string
}

// DecideSlotReuse is the single source of truth for reuse safety. It fails
// closed: unknown cleanup/git state means the slot needs recovery, not reuse.
func DecideSlotReuse(in SlotReuseInput) SlotReuseDecision {
	resolved := ResolveWorkstateDisposition(WorkstateInput{
		State:              in.State,
		HookBead:           in.HookBead,
		CleanupStatus:      in.CleanupStatus,
		ActiveMR:           in.ActiveMR,
		ActiveMRBlocks:     in.ActiveMRBlocks,
		PushFailed:         in.PushFailed,
		MRFailed:           in.MRFailed,
		GitDirty:           in.GitDirty,
		StashCount:         in.StashCount,
		UnpushedCommits:    in.UnpushedCommits,
		GitCheckFailed:     in.GitCheckFailed,
		HasSubmittableWork: in.HasSubmittableWork,
		MQSubmitted:        in.MQSubmitted,
		MQStatusUnknown:    in.MQStatusUnknown,
	})
	if resolved.Disposition.Reusable() {
		return SlotReuseDecision{Reusable: true, Reason: "reusable"}
	}
	return SlotReuseDecision{Reason: resolved.Reason}
}
