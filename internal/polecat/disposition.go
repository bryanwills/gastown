package polecat

// Disposition is the canonical, derived workstate for a polecat. It folds
// lifecycle state, cleanup metadata, git observations, and merge-queue evidence
// into one allowed value so callers derive policy from one source of truth.
type Disposition string

const (
	// DispositionAvailableClean means the polecat has no work at risk and can be
	// reused or destructively removed.
	DispositionAvailableClean Disposition = "available-clean"

	// DispositionActiveWork means the polecat is still attached to active work.
	DispositionActiveWork Disposition = "active-work"

	// DispositionSubmittedPreserved means work is submitted/pending elsewhere and
	// the sandbox may be reused, but destructive cleanup must preserve evidence.
	DispositionSubmittedPreserved Disposition = "submitted-preserved"

	// DispositionSubmitRequired means local metadata indicates completed work that
	// still needs merge-queue submission before cleanup.
	DispositionSubmitRequired Disposition = "submit-required"

	// DispositionRecoverLocal means local git state or failed completion metadata
	// contains work that needs recovery before reuse or cleanup.
	DispositionRecoverLocal Disposition = "recover-local"

	// DispositionReconcileMetadata means metadata is internally inconsistent but
	// does not prove local work is at risk. A reconciler should repair it first.
	DispositionReconcileMetadata Disposition = "reconcile-metadata"

	// DispositionBlockedUnknown means the helper cannot classify the polecat
	// safely, so capacity/reuse/destructive cleanup must fail closed.
	DispositionBlockedUnknown Disposition = "blocked-unknown"
)

// WorkstateInput contains the raw observations used to derive a Disposition.
// Callers should not make policy decisions from these fields directly.
type WorkstateInput struct {
	State              State
	HookBead           string
	CleanupStatus      CleanupStatus
	ActiveMR           string
	ActiveMRBlocks     bool
	PushFailed         bool
	MRFailed           bool
	GitDirty           bool
	StashCount         int
	UnpushedCommits    int
	GitCheckFailed     bool
	HasSubmittableWork bool
	MQSubmitted        bool
	MQStatusUnknown    bool
}

// WorkstateDisposition includes the canonical disposition and a stable reason
// for diagnostics. Predicates must be derived from Disposition, not Reason.
type WorkstateDisposition struct {
	Disposition Disposition
	Reason      string
}

// ResolveWorkstateDisposition derives the only allowed polecat disposition from
// raw observations. The ordering intentionally fails closed: active work, local
// recovery, and unknown checks take precedence over availability.
func ResolveWorkstateDisposition(in WorkstateInput) WorkstateDisposition {
	switch in.State {
	case StateIdle:
		// Continue with idle-specific evidence below.
	case StateWorking, StateDone:
		return WorkstateDisposition{Disposition: DispositionActiveWork, Reason: "not-idle"}
	case StateStuck, StateStalled, StateZombie:
		return WorkstateDisposition{Disposition: DispositionBlockedUnknown, Reason: "state-" + string(in.State)}
	default:
		return WorkstateDisposition{Disposition: DispositionBlockedUnknown, Reason: "state-unknown"}
	}

	if in.HookBead != "" {
		return WorkstateDisposition{Disposition: DispositionReconcileMetadata, Reason: "hook-still-set"}
	}
	if in.PushFailed {
		return WorkstateDisposition{Disposition: DispositionRecoverLocal, Reason: "push-failed"}
	}
	if in.MRFailed {
		return WorkstateDisposition{Disposition: DispositionSubmitRequired, Reason: "mr-failed"}
	}
	if in.GitCheckFailed {
		return WorkstateDisposition{Disposition: DispositionBlockedUnknown, Reason: "git-check-failed"}
	}

	if in.CleanupStatus == "" || in.CleanupStatus == CleanupUnknown {
		return WorkstateDisposition{Disposition: DispositionReconcileMetadata, Reason: "cleanup-unknown"}
	}
	if in.CleanupStatus.RequiresRecovery() {
		return WorkstateDisposition{Disposition: DispositionRecoverLocal, Reason: "cleanup-" + string(in.CleanupStatus)}
	}
	if !in.CleanupStatus.IsSafe() {
		return WorkstateDisposition{Disposition: DispositionBlockedUnknown, Reason: "cleanup-" + string(in.CleanupStatus)}
	}

	if in.GitDirty {
		return WorkstateDisposition{Disposition: DispositionRecoverLocal, Reason: "git-dirty"}
	}
	if in.StashCount > 0 {
		return WorkstateDisposition{Disposition: DispositionRecoverLocal, Reason: "git-stash"}
	}
	if in.UnpushedCommits > 0 {
		return WorkstateDisposition{Disposition: DispositionRecoverLocal, Reason: "git-unpushed"}
	}

	if in.ActiveMR != "" && in.ActiveMRBlocks {
		return WorkstateDisposition{Disposition: DispositionSubmittedPreserved, Reason: "active-mr-open"}
	}
	if in.HasSubmittableWork {
		if in.MQStatusUnknown {
			return WorkstateDisposition{Disposition: DispositionBlockedUnknown, Reason: "mq-unknown"}
		}
		if !in.MQSubmitted {
			return WorkstateDisposition{Disposition: DispositionSubmitRequired, Reason: "mq-not-submitted"}
		}
		return WorkstateDisposition{Disposition: DispositionSubmittedPreserved, Reason: "mq-submitted"}
	}

	return WorkstateDisposition{Disposition: DispositionAvailableClean, Reason: "available-clean"}
}

// Reusable reports whether this disposition can accept new work using normal
// branch-only reuse helpers.
func (d Disposition) Reusable() bool {
	return d == DispositionAvailableClean || d == DispositionSubmittedPreserved
}

// SlotOpenEligible reports whether schedulers may treat the polecat as an open
// capacity slot.
func (d Disposition) SlotOpenEligible() bool {
	return d.Reusable()
}

// SafeToNuke reports whether destructive sandbox removal is allowed without
// preserving submitted work or recovering local data.
func (d Disposition) SafeToNuke() bool {
	return d == DispositionAvailableClean
}

// NeedsMQSubmit reports whether completed work still needs merge-queue submit.
func (d Disposition) NeedsMQSubmit() bool {
	return d == DispositionSubmitRequired
}

// NeedsRecovery reports whether local work must be recovered before cleanup.
func (d Disposition) NeedsRecovery() bool {
	return d == DispositionRecoverLocal
}

// CountsAgainstCapacity reports whether the polecat should still occupy a
// capacity slot.
func (d Disposition) CountsAgainstCapacity() bool {
	return !d.SlotOpenEligible()
}
