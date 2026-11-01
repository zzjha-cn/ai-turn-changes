// Package rewindservice performs soft-fork rewind transactions.
package rewindservice

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/zzjha-cn/codex-rewind/internal/codex/appserver"
	"github.com/zzjha-cn/codex-rewind/internal/config"
	"github.com/zzjha-cn/codex-rewind/internal/display"
	"github.com/zzjha-cn/codex-rewind/internal/journal"
	"github.com/zzjha-cn/codex-rewind/internal/snapshot"
	"github.com/zzjha-cn/codex-rewind/internal/store"
)

// ForkRequest asks for one conversation fork.
type ForkRequest struct {
	ThreadID string
	TurnID   string
	Mode     string
	Cwd      string
}

// Forker abstracts app-server thread forking for tests.
type Forker interface {
	Fork(ctx context.Context, req ForkRequest) (newThreadID string, err error)
}

// RealForker starts a temporary app-server per fork.
type RealForker struct {
	Binary  string
	Env     []string
	Timeout time.Duration
}

func (f *RealForker) Fork(ctx context.Context, req ForkRequest) (string, error) {
	params := appserver.ThreadForkParams{ThreadID: req.ThreadID, Cwd: req.Cwd}
	switch req.Mode {
	case store.ModeBefore:
		params.BeforeTurnID = req.TurnID
	case store.ModeThrough:
		params.LastTurnID = req.TurnID
	default:
		return "", fmt.Errorf("unknown rewind mode %q", req.Mode)
	}
	return appserver.Fork(ctx, appserver.Options{Binary: f.Binary, Env: f.Env, Timeout: f.Timeout}, params)
}

// Options configures a rewind.
type Options struct {
	Root   string
	DryRun bool
	Force  bool
	Resume bool
	Now    func() time.Time
	Rand   func(n int) ([]byte, error)
	Forker Forker
}

// Request selects the target turn and mode.
type Request struct {
	SessionID string
	Seq       int64
	TurnID    string
	Last      bool
	Mode      string
}

// Result reports a rewind (or dry-run plan).
type Result struct {
	Op            *store.RewindOp
	Session       *store.Session
	Target        *store.QueueTurn
	Mode          string
	TargetStateID string
	NewThreadID   string
	SafetyStateID string
	Plan          *snapshot.Plan
	Applied       []string
	Verified      []snapshot.Conflict
	DryRun        bool
	Warnings      []string
}

// ConflictError is returned when manual changes would be overwritten.
type ConflictError struct {
	Conflicts []snapshot.Conflict
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("%d file conflict(s) detected", len(e.Conflicts))
}

type Service struct {
	opts Options
}

func New(opts Options) *Service {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Service{opts: opts}
}

