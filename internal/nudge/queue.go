// Package nudge provides non-destructive nudge delivery for Gas Town agents.
//
// The nudge queue allows messages to be delivered cooperatively: instead of
// sending text directly to a tmux session (which cancels in-flight tool calls),
// nudges are written to a queue directory and picked up by the agent's
// UserPromptSubmit hook at the next natural turn boundary.
//
// Queue location: <townRoot>/.runtime/nudge_queue/<session>/
// Each nudge is a JSON file named by timestamp for FIFO ordering.
package nudge

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
)

// Priority levels for nudge delivery.
const (
	// PriorityNormal is the default — delivered at next turn boundary.
	PriorityNormal = "normal"
	// PriorityUrgent means the agent should handle this promptly.
	PriorityUrgent = "urgent"
)

// Operational limits and defaults.
// These are compiled-in fallbacks. Configurable via operational.nudge
// in settings/config.json (ZFC pattern).
const (
	// DefaultNormalTTL is the time-to-live for normal-priority nudges.
	DefaultNormalTTL = 30 * time.Minute

	// DefaultUrgentTTL is the time-to-live for urgent-priority nudges.
	DefaultUrgentTTL = 2 * time.Hour

	// MaxQueueDepth is the maximum number of pending nudges per session.
	MaxQueueDepth = 50

	// staleClaimThreshold is how long a .claimed file must be untouched
	// before Drain considers it orphaned (from a crashed drainer) and removes it.
	staleClaimThreshold = 5 * time.Minute
)

// nudgeConfig loads nudge-specific thresholds from town settings.
func nudgeConfig(townRoot string) *config.NudgeThresholds {
	return config.LoadOperationalConfig(townRoot).GetNudgeConfig()
}

