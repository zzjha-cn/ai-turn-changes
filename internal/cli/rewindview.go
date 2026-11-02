package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/zzjha-cn/codex-rewind/internal/config"
	"github.com/zzjha-cn/codex-rewind/internal/display"
	"github.com/zzjha-cn/codex-rewind/internal/journal"
	"github.com/zzjha-cn/codex-rewind/internal/store"
)

// rewindView is a completed rewind rendered for `cxr list` and `cxr status`.
// Only completed operations drive branch markers; unfinished ones stay visible
// through `cxr doctor`.
type rewindView struct {
	OpID            string    `json:"opId"`
	SourceSessionID string    `json:"sourceSessionId"`
	SourceThreadID  string    `json:"sourceThreadId"`
	TargetTurnID    string    `json:"targetTurnId"`
	TargetSeq       int64     `json:"targetSeq,omitempty"`
	TargetInWindow  bool      `json:"targetInWindow"`
	Mode            string    `json:"mode"`
	NewThreadID     string    `json:"newThreadId,omitempty"`
	NewThreadName   string    `json:"newThreadName,omitempty"`
	Phase           string    `json:"phase"`
	CompletedAt     time.Time `json:"completedAt"`
	PendingResume   bool      `json:"pendingResume,omitempty"`
}

// forkedFromView describes the source side of a fork branch.
type forkedFromView struct {
	OpID            string    `json:"opId"`
	SourceSessionID string    `json:"sourceSessionId"`
	SourceThreadID  string    `json:"sourceThreadId"`
	TargetTurnID    string    `json:"targetTurnId"`
	TargetSeq       int64     `json:"targetSeq,omitempty"`
	TargetInWindow  bool      `json:"targetInWindow"`
	Mode            string    `json:"mode"`
	CompletedAt     time.Time `json:"completedAt"`
}

// rewindIndex indexes completed rewind operations for list/status rendering.
// It never changes stored session data; the original session keeps its full
// retained window.
type rewindIndex struct {
	completed    []*store.RewindOp
	bySource     map[string]*store.RewindOp
	byNewThread  map[string]*store.RewindOp
	sessionIDs   map[string]bool
	threadIDs    map[string]bool
	queues       map[string]*store.Queue
	loadedQueues map[string]bool
}

func buildRewindIndex(l *store.Layout, sessions []*store.Session) *rewindIndex {
	idx := &rewindIndex{
		bySource:     map[string]*store.RewindOp{},
		byNewThread:  map[string]*store.RewindOp{},
		sessionIDs:   map[string]bool{},
		threadIDs:    map[string]bool{},
		queues:       map[string]*store.Queue{},
		loadedQueues: map[string]bool{},
	}
	for _, s := range sessions {
		idx.sessionIDs[s.SessionID] = true
		if s.ThreadID != "" {
			idx.threadIDs[s.ThreadID] = true
		}
	}
	ops, _ := store.ListRewindOps(l)
	for _, op := range ops {
		if op.Phase != journal.Completed {
			continue
		}
		idx.completed = append(idx.completed, op)
		if _, ok := idx.bySource[op.SourceSessionID]; !ok {
			idx.bySource[op.SourceSessionID] = op
		}
		if op.NewThreadID != nil && *op.NewThreadID != "" {
			if _, ok := idx.byNewThread[*op.NewThreadID]; !ok {
				idx.byNewThread[*op.NewThreadID] = op
			}
		}
	}
	return idx
}

func (idx *rewindIndex) latest() *store.RewindOp {
	if len(idx.completed) == 0 {
		return nil
	}
	return idx.completed[0]
}

// sourceFor returns the newest completed rewind whose source is s.
func (idx *rewindIndex) sourceFor(s *store.Session) *store.RewindOp {
	if s == nil {
		return nil
	}
	if op := idx.bySource[s.SessionID]; op != nil {
		return op
	}
	if s.ThreadID == "" {
		return nil
	}
	for _, op := range idx.completed {
		if op.SourceThreadID != "" && op.SourceThreadID == s.ThreadID {
			return op
		}
	}
	return nil
}

// forkFor returns the completed rewind that produced s, if any.
func (idx *rewindIndex) forkFor(s *store.Session) *store.RewindOp {
	if s == nil {
		return nil
	}
	if op := idx.byNewThread[s.SessionID]; op != nil {
		return op
	}
	if s.ThreadID != "" {
		return idx.byNewThread[s.ThreadID]
	}
	return nil
}