// Rewind performs or plans one rewind.
func (s *Service) Rewind(ctx context.Context, req Request) (*Result, error) {
	l := store.New(s.opts.Root)
	if !l.Exists() {
		return nil, fmt.Errorf("no rewind data at %s (run cxr install and use Codex hooks first)", l.DataDir())
	}
	cfg, cfgErr := config.Load(l.ConfigPath())
	res := &Result{DryRun: s.opts.DryRun}
	if cfgErr != nil {
		res.Warnings = append(res.Warnings, "config: "+cfgErr.Error())
	}
	limits := display.Limits{MaxPromptBytes: cfg.Limits.MaxPromptBytes, MaxAssistantBytes: cfg.Limits.MaxAssistantBytes,
		MaxFiles: cfg.Limits.MaxDisplayFiles, MaxTextChars: cfg.Limits.MaxDisplayTextChars}

	lock, err := store.TryAcquire(l.LockPath(), "cxr rewind")
	if err != nil {
		if errors.Is(err, store.ErrLocked) {
			return nil, errors.New("repository lock is held by a running hook; close Codex or wait for the turn to finish")
		}
		return nil, err
	}
	defer lock.Release()

	sess, q, target, mode, err := s.selectTarget(l, cfg, limits, req)
	if err != nil {
		return nil, err
	}
	res.Session = sess
	res.Target = target
	res.Mode = mode

	live, stale, sessErrs := s.activeSessions(l, cfg, limits)
	if len(sessErrs) > 0 {
		msg := "cannot confirm active sessions: " + sessErrs[0].Error()
		if !s.opts.DryRun {
			return nil, errors.New(msg + "; run `cxr doctor` first")
		}
		res.Warnings = append(res.Warnings, msg)
	}
	if len(live) > 0 {
		msg := fmt.Sprintf("active turn detected in session(s) %v; finish or interrupt the turn first", live)
		if !s.opts.DryRun {
			// --force overrides conflicts and stale markers only. It must never
			// bypass a turn that is still running.
			return nil, errors.New(msg + " (--force does not override a running turn)")
		}
		res.Warnings = append(res.Warnings, msg)
	}
	if len(stale) > 0 {
		msg := fmt.Sprintf("session(s) %v report a stale active marker", stale)
		if s.opts.Force {
			res.Warnings = append(res.Warnings, msg+"; continuing because --force was given")
		} else if !s.opts.DryRun {
			return nil, errors.New(msg + "; inspect with `cxr doctor` or re-run with --force")
		} else {
			res.Warnings = append(res.Warnings, msg)
		}
	}
	for _, t := range q.Turns {
		if t.Status == store.TurnRunning {
			msg := fmt.Sprintf("session %s still has a running turn %s; finish or interrupt it first", sess.SessionID, t.TurnID)
			if !s.opts.DryRun {
				return nil, errors.New(msg)
			}
			res.Warnings = append(res.Warnings, msg)
			break
		}
	}

	ops, oerrs := store.ListRewindOps(l)
	if len(oerrs) > 0 {
		res.Warnings = append(res.Warnings, oerrs[0].Error())
	}
	var op *store.RewindOp
	for _, candidate := range ops {
		if !candidate.Phase.Incomplete() {
			continue
		}
		if candidate.SourceSessionID == sess.SessionID && candidate.TargetTurnID == target.TurnID && candidate.Mode == mode {
			if op == nil || candidate.UpdatedAt.After(op.UpdatedAt) {
				op = candidate
			}
			continue
		}
		if !s.opts.Resume {
			return nil, fmt.Errorf("unfinished rewind op %s is in phase %s; finish it with cxr rewind --resume or inspect cxr doctor", candidate.OpID, candidate.Phase)
		}
		return nil, fmt.Errorf("unfinished rewind op %s targets another turn; finish it first", candidate.OpID)
	}
	if s.opts.Resume && op == nil {
		return nil, errors.New("--resume requested but no matching incomplete rewind op was found")
	}
	if op != nil && op.Phase == journal.FailedTerminal {
		return nil, fmt.Errorf("rewind op %s failed terminally: %s", op.OpID, errText(op.Error))
	}

	targetStateID, err := stateIDForMode(target, mode)
	if err != nil {
		return nil, err
	}
	res.TargetStateID = targetStateID
	if op != nil && op.TargetStateID != "" && op.TargetStateID != targetStateID {
		return nil, fmt.Errorf("rewind op %s target state %s does not match %s", op.OpID, op.TargetStateID, targetStateID)
	}
	targetState, err := snapshot.LoadState(l, targetStateID)
	if err != nil {
		return nil, fmt.Errorf("load target state: %w", err)
	}
	cas := snapshot.NewCAS(l)
	scope, latest, err := s.scopeAndLatest(l, q, target, mode)
	if err != nil {
		return nil, err
	}

	captureOpts := snapshot.CaptureOptions{Previous: s.latestFinalState(l, q), Now: s.now, SkipObjects: true, NoPersist: true}
	current, err := snapshot.Capture(l, cas, captureOpts)
	if err != nil {
		return nil, fmt.Errorf("capture current workspace: %w", err)
	}
	plan := snapshot.BuildPlan(targetState, current, scope, latest)
	res.Plan = plan
	if plan.HasConflicts() {
		mapped := mapConflicts(plan.Conflicts)
		if op != nil {
			op.Conflicts = mapped
			op.UpdatedAt = s.now()
			_ = store.SaveRewindOp(l, op)
		}
		if !s.opts.Force && !s.opts.DryRun {
			return res, &ConflictError{Conflicts: plan.Conflicts}
		}
		if s.opts.Force {
			res.Warnings = append(res.Warnings, fmt.Sprintf("--force: overwriting %d conflicted path(s)", len(plan.Conflicts)))
		} else {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%d conflict(s) would block this rewind; use --force to overwrite", len(plan.Conflicts)))
		}
	}

	writeOIDs := make([]string, 0, len(plan.Actions))
	for _, a := range plan.Actions {
		if a.Kind == snapshot.ActionWrite && a.Expected != nil && !a.Expected.IsSymlink && a.Expected.OID != "" {
			writeOIDs = append(writeOIDs, a.Expected.OID)
		}
	}
	if err := cas.VerifyAll(writeOIDs); err != nil {
		if op != nil {
			op.Phase = journal.FailedTerminal
			op.SetError("object verification failed: "+err.Error(), s.now())
			_ = store.SaveRewindOp(l, op)
		}
		return res, fmt.Errorf("refusing partial restore: %w", err)
	}
	if s.opts.DryRun {
		return res, nil
	}

	if op == nil {
		op = store.NewRewindOp(s.newOpID(), sess.SessionID, threadID(sess), target.TurnID, mode, targetStateID, s.now())
		op.Conflicts = mapConflicts(plan.Conflicts)
		if err := store.SaveRewindOp(l, op); err != nil {
			return res, err
		}
	}
	res.Op = op
	var safety *snapshot.State
	if op.SafetyStateID != nil && *op.SafetyStateID != "" {
		if st, err := snapshot.LoadState(l, *op.SafetyStateID); err == nil {
			safety = st
			res.SafetyStateID = st.StateID
		} else {
			res.Warnings = append(res.Warnings, "safety state unreadable: "+err.Error())
		}
	}
	if safety == nil {
		captured, cerr := snapshot.Capture(l, cas, snapshot.CaptureOptions{Previous: s.latestFinalState(l, q), Now: s.now})
		if cerr != nil {
			return res, fmt.Errorf("capture safety state: %w", cerr)
		}
		safety = captured
		id := safety.StateID
		op.SafetyStateID = &id
		res.SafetyStateID = id
		if op.Phase == journal.Planned {
			op.SetPhase(journal.SafetyCaptured, s.now())
		}
		if err := store.SaveRewindOp(l, op); err != nil {
			return res, err
		}
	} else if op.SafetyStateID == nil {
		id := safety.StateID
		op.SafetyStateID = &id
		if op.Phase == journal.Planned {
			op.SetPhase(journal.SafetyCaptured, s.now())
		}
		if err := store.SaveRewindOp(l, op); err != nil {
			return res, err
		}
	}

	if op.NewThreadID == nil || *op.NewThreadID == "" {
		forker := s.opts.Forker
		if forker == nil {
			forker = &RealForker{}
		}
		newID, ferr := forker.Fork(ctx, ForkRequest{ThreadID: threadID(sess), TurnID: target.TurnID, Mode: mode, Cwd: l.Root})
		if ferr != nil {
			op.Phase = journal.FailedRetryable
			op.SetError("thread/fork failed: "+ferr.Error(), s.now())
			_ = store.SaveRewindOp(l, op)
			return res, fmt.Errorf("thread/fork failed before any file was changed (safe to retry with --resume): %w", ferr)
		}
		op.NewThreadID = &newID
		res.NewThreadID = newID
		op.SetPhase(journal.ThreadForked, s.now())
		if err := store.SaveRewindOp(l, op); err != nil {
			return res, err
		}
	} else {
		res.NewThreadID = *op.NewThreadID
		res.Warnings = append(res.Warnings, "reusing already forked thread "+res.NewThreadID)
	}
	if op.Phase == journal.Verified || op.Phase == journal.Completed {
		if op.Phase == journal.Verified {
			op.SetPhase(journal.Completed, s.now())
			op.SetError("", s.now())
			_ = store.SaveRewindOp(l, op)
		}
		return res, nil
	}

	op.SetPhase(journal.FilesRestoring, s.now())
	op.SetError("", s.now())
	if err := store.SaveRewindOp(l, op); err != nil {
		return res, err
	}
	applied, aerr := snapshot.Apply(l.Root, cas, plan)
	res.Applied = applied
	if aerr != nil {
		phase := journal.FailedRetryable
		msg := "restore failed: " + aerr.Error()
		if safety != nil {
			if _, rerr := snapshot.RestorePaths(l.Root, cas, safety, plan.Paths()); rerr != nil {
				phase = journal.FailedTerminal
				msg += "; rollback failed: " + rerr.Error()
			} else {
				msg += "; rolled back to safety state " + safety.StateID
			}
		}
		op.Phase = phase
		op.SetError(msg, s.now())
		_ = store.SaveRewindOp(l, op)
		return res, errors.New(msg)
	}
	conflicts, verr := snapshot.VerifyPaths(l, cas, targetState, plan.Paths())
	res.Verified = conflicts
	if verr != nil || len(conflicts) > 0 {
		phase := journal.FailedRetryable
		msg := "verification failed"
		if verr != nil {
			msg += ": " + verr.Error()
		} else {
			msg += fmt.Sprintf(": %d path(s) do not match target state", len(conflicts))
		}
		if safety != nil {
			if _, rerr := snapshot.RestorePaths(l.Root, cas, safety, plan.Paths()); rerr != nil {
				phase = journal.FailedTerminal
				msg += "; rollback failed: " + rerr.Error()
			} else {
				msg += "; rolled back to safety state " + safety.StateID
			}
		}
		op.Phase = phase
		op.SetError(msg, s.now())
		_ = store.SaveRewindOp(l, op)
		return res, errors.New(msg)
	}
	op.SetPhase(journal.Verified, s.now())
	if err := store.SaveRewindOp(l, op); err != nil {
		return res, err
	}
	op.SetPhase(journal.Completed, s.now())
	op.SetError("", s.now())
	if err := store.SaveRewindOp(l, op); err != nil {
		return res, err
	}
	return res, nil
}