// QueuedNudge represents a nudge message stored in the queue.
type QueuedNudge struct {
	Sender    string    `json:"sender"`
	Message   string    `json:"message"`
	Priority  string    `json:"priority"`
	Kind      string    `json:"kind,omitempty"`
	ThreadID  string    `json:"thread_id,omitempty"`
	Severity  string    `json:"severity,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	// DeliverAfter, if non-zero, defers delivery until this time has passed.
	// Drain skips (but does not discard) the nudge until the deadline is met.
	DeliverAfter time.Time `json:"deliver_after,omitempty"`
}

type recentRoutineState map[string]time.Time

// queueDir returns the nudge queue directory for a given session.
// Path: <townRoot>/.runtime/nudge_queue/<session>/
func queueDir(townRoot, session string) string {
	// Sanitize session name for filesystem safety
	safe := strings.ReplaceAll(session, "/", "_")
	return filepath.Join(townRoot, constants.DirRuntime, "nudge_queue", safe)
}

func recentRoutinePath(townRoot, session string) string {
	return filepath.Join(queueDir(townRoot, session), ".recent-routine")
}

// randomSuffix returns a short random hex string to disambiguate filenames
// when multiple processes enqueue within the same nanosecond.
func randomSuffix() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Enqueue writes a nudge to the queue for the given session.
// The nudge will be picked up by the agent's hook at the next turn boundary.
// Returns an error if the queue is full (MaxQueueDepth reached).
func Enqueue(townRoot, session string, nudge QueuedNudge) error {
	return enqueue(townRoot, session, nudge, true)
}

func enqueue(townRoot, session string, nudge QueuedNudge, suppressRoutineDuplicates bool) error {
	dir := queueDir(townRoot, session)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating nudge queue dir: %w", err)
	}

	nudge = normalizeQueuedNudge(nudge)
	if suppressRoutineDuplicates && shouldSuppressRoutineDuplicate(townRoot, session, nudge) {
		return nil
	}

	// Check queue depth before writing to prevent runaway senders.
	maxDepth := nudgeConfig(townRoot).MaxQueueDepthV()
	pending, _ := Pending(townRoot, session)
	if pending >= maxDepth {
		return fmt.Errorf("nudge queue for %s is full (%d/%d pending)", session, pending, maxDepth)
	}

	// Set expiry if not already specified by the caller.
	if nudge.ExpiresAt.IsZero() {
		switch nudge.Priority {
		case PriorityUrgent:
			nudge.ExpiresAt = nudge.Timestamp.Add(DefaultUrgentTTL)
		default:
			nudge.ExpiresAt = nudge.Timestamp.Add(DefaultNormalTTL)
		}
	}

	data, err := json.MarshalIndent(nudge, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling nudge: %w", err)
	}

	// Use nanosecond timestamp + random suffix for unique, ordered filenames.
	// The random suffix prevents collisions when multiple agents enqueue
	// nudges for the same session within the same nanosecond.
	filename := fmt.Sprintf("%d-%s.json", nudge.Timestamp.UnixNano(), randomSuffix())
	path := filepath.Join(dir, filename)

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("writing nudge to queue: %w", err)
	}

	return nil
}

func normalizeQueuedNudge(n QueuedNudge) QueuedNudge {
	if n.Timestamp.IsZero() {
		n.Timestamp = time.Now()
	}
	if n.Priority == "" {
		n.Priority = PriorityNormal
	}
	if n.Severity == "" {
		n.Severity = severityForPriority(n.Priority)
	}
	if n.Kind == "" {
		n.Kind = deriveNudgeKind(n.Message)
	}
	if n.ThreadID == "" && isSuppressibleRoutineKind(n.Kind) {
		n.ThreadID = routineThreadID(n.Message)
	}
	return n
}

func severityForPriority(priority string) string {
	switch priority {
	case PriorityUrgent:
		return "high"
	default:
		return "routine"
	}
}

func deriveNudgeKind(message string) string {
	normalized := strings.ToUpper(strings.TrimSpace(message))
	known := []string{
		"SLOT_OPEN", "SLOT_BLOCKED", "MERGE_READY", "MERGE_FAILED", "MERGED",
		"MR_REJECTED", "CONVOY_NEEDS_FEEDING", "RECOVERY_NEEDED", "SPAWN_BLOCKED",
		"RECOVERED_BEAD", "HEALTH_CHECK", "POLECAT_DONE",
	}
	for _, kind := range known {
		if strings.HasPrefix(normalized, kind+":") || strings.HasPrefix(normalized, kind+" ") || normalized == kind {
			return strings.ToLower(kind)
		}
	}
	if strings.Contains(normalized, "MERGE QUEUE") || strings.Contains(normalized, "NEW MR AVAILABLE") {
		return "merge-queue"
	}
	if strings.Contains(normalized, "<SYSTEM-REMINDER>") {
		return "system-reminder"
	}
	return ""
}

func routineThreadID(message string) string {
	compact := strings.Join(strings.Fields(message), " ")
	if len(compact) <= 160 {
		return compact
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(compact))
	return fmt.Sprintf("%s#%x", compact[:120], h.Sum64())
}

func actionabilityKey(n QueuedNudge) string {
	n = normalizeQueuedNudge(n)
	if n.Kind == "" || n.ThreadID == "" {
		return ""
	}
	return strings.ToLower(n.Kind) + "\x00" + n.ThreadID
}

func isRoutineNudge(n QueuedNudge) bool {
	n = normalizeQueuedNudge(n)
	if n.Priority == PriorityUrgent {
		return false
	}
	severity := strings.ToLower(n.Severity)
	return severity == "" || severity == "routine" || severity == "normal" || severity == "low" || severity == "medium"
}

func shouldSuppressRoutineDuplicate(townRoot, session string, n QueuedNudge) bool {
	if !isRoutineNudge(n) {
		return false
	}
	if !isSuppressibleRoutineKind(n.Kind) {
		return false
	}
	key := actionabilityKey(n)
	if key == "" {
		return false
	}
	cooldown := nudgeConfig(townRoot).RoutineDuplicateCooldownD()
	if cooldown <= 0 {
		return false
	}

	now := n.Timestamp
	state := loadRecentRoutine(townRoot, session)
	for k, last := range state {
		if now.Sub(last) > cooldown {
			delete(state, k)
		}
	}
	if last, ok := state[key]; ok && now.Sub(last) <= cooldown {
		return true
	}
	state[key] = now
	storeRecentRoutine(townRoot, session, state)
	return false
}

func isSuppressibleRoutineKind(kind string) bool {
	switch strings.ToLower(kind) {
	case "slot_open", "slot-open", "slot_blocked", "slot-blocked",
		"merge_ready", "merge-ready", "merge-queue",
		"system-reminder", "health_check", "health-check",
		"convoy_needs_feeding", "convoy-needs-feeding":
		return true
	default:
		return false
	}
}

func loadRecentRoutine(townRoot, session string) recentRoutineState {
	data, err := os.ReadFile(recentRoutinePath(townRoot, session))
	if err != nil {
		return recentRoutineState{}
	}
	var state recentRoutineState
	if err := json.Unmarshal(data, &state); err != nil || state == nil {
		return recentRoutineState{}
	}
	return state
}

func storeRecentRoutine(townRoot, session string, state recentRoutineState) {
	data, err := json.Marshal(state)
	if err != nil {
		return
	}
	_ = os.WriteFile(recentRoutinePath(townRoot, session), data, 0644)
}

// Requeue writes previously drained nudges back to the queue for later delivery.
// Existing timestamps are preserved so FIFO ordering remains stable relative to
// one another; only expired nudges are skipped.
func Requeue(townRoot, session string, nudges []QueuedNudge) error {
	for _, n := range nudges {
		if !n.ExpiresAt.IsZero() && time.Now().After(n.ExpiresAt) {
			continue
		}
		if err := enqueue(townRoot, session, n, false); err != nil {
			return err
		}
	}
	return nil
}

// Drain reads and removes all queued nudges for a session, returning them
// in FIFO order. This is called by the hook to pick up pending nudges.
//
// Uses rename-then-process to prevent concurrent Drain calls from delivering
// the same nudge twice: each file is atomically renamed to a .claimed suffix
// before reading, so only one caller can claim each nudge.
//
// Expired nudges (past ExpiresAt) are silently discarded during drain.
// Orphaned .claimed files from crashed drainers are swept if older than 5 minutes.
func Drain(townRoot, session string) ([]QueuedNudge, error) {
	dir := queueDir(townRoot, session)

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading nudge queue: %w", err)
	}

	// Requeue orphaned .claimed files from crashed drainers.
	// A .claimed file older than staleClaimThreshold is certainly orphaned —
	// normal processing completes in milliseconds. We rename it back to .json
	// so it gets picked up on this or a future Drain call, rather than deleting
	// it (which would permanently drop the nudge).
	staleThreshold := nudgeConfig(townRoot).StaleClaimThresholdD()
	now := time.Now()
	for _, entry := range entries {
		if !strings.Contains(entry.Name(), ".claimed") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) > staleThreshold {
			orphanPath := filepath.Join(dir, entry.Name())
			// Strip everything from ".claimed" onward to restore original .json filename
			name := entry.Name()
			claimedIdx := strings.Index(name, ".claimed")
			restoredPath := filepath.Join(dir, name[:claimedIdx])
			if err := os.Rename(orphanPath, restoredPath); err != nil {
				// Rename failed — remove as last resort to prevent infinite accumulation
				fmt.Fprintf(os.Stderr, "Warning: failed to requeue orphaned claim %s: %v\n", entry.Name(), err)
				_ = os.Remove(orphanPath)
			}
		}
	}

	// Sort by name (timestamp-based) for FIFO ordering
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	var nudges []QueuedNudge
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		path := filepath.Join(dir, entry.Name())

		// Atomically claim the file by renaming it. If another Drain call
		// is racing us, only one rename will succeed — the loser gets
		// ENOENT and moves on. This prevents double-delivery.
		//
		// Each drainer uses a unique claim suffix to avoid destination
		// collisions. On Windows, os.Rename to a shared destination is
		// not atomic — two goroutines can both "succeed" via
		// MOVEFILE_REPLACE_EXISTING, causing data loss. Unique suffixes
		// ensure each rename has a distinct target.
		claimPath := path + ".claimed." + randomSuffix()
		if err := os.Rename(path, claimPath); err != nil {
			// Another Drain got it first, or file was already removed
			continue
		}

		data, err := os.ReadFile(claimPath)
		if err != nil {
			if os.IsNotExist(err) {
				// File vanished between rename and read — treat as lost race
				continue
			}
			// Transient read error (e.g., Windows AV/indexer holding a share
			// lock) — unclaim so the nudge can be retried on a future Drain
			// call rather than permanently lost.
			_ = os.Rename(claimPath, path) // best-effort unclaim; orphan sweep catches failures
			continue
		}

		var n QueuedNudge
		if err := json.Unmarshal(data, &n); err != nil {
			// Malformed — clean up
			if rmErr := os.Remove(claimPath); rmErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to remove malformed claim %s: %v\n", entry.Name(), rmErr)
			}
			continue
		}

		// Skip expired nudges — stale messages create noise, not value.
		if !n.ExpiresAt.IsZero() && now.After(n.ExpiresAt) {
			if rmErr := os.Remove(claimPath); rmErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to remove expired nudge %s: %v\n", entry.Name(), rmErr)
			}
			continue
		}

		// Deferred nudge: not ready yet — unclaim and leave in queue.
		if !n.DeliverAfter.IsZero() && now.Before(n.DeliverAfter) {
			if renameErr := os.Rename(claimPath, path); renameErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to unclaim deferred nudge %s: %v\n", entry.Name(), renameErr)
			}
			continue
		}

		nudges = append(nudges, n)

		// Remove the claimed file after successful processing
		if rmErr := os.Remove(claimPath); rmErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to remove processed claim %s: %v\n", entry.Name(), rmErr)
		}
	}

	return nudges, nil
}

// Pending returns the count of queued nudges for a session without draining.
// This is an approximate count — it does not check expiry or read file contents.
func Pending(townRoot, session string) (int, error) {
	dir := queueDir(townRoot, session)

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("reading nudge queue: %w", err)
	}

	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			count++
		}
	}

	return count, nil
}

// QueueLen returns the number of pending nudges for a session without draining.
// Returns 0 on error — callers use this for quick checks. Missing queue
// directories are expected (no nudges yet) and silenced; other filesystem
// errors are logged to stderr so they don't go unnoticed.
func QueueLen(townRoot, session string) int {
	n, err := Pending(townRoot, session)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: nudge queue check failed for %s: %v\n", session, err)
	}
	return n
}

// RemoveKindByThread deletes queued nudges for a session that match both the
// provided kind and thread ID. It only removes queued .json files, leaving any
// in-flight claimed files alone so concurrent drainers can finish safely.
func RemoveKindByThread(townRoot, session, kind, threadID string) (int, error) {
	if kind == "" || threadID == "" {
		return 0, nil
	}

	dir := queueDir(townRoot, session)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("reading nudge queue: %w", err)
	}

	removed := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return removed, fmt.Errorf("reading queued nudge %s: %w", entry.Name(), err)
		}

		var n QueuedNudge
		if err := json.Unmarshal(data, &n); err != nil {
			continue
		}
		if n.Kind != kind || n.ThreadID != threadID {
			continue
		}

		if err := os.Remove(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return removed, fmt.Errorf("removing queued nudge %s: %w", entry.Name(), err)
		}
		removed++
	}

	return removed, nil
}

// FormatForInjection formats queued nudges as a system-reminder block
// suitable for Claude Code hook output.
func FormatForInjection(nudges []QueuedNudge) string {
	if len(nudges) == 0 {
		return ""
	}
	nudges, counts := collapseForInjection(nudges)

	var b strings.Builder
	b.WriteString("<system-reminder>\n")

	// Separate urgent from normal
	var urgent, normal []QueuedNudge
	for _, n := range nudges {
		if n.Priority == PriorityUrgent {
			urgent = append(urgent, n)
		} else {
			normal = append(normal, n)
		}
	}

	if len(urgent) > 0 {
		b.WriteString(fmt.Sprintf("QUEUED NUDGE (%d urgent):\n\n", len(urgent)))
		for _, n := range urgent {
			b.WriteString(fmt.Sprintf("  [URGENT from %s] %s%s\n", n.Sender, n.Message, repeatSuffix(n, counts)))
		}
		if len(normal) > 0 {
			b.WriteString(fmt.Sprintf("\nPlus %d non-urgent nudge(s):\n", len(normal)))
			for _, n := range normal {
				b.WriteString(fmt.Sprintf("  [from %s] %s%s\n", n.Sender, n.Message, repeatSuffix(n, counts)))
			}
		}
		b.WriteString("\nHandle urgent nudges before continuing current work.\n")
	} else {
		b.WriteString(fmt.Sprintf("QUEUED NUDGE (%d message(s)):\n\n", len(normal)))
		for _, n := range normal {
			b.WriteString(fmt.Sprintf("  [from %s] %s%s\n", n.Sender, n.Message, repeatSuffix(n, counts)))
		}
		b.WriteString("\nThis is a background notification. Continue current work unless the nudge is higher priority.\n")
	}

	b.WriteString("</system-reminder>\n")
	return b.String()
}

func collapseForInjection(nudges []QueuedNudge) ([]QueuedNudge, map[string]int) {
	result := make([]QueuedNudge, 0, len(nudges))
	counts := make(map[string]int)
	seen := make(map[string]int)
	for _, n := range nudges {
		n = normalizeQueuedNudge(n)
		key := ""
		if isRoutineNudge(n) {
			key = actionabilityKey(n)
		}
		if key == "" {
			result = append(result, n)
			continue
		}
		if idx, ok := seen[key]; ok {
			counts[key]++
			result[idx] = n
			continue
		}
		seen[key] = len(result)
		counts[key] = 1
		result = append(result, n)
	}
	return result, counts
}

func repeatSuffix(n QueuedNudge, counts map[string]int) string {
	key := actionabilityKey(n)
	if key == "" || counts[key] <= 1 {
		return ""
	}
	return fmt.Sprintf(" (collapsed %d duplicate routine reminders)", counts[key]-1)
}