func (idx *rewindIndex) hasSessionForThread(id string) bool {
	if id == "" {
		return false
	}
	return idx.sessionIDs[id] || idx.threadIDs[id]
}

func (idx *rewindIndex) rememberQueue(sessionID string, q *store.Queue) {
	idx.queues[sessionID] = q
	idx.loadedQueues[sessionID] = true
}

// targetSeq resolves the target turn's sequence number from the source queue.
// The target can have fallen out of the retained window, in which case only the
// turn id is known.
func (idx *rewindIndex) targetSeq(l *store.Layout, cfg config.Config, limits display.Limits, op *store.RewindOp) (int64, bool) {
	if op == nil {
		return 0, false
	}
	if !idx.loadedQueues[op.SourceSessionID] {
		q, err := store.LoadQueueReadOnly(l, op.SourceSessionID, cfg.Retention.MaxRetainedTurns, limits)
		if err == nil {
			idx.queues[op.SourceSessionID] = q
		}
		idx.loadedQueues[op.SourceSessionID] = true
	}
	q := idx.queues[op.SourceSessionID]
	if q == nil {
		return 0, false
	}
	if t := q.Turn(op.TargetTurnID); t != nil {
		return t.Seq, true
	}
	return 0, false
}

func (idx *rewindIndex) sourceView(l *store.Layout, cfg config.Config, limits display.Limits, op *store.RewindOp) *rewindView {
	if op == nil {
		return nil
	}
	v := &rewindView{
		OpID:            op.OpID,
		SourceSessionID: op.SourceSessionID,
		SourceThreadID:  op.SourceThreadID,
		TargetTurnID:    op.TargetTurnID,
		Mode:            op.Mode,
		NewThreadName:   op.NewThreadName,
		Phase:           string(op.Phase),
		CompletedAt:     op.UpdatedAt,
	}
	if op.NewThreadID != nil {
		v.NewThreadID = *op.NewThreadID
	}
	if seq, ok := idx.targetSeq(l, cfg, limits, op); ok {
		v.TargetSeq = seq
		v.TargetInWindow = true
	}
	if v.NewThreadID != "" {
		v.PendingResume = !idx.hasSessionForThread(v.NewThreadID)
	}
	return v
}

func (idx *rewindIndex) forkedFromView(l *store.Layout, cfg config.Config, limits display.Limits, op *store.RewindOp) *forkedFromView {
	if op == nil {
		return nil
	}
	v := &forkedFromView{
		OpID:            op.OpID,
		SourceSessionID: op.SourceSessionID,
		SourceThreadID:  op.SourceThreadID,
		TargetTurnID:    op.TargetTurnID,
		Mode:            op.Mode,
		CompletedAt:     op.UpdatedAt,
	}
	if seq, ok := idx.targetSeq(l, cfg, limits, op); ok {
		v.TargetSeq = seq
		v.TargetInWindow = true
	}
	return v
}

// branchTip follows completed rewinds from start to the newest branch. When the
// fork has not been resumed yet, the source is returned so its pending marker is
// visible.
func (idx *rewindIndex) branchTip(sessions []*store.Session, start *store.Session) *store.Session {
	seen := map[string]bool{}
	cur := start
	for cur != nil {
		if seen[cur.SessionID] {
			return cur
		}
		seen[cur.SessionID] = true
		op := idx.sourceFor(cur)
		if op == nil || op.NewThreadID == nil || *op.NewThreadID == "" {
			return cur
		}
		next := findSessionByThread(sessions, *op.NewThreadID)
		if next == nil {
			return cur
		}
		cur = next
	}
	return start
}

func findSessionByThread(sessions []*store.Session, id string) *store.Session {
	if id == "" {
		return nil
	}
	for _, s := range sessions {
		if s.SessionID == id || s.ThreadID == id {
			return s
		}
	}
	return nil
}

// pendingForkSession is a synthetic view of a fork that exists in Codex but has
// not yet produced a `.codex-rewind` session directory (no resume yet).
func pendingForkSession(source *store.Session, op *store.RewindOp) *store.Session {
	if op == nil || op.NewThreadID == nil || *op.NewThreadID == "" {
		return nil
	}
	title := "(untitled)"
	if source != nil && source.Title != "" {
		title = source.Title
	}
	id := *op.NewThreadID
	s := &store.Session{
		SchemaVersion:  store.SessionSchemaVersion,
		SessionID:      id,
		ThreadID:       id,
		Source:         "rewind",
		OpenedAt:       op.UpdatedAt,
		LastActivityAt: op.UpdatedAt,
		Title:          title + " (rewind fork)",
		Open:           false,
	}
	if source != nil {
		s.Cwd = source.Cwd
		s.GitRoot = source.GitRoot
	}
	return s
}