func (s *Service) selectTarget(l *store.Layout, cfg config.Config, limits display.Limits, req Request) (*store.Session, *store.Queue, *store.QueueTurn, string, error) {
	var sess *store.Session
	var err error
	if req.SessionID != "" {
		sess, err = store.LoadSession(l, req.SessionID)
		if err != nil {
			return nil, nil, nil, "", err
		}
	} else {
		sess, err = store.MostRecent(l)
		if err != nil {
			return nil, nil, nil, "", fmt.Errorf("select session: %w", err)
		}
	}
	q, err := store.LoadQueue(l, sess.SessionID, cfg.Retention.MaxRetainedTurns, limits)
	if err != nil {
		return nil, nil, nil, "", err
	}
	if req.Last {
		if req.Seq != 0 || req.TurnID != "" {
			return nil, nil, nil, "", errors.New("--last cannot be combined with --seq or --turn")
		}
		target := q.LastTerminal()
		if target == nil {
			return nil, nil, nil, "", errors.New("no completed turn to rewind")
		}
		return sess, q, target, store.ModeBefore, nil
	}
	mode := req.Mode
	if mode == "" {
		mode = store.ModeBefore
	}
	if mode != store.ModeBefore && mode != store.ModeThrough {
		return nil, nil, nil, "", fmt.Errorf("invalid mode %q", mode)
	}
	var target *store.QueueTurn
	switch {
	case req.TurnID != "" && req.Seq != 0:
		return nil, nil, nil, "", errors.New("use either --turn or --seq, not both")
	case req.TurnID != "":
		for i := range q.Turns {
			if q.Turns[i].TurnID == req.TurnID {
				target = &q.Turns[i]
				break
			}
		}
		if target == nil {
			return nil, nil, nil, "", fmt.Errorf("turn %s is not in the retained window for session %s", req.TurnID, sess.SessionID)
		}
	case req.Seq != 0:
		target = q.TurnBySeq(req.Seq)
		if target == nil {
			return nil, nil, nil, "", fmt.Errorf("seq %d is not in the retained window for session %s", req.Seq, sess.SessionID)
		}
	default:
		return nil, nil, nil, "", errors.New("specify --last, --seq or --turn")
	}
	if target.Status == store.TurnRunning {
		return nil, nil, nil, "", fmt.Errorf("turn %s is still running", target.TurnID)
	}
	return sess, q, target, mode, nil
}

