// Package cleanservice removes expired sessions, old operations and orphaned
// snapshots.
package cleanservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/zzjha-cn/codex-rewind/internal/config"
	"github.com/zzjha-cn/codex-rewind/internal/display"
	"github.com/zzjha-cn/codex-rewind/internal/gc"
	"github.com/zzjha-cn/codex-rewind/internal/snapshot"
	"github.com/zzjha-cn/codex-rewind/internal/store"
	"github.com/zzjha-cn/codex-rewind/internal/workspace"
)

// Options configures a clean run.
type Options struct {
	Root              string
	OlderThan         time.Duration
	DryRun            bool
	Yes               bool
	IncludeStaleOpen  bool
	IncludeIncomplete bool
	Now               func() time.Time
}

// SessionPlan is one session slated for deletion.
type SessionPlan struct {
	SessionID    string    `json:"sessionId"`
	LastActivity time.Time `json:"lastActivityAt"`
	Closed       bool      `json:"closed"`
	TurnCount    int       `json:"turnCount"`
	Bytes        int64     `json:"bytes"`
}

// OpPlan is one rewind operation slated for deletion.
type OpPlan struct {
	OpID      string    `json:"opId"`
	Phase     string    `json:"phase"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Plan is the full clean plan and, after execution, its report.
type Plan struct {
	DryRun       bool          `json:"dryRun"`
	Cutoff       time.Time     `json:"cutoff"`
	Sessions     []SessionPlan `json:"sessions"`
	Ops          []OpPlan      `json:"ops"`
	States       []string      `json:"states"`
	Objects      []string      `json:"objects"`
	Tmp          []string      `json:"tmp"`
	ReclaimBytes int64         `json:"reclaimBytes"`
	Warnings     []string      `json:"warnings,omitempty"`
	Executed     bool          `json:"executed"`
}

// ParseAge parses "30d", "48h", "2w" or plain days.
func ParseAge(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty age")
	}
	unit := s[len(s)-1]
	num := s[:len(s)-1]
	mult := 24 * time.Hour
	switch unit {
	case 'd':
	case 'w':
		mult = 7 * 24 * time.Hour
	case 'h':
		mult = time.Hour
	case 'm':
		mult = time.Minute
	default:
		num = s
	}
	f, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid age %q", s)
	}
	if f <= 0 {
		return 0, fmt.Errorf("age must be positive: %q", s)
	}
	return time.Duration(f * float64(mult)), nil
}

// Run builds a plan and, when explicitly confirmed, executes it.
func Run(ctx context.Context, opts Options) (*Plan, error) {
	_ = ctx
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	l := store.New(opts.Root)
	plan := &Plan{DryRun: opts.DryRun || !opts.Yes}
	if !l.Exists() {
		plan.DryRun = true
		return plan, nil
	}
	cfg, cfgErr := config.Load(l.ConfigPath())
	if cfgErr != nil {
		plan.Warnings = append(plan.Warnings, "config: "+cfgErr.Error())
	}
	age := opts.OlderThan
	if age <= 0 {
		age = time.Duration(cfg.Retention.CleanAfterDays) * 24 * time.Hour
	}
	plan.Cutoff = now().Add(-age)

	lock, err := store.TryAcquire(l.LockPath(), "cxr clean")
	if err != nil {
		if errors.Is(err, store.ErrLocked) {
			return nil, errors.New("repository lock is held; close Codex or wait for the running hook")
		}
		return nil, err
	}
	defer lock.Release()

	sessions, sessErrs := store.LoadSessions(l)
	for _, e := range sessErrs {
		plan.Warnings = append(plan.Warnings, e.Error())
	}
	ops, opErrs := store.ListRewindOps(l)
	for _, e := range opErrs {
		plan.Warnings = append(plan.Warnings, e.Error())
	}
	incompleteBySession := map[string]bool{}
	for _, op := range ops {
		if op.Phase.Incomplete() {
			incompleteBySession[op.SourceSessionID] = true
		}
	}
	exclude := map[string]struct{}{}
	for _, sess := range sessions {
		if sess.IsActive() {
			continue
		}
		if !sess.LastActivityAt.Before(plan.Cutoff) {
			continue
		}
		if sess.ClosedAt == nil && !opts.IncludeStaleOpen {
			continue
		}
		if incompleteBySession[sess.SessionID] && !opts.IncludeIncomplete {
			continue
		}
		q, qerr := store.LoadQueue(l, sess.SessionID, cfg.Retention.MaxRetainedTurns, display.Limits{})
		if qerr != nil {
			plan.Warnings = append(plan.Warnings, "skip session "+sess.SessionID+": "+qerr.Error())
			continue
		}
		running := false
		for _, t := range q.Turns {
			if t.Status == store.TurnRunning {
				running = true
				break
			}
		}
		if running {
			continue
		}
		dir, derr := l.SessionDir(sess.SessionID)
		if derr != nil {
			continue
		}
		size := dirSize(dir)
		plan.Sessions = append(plan.Sessions, SessionPlan{
			SessionID:    sess.SessionID,
			LastActivity: sess.LastActivityAt,
			Closed:       sess.ClosedAt != nil,
			TurnCount:    len(q.Turns),
			Bytes:        size,
		})
		plan.ReclaimBytes += size
		exclude[sess.SessionID] = struct{}{}
	}

	for _, op := range ops {
		if op.Phase.Incomplete() {
			if opts.IncludeIncomplete && op.UpdatedAt.Before(plan.Cutoff) {
				plan.Ops = append(plan.Ops, OpPlan{OpID: op.OpID, Phase: string(op.Phase), UpdatedAt: op.UpdatedAt})
				plan.ReclaimBytes += fileSize(opPath(l, op.OpID))
			}
			continue
		}
		if op.UpdatedAt.Before(plan.Cutoff) {
			plan.Ops = append(plan.Ops, OpPlan{OpID: op.OpID, Phase: string(op.Phase), UpdatedAt: op.UpdatedAt})
			plan.ReclaimBytes += fileSize(opPath(l, op.OpID))
		}
	}

	if entries, err := os.ReadDir(l.TmpDir()); err == nil {
		for _, e := range entries {
			plan.Tmp = append(plan.Tmp, e.Name())
			plan.ReclaimBytes += dirSize(filepath.Join(l.TmpDir(), e.Name()))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		plan.Warnings = append(plan.Warnings, err.Error())
	}

	gcRes, gcErr := gc.Collect(l, gc.Options{DryRun: true, ExcludeSessions: exclude})
	if gcErr != nil {
		plan.Warnings = append(plan.Warnings, "gc: "+gcErr.Error())
	} else {
		plan.States = gcRes.UnreachableStates
		plan.Objects = gcRes.UnreachableObjects
		for _, id := range plan.States {
			plan.ReclaimBytes += fileSize(statePath(l, id))
		}
		for _, oid := range plan.Objects {
			plan.ReclaimBytes += objectSize(l, oid)
		}
	}

	if plan.DryRun {
		return plan, nil
	}

	for _, sp := range plan.Sessions {
		dir, err := l.SessionDir(sp.SessionID)
		if err != nil {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			plan.Warnings = append(plan.Warnings, "delete session "+sp.SessionID+": "+err.Error())
		}
	}
	for _, op := range plan.Ops {
		if err := os.Remove(opPath(l, op.OpID)); err != nil && !errors.Is(err, os.ErrNotExist) {
			plan.Warnings = append(plan.Warnings, "delete op "+op.OpID+": "+err.Error())
		}
	}
	for _, name := range plan.Tmp {
		if err := os.RemoveAll(filepath.Join(l.TmpDir(), name)); err != nil {
			plan.Warnings = append(plan.Warnings, "delete tmp "+name+": "+err.Error())
		}
	}
	finalGC, err := gc.Collect(l, gc.Options{})
	if err != nil {
		return plan, err
	}
	plan.States = finalGC.DeletedStates
	plan.Objects = finalGC.DeletedObjects
	plan.ReclaimBytes += finalGC.BytesFreed
	plan.Executed = true
	for _, e := range finalGC.Errors {
		plan.Warnings = append(plan.Warnings, e.Error())
	}
	logClean(l, plan, now())
	return plan, nil
}

func opPath(l *store.Layout, opID string) string {
	path, err := store.RewindOpPath(l, opID)
	if err != nil {
		return ""
	}
	return path
}

func statePath(l *store.Layout, stateID string) string {
	path, err := snapshot.StatePath(l, stateID)
	if err != nil {
		return ""
	}
	return path
}

func objectSize(l *store.Layout, oid string) int64 {
	digest, err := workspace.ParseOID(oid)
	if err != nil {
		return 0
	}
	return fileSize(filepath.Join(l.ObjectsDir(), digest[:2], digest[2:]))
}

func fileSize(path string) int64 {
	if path == "" {
		return 0
	}
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return 0
	}
	return st.Size()
}

func dirSize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

func logClean(l *store.Layout, plan *Plan, now time.Time) {
	data, err := json.Marshal(struct {
		SchemaVersion int       `json:"schemaVersion"`
		At            time.Time `json:"at"`
		DryRun        bool      `json:"dryRun"`
		Sessions      []string  `json:"sessions"`
		Ops           []string  `json:"ops"`
		States        []string  `json:"states"`
		Objects       []string  `json:"objects"`
		Bytes         int64     `json:"bytes"`
	}{SchemaVersion: 1, At: now, DryRun: plan.DryRun, Sessions: sessionIDs(plan.Sessions), Ops: opIDs(plan.Ops),
		States: plan.States, Objects: plan.Objects, Bytes: plan.ReclaimBytes})
	if err != nil {
		return
	}
	data = append(data, '\n')
	if err := os.MkdirAll(l.LogsDir(), 0700); err != nil {
		return
	}
	path := filepath.Join(l.LogsDir(), "clean-"+now.Format("2006-01-02")+".jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(data)
}

func sessionIDs(in []SessionPlan) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, s.SessionID)
	}
	return out
}

func opIDs(in []OpPlan) []string {
	out := make([]string, 0, len(in))
	for _, o := range in {
		out = append(out, o.OpID)
	}
	return out
}