// addPendingForks appends synthetic fork entries next to their sources so
// `list --all` shows both sides of a rewind even before `codex resume`.
func (idx *rewindIndex) addPendingForks(l *store.Layout, entries []sessionJSON, cfg config.Config, limits display.Limits) []sessionJSON {
	seen := map[string]bool{}
	for _, e := range entries {
		if e.Session == nil {
			continue
		}
		seen[e.Session.SessionID] = true
		if e.Session.ThreadID != "" {
			seen[e.Session.ThreadID] = true
		}
	}
	out := make([]sessionJSON, 0, len(entries)*2)
	for _, e := range entries {
		out = append(out, e)
		if e.Session == nil || e.Rewind == nil || !e.Rewind.PendingResume {
			continue
		}
		op := idx.sourceFor(e.Session)
		if op == nil || op.NewThreadID == nil || *op.NewThreadID == "" {
			continue
		}
		id := *op.NewThreadID
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, sessionJSON{
			Session:       pendingForkSession(e.Session, op),
			Queue:         store.NewQueue(id, cfg.Retention.MaxRetainedTurns),
			ForkedFrom:    idx.forkedFromView(l, cfg, limits, op),
			PendingResume: true,
		})
	}
	return out
}

// printRewindMarkers writes the branch lineage lines under a session header.
func printRewindMarkers(out io.Writer, rewind *rewindView, forkedFrom *forkedFromView) {
	if forkedFrom != nil {
		fmt.Fprintf(out, "  forked from %s", shortSession(forkedFrom.SourceSessionID))
		if forkedFrom.TargetInWindow {
			fmt.Fprintf(out, " at [%d]", forkedFrom.TargetSeq)
		} else if forkedFrom.TargetTurnID != "" {
			fmt.Fprintf(out, " at turn %s", shortSession(forkedFrom.TargetTurnID))
		}
		fmt.Fprintf(out, " (%s)\n", forkedFrom.Mode)
	}
	if rewind != nil {
		fmt.Fprint(out, "  rewind at ")
		if rewind.TargetInWindow {
			fmt.Fprintf(out, "[%d]", rewind.TargetSeq)
		} else if rewind.TargetTurnID != "" {
			fmt.Fprintf(out, "turn %s", shortSession(rewind.TargetTurnID))
		} else {
			fmt.Fprint(out, "?")
		}
		fmt.Fprintf(out, " -> new thread %s (%s)\n", shortSession(rewind.NewThreadID), rewind.Mode)
		if rewind.PendingResume {
			fmt.Fprintf(out, "  pending resume: codex resume %s\n", rewind.NewThreadID)
		}
	}
}

func rewindRole(rewind *rewindView, forkedFrom *forkedFromView) string {
	switch {
	case rewind != nil && forkedFrom != nil:
		return "rewind fork/source"
	case rewind != nil:
		return "rewind source"
	case forkedFrom != nil:
		return "rewind fork"
	}
	return ""
}

// rewindTurnNote explains how the target turn is treated by the fork.
func rewindTurnNote(rewind *rewindView) string {
	if rewind == nil {
		return ""
	}
	fork := shortSession(rewind.NewThreadID)
	switch rewind.Mode {
	case store.ModeThrough:
		return "included in new thread " + fork
	default:
		return "excluded from new thread " + fork
	}
}

func formatRewindSummary(v *rewindView) string {
	if v == nil {
		return ""
	}
	target := "?"
	if v.TargetInWindow {
		target = fmt.Sprintf("[%d]", v.TargetSeq)
	} else if v.TargetTurnID != "" {
		target = shortSession(v.TargetTurnID)
	}
	fork := shortSession(v.NewThreadID)
	if fork == "" {
		fork = "?"
	}
	s := fmt.Sprintf("%s at %s (%s) -> %s", shortSession(v.SourceSessionID), target, v.Mode, fork)
	if v.PendingResume {
		s += " (pending resume)"
	}
	if !v.CompletedAt.IsZero() {
		s += "  " + v.CompletedAt.Local().Format("2006-01-02 15:04")
	}
	return s
}