func stateIDForMode(t *store.QueueTurn, mode string) (string, error) {
	if t == nil {
		return "", errors.New("nil target turn")
	}
	if mode == store.ModeThrough {
		if t.FinalStateID == "" {
			return "", fmt.Errorf("turn %s has no final state (interrupted); --through is unavailable, use --before", t.TurnID)
		}
		return t.FinalStateID, nil
	}
	if t.BaselineStateID == "" {
		return "", fmt.Errorf("turn %s has no baseline state (capture failed); --before is unavailable", t.TurnID)
	}
	return t.BaselineStateID, nil
}

func (s *Service) scopeAndLatest(l *store.Layout, q *store.Queue, target *store.QueueTurn, mode string) ([]string, map[string]*snapshot.FileEntry, error) {
	latest := make(map[string]*snapshot.FileEntry)
	scopeSet := make(map[string]struct{})
	// Item changes are authoritative; Display.Files is capped.
	for i := range q.Turns {
		t := &q.Turns[i]
		discard := t.Seq > target.Seq
		if mode == store.ModeBefore {
			discard = t.Seq >= target.Seq
		}
		if !discard {
			continue
		}
		var changes []store.FileChange
		it, err := store.LoadTurnItem(l, q.SessionID, t.Seq, t.TurnID)
		if err == nil {
			changes = it.Changes
			for _, c := range changes {
				scopeSet[c.Path] = struct{}{}
			}
		}
		if t.FinalStateID == "" {
			continue
		}
		st, err := snapshot.LoadState(l, t.FinalStateID)
		if err != nil {
			continue
		}
		inState := make(map[string]bool, len(st.Files))
		for j := range st.Files {
			f := &st.Files[j]
			inState[f.Path] = true
			if _, ok := scopeSet[f.Path]; ok {
				latest[f.Path] = f
			}
		}
		// An untracked file deleted by this turn disappears from the scan, so
		// the final state has no entry. Record an explicit tombstone so the
		// restore planner does not mistake the absence for a manual edit.
		for _, c := range changes {
			if c.Status == "deleted" && !inState[c.Path] {
				latest[c.Path] = &snapshot.FileEntry{Path: c.Path, Exists: false}
			}
		}
	}
	scope := make([]string, 0, len(scopeSet))
	for p := range scopeSet {
		scope = append(scope, p)
	}
	return scope, latest, nil
}

func (s *Service) latestFinalState(l *store.Layout, q *store.Queue) *snapshot.State {
	for i := len(q.Turns) - 1; i >= 0; i-- {
		if q.Turns[i].FinalStateID == "" {
			continue
		}
		if st, err := snapshot.LoadState(l, q.Turns[i].FinalStateID); err == nil {
			return st
		}
	}
	return nil
}

// activeSessions splits sessions with an active marker into those whose marker
// is backed by a running turn (live) and those that are provably stale. A live
// turn can never be bypassed; --force only overrides a stale marker.
func (s *Service) activeSessions(l *store.Layout, cfg config.Config, limits display.Limits) (live, stale []string, errs []error) {
	sessions, errs := store.LoadSessions(l)
	for _, sess := range sessions {
		if !sess.IsActive() {
			continue
		}
		q, err := store.LoadQueueReadOnly(l, sess.SessionID, cfg.Retention.MaxRetainedTurns, limits)
		if err != nil {
			live = append(live, sess.SessionID)
			continue
		}
		t := q.Turn(sess.ActiveTurn())
		if t != nil && t.Status == store.TurnRunning {
			live = append(live, sess.SessionID)
			continue
		}
		stale = append(stale, sess.SessionID)
	}
	return live, stale, errs
}

func (s *Service) now() time.Time {
	if s.opts.Now != nil {
		return s.opts.Now()
	}
	return time.Now()
}

func (s *Service) newOpID() string {
	buf := make([]byte, 4)
	if s.opts.Rand != nil {
		if b, err := s.opts.Rand(4); err == nil && len(b) == 4 {
			buf = b
		}
	} else if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("rewind-%d", s.now().UnixNano())
	}
	return fmt.Sprintf("rewind-%d-%s", s.now().UnixNano(), hex.EncodeToString(buf))
}

func threadID(sess *store.Session) string {
	if sess.ThreadID != "" {
		return sess.ThreadID
	}
	return sess.SessionID
}

func mapConflicts(in []snapshot.Conflict) []store.RewindConflict {
	out := make([]store.RewindConflict, 0, len(in))
	for _, c := range in {
		out = append(out, store.RewindConflict{Path: c.Path, Reason: c.Reason})
	}
	return out
}

func errText(s *string) string {
	if s == nil || *s == "" {
		return "(no error recorded)"
	}
	return *s
}
