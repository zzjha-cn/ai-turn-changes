package hookservice

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/zzjha-cn/codex-rewind/internal/config"
	"github.com/zzjha-cn/codex-rewind/internal/display"
	"github.com/zzjha-cn/codex-rewind/internal/gc"
	"github.com/zzjha-cn/codex-rewind/internal/snapshot"
	"github.com/zzjha-cn/codex-rewind/internal/store"
	"github.com/zzjha-cn/codex-rewind/internal/workspace"
)

// Options configures a hook service.
type Options struct {
	RootOverride string
	Now          func() time.Time
	Rand         func(n int) ([]byte, error)
}

// Service processes hook events.
type Service struct {
	opts Options
}

// Result describes what one hook invocation did.
type Result struct {
	Root      string
	SessionID string
	TurnID    string
	Event     string
	Locked    bool
	Strict    bool
	Removed   []string
	Warnings  []string
}

func New(opts Options) *Service { return &Service{opts: opts} }

// Handle processes one hook payload while holding the repository lock.
func (s *Service) Handle(ctx context.Context, p Payload) (res *Result, retErr error) {
	_ = ctx
	if !p.Supported() {
		return nil, fmt.Errorf("unsupported hook_event_name %q", p.HookEventName)
	}
	if p.SessionID == "" {
		return nil, errors.New("hook payload missing session_id")
	}
	if err := store.ValidateID(p.SessionID); err != nil {
		return nil, fmt.Errorf("session_id: %w", err)
	}
	cwd := p.Cwd
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}
	root, err := workspace.Resolve(cwd, s.opts.RootOverride)
	if err != nil {
		return nil, err
	}
	l := store.New(root)
	if err := l.Ensure(); err != nil {
		return nil, err
	}
	cfg, cfgErr := config.Ensure(l.ConfigPath())
	res = &Result{Root: root, SessionID: p.SessionID, TurnID: p.TurnID, Event: p.HookEventName, Strict: cfg.StrictHook}
	if cfgErr != nil {
		res.Warnings = append(res.Warnings, "config: "+cfgErr.Error())
	}
	defer func() {
		s.logHook(l, res, retErr)
	}()

	lock, lerr := store.TryAcquire(l.LockPath(), "cxr hook "+p.HookEventName)
	if lerr != nil {
		if errors.Is(lerr, store.ErrLocked) {
			res.Locked = true
			return res, errors.New("repository lock is held by another process")
		}
		return res, lerr
	}
	defer lock.Release()

	sess, serr := s.ensureSession(l, cfg, p, cwd, root)
	if serr != nil {
		return res, serr
	}
	res.SessionID = sess.SessionID

	if p.HookEventName != EventSessionStart {
		if _, ferr := s.lazyFinalize(l, cfg, sess, p.TurnID); ferr != nil {
			res.Warnings = append(res.Warnings, "lazy finalize: "+ferr.Error())
		}
	}

	switch p.HookEventName {
	case EventSessionStart:
		return res, nil
	case EventUserPromptSubmit:
		return res, s.handleUserPrompt(l, cfg, sess, p)
	case EventPreToolUse, EventPostToolUse:
		return res, s.handleToolEvent(l, cfg, sess, p)
	case EventStop:
		return res, s.handleStop(l, cfg, sess, p, res)
	case EventInterrupt:
		return res, s.handleInterrupt(l, cfg, sess, p, res)
	case EventSessionEnd:
		return res, s.handleSessionEnd(l, cfg, sess, res)
	}
	return res, nil
}

func (s *Service) ensureSession(l *store.Layout, cfg config.Config, p Payload, cwd, root string) (*store.Session, error) {
	sess, err := store.LoadSession(l, p.SessionID)
	created := false
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		sess = store.NewSession(p.SessionID, p.ThreadID, p.Source, cwd, root, s.now())
		created = true
	}
	if p.ThreadID != "" && sess.ThreadID == "" {
		sess.ThreadID = p.ThreadID
	}
	if sess.ThreadID == "" {
		sess.ThreadID = sess.SessionID
	}
	if p.Source != "" {
		sess.Source = p.Source
	}
	sess.Cwd = cwd
	sess.GitRoot = root
	sess.Open = true
	sess.ClosedAt = nil
	sess.Touch(s.now())
	if sess.Title == "" {
		if prompt := p.PromptText(); prompt != "" {
			sess.Title = display.Title(prompt)
		}
	}
	if err := store.SaveSession(l, sess); err != nil {
		return nil, err
	}
	_ = created
	_ = cfg
	return sess, nil
}

func (s *Service) handleUserPrompt(l *store.Layout, cfg config.Config, sess *store.Session, p Payload) error {
	limits := displayLimits(cfg)
	q, err := store.LoadQueue(l, sess.SessionID, cfg.Retention.MaxRetainedTurns, limits)
	if err != nil {
		return err
	}
	if q.Turn(p.TurnID) != nil {
		return nil
	}
	seq := q.NextSeq
	if seq < 1 {
		seq = 1
	}
	turnID := p.TurnID
	if turnID == "" {
		turnID = s.newTurnID(seq)
	}
	if err := store.ValidateID(turnID); err != nil {
		return fmt.Errorf("turn_id: %w", err)
	}
	if q.Turn(turnID) != nil {
		return nil
	}
	started := s.now()
	prompt := display.TruncateUTF8(display.StripControl(p.PromptText()), cfg.Limits.MaxPromptBytes)
	var parent *string
	if prev := q.LastTerminal(); prev != nil {
		id := prev.TurnID
		parent = &id
	}
	cas := snapshot.NewCAS(l)
	prevState := s.latestFinalState(l, q)
	baseline, berr := snapshot.Capture(l, cas, snapshot.CaptureOptions{Previous: prevState, Now: s.now, Rand: nil})
	if berr != nil {
		em := "baseline capture failed: " + berr.Error()
		it := &store.TurnItem{
			SchemaVersion: store.TurnSchemaVersion,
			TurnID:        turnID,
			Seq:           seq,
			ParentTurnID:  parent,
			Status:        store.TurnFailed,
			Prompt:        prompt,
			StartedAt:     started,
			Changes:       []store.FileChange{},
			Error:         &em,
			NeedsFinalize: false,
		}
		if err := store.SaveTurnItem(l, sess.SessionID, it); err != nil {
			return err
		}
		q.Upsert(it, limits)
		if err := store.SaveQueue(l, q); err != nil {
			return err
		}
		sess.Touch(s.now())
		if err := store.SaveSession(l, sess); err != nil {
			return err
		}
		return berr
	}
	it := &store.TurnItem{
		SchemaVersion:   store.TurnSchemaVersion,
		TurnID:          turnID,
		Seq:             seq,
		ParentTurnID:    parent,
		Status:          store.TurnRunning,
		Prompt:          prompt,
		StartedAt:       started,
		BaselineStateID: baseline.StateID,
		Changes:         []store.FileChange{},
	}
	if err := store.SaveTurnItem(l, sess.SessionID, it); err != nil {
		return err
	}
	q.Upsert(it, limits)
	if err := store.SaveQueue(l, q); err != nil {
		return err
	}
	sess.SetActive(turnID)
	if sess.Title == "" {
		sess.Title = display.Title(prompt)
	}
	sess.Touch(s.now())
	return store.SaveSession(l, sess)
}

func (s *Service) handleToolEvent(l *store.Layout, cfg config.Config, sess *store.Session, p Payload) error {
	if p.HookEventName == EventPreToolUse && !cfg.Hooks.RecordPreToolUse {
		return nil
	}
	if p.HookEventName == EventPostToolUse && !cfg.Hooks.RecordPostToolUse {
		return nil
	}
	turnID := p.TurnID
	if turnID == "" {
		turnID = sess.ActiveTurn()
	}
	if turnID == "" {
		return nil
	}
	paths := ExtractPaths(p.ToolName, p.ToolInput)
	if err := AppendToolEvent(l, sess.SessionID, turnID, p.HookEventName, p.ToolName, paths, s.now(),
		cfg.Limits.MaxEventsPerTurn, cfg.Limits.MaxEventBytesPerTurn); err != nil {
		return err
	}
	sess.Touch(s.now())
	return store.SaveSession(l, sess)
}

func (s *Service) handleStop(l *store.Layout, cfg config.Config, sess *store.Session, p Payload, res *Result) error {
	limits := displayLimits(cfg)
	q, err := store.LoadQueue(l, sess.SessionID, cfg.Retention.MaxRetainedTurns, limits)
	if err != nil {
		return err
	}
	turnID := p.TurnID
	if turnID == "" {
		turnID = sess.ActiveTurn()
	}
	if turnID == "" {
		return errors.New("Stop hook has no turn_id and the session has no active turn")
	}
	res.TurnID = turnID
	it := findItem(l, sess.SessionID, turnID, q)
	if it == nil {
		if sess.ActiveTurn() == turnID {
			sess.ClearActive()
			sess.Touch(s.now())
			_ = store.SaveSession(l, sess)
		}
		return fmt.Errorf("Stop hook: turn %s not found", turnID)
	}
	if it.Status != store.TurnRunning && !it.NeedsFinalize {
		return nil
	}
	assistant := display.TruncateUTF8(display.StripControl(p.LastAssistantMessage), cfg.Limits.MaxAssistantBytes)
	it.LastAssistantMessage = assistant
	ended := s.now()
	it.EndedAt = &ended
	cas := snapshot.NewCAS(l)
	var baseline *snapshot.State
	if it.BaselineStateID != "" {
		baseline, err = snapshot.LoadState(l, it.BaselineStateID)
		if err != nil {
			res.Warnings = append(res.Warnings, "baseline state: "+err.Error())
			baseline = nil
		}
	}
	final, cerr := snapshot.Capture(l, cas, snapshot.CaptureOptions{Previous: baseline, Now: s.now})
	if cerr != nil {
		em := "final capture failed: " + cerr.Error()
		it.Status = store.TurnFailed
		it.Error = &em
		it.NeedsFinalize = true
		if err := store.SaveTurnItem(l, sess.SessionID, it); err != nil {
			return err
		}
		q.Upsert(it, limits)
		if err := store.SaveQueue(l, q); err != nil {
			return err
		}
		sess.ClearActive()
		sess.Touch(s.now())
		_ = store.SaveSession(l, sess)
		if removed, gcErrs := s.trimQueue(l, cfg, q, true); len(removed) > 0 {
			res.Removed = append(res.Removed, removed...)
			for _, e := range gcErrs {
				res.Warnings = append(res.Warnings, e.Error())
			}
		}
		return cerr
	}
	finalStatus := store.TurnCompleted
	if it.Status == store.TurnInterrupted {
		finalStatus = store.TurnInterrupted
	}
	if it.Status == store.TurnFailed && it.BaselineStateID == "" {
		finalStatus = store.TurnFailed
	}
	it.Status = finalStatus
	it.FinalStateID = final.StateID
	it.Changes = snapshot.Diff(baseline, final, cas)
	it.NeedsFinalize = false
	it.Error = nil
	it.Unstable = anyUnstable(final)
	if err := store.SaveTurnItem(l, sess.SessionID, it); err != nil {
		return err
	}
	q.Upsert(it, limits)
	if err := store.SaveQueue(l, q); err != nil {
		return err
	}
	sess.ClearActive()
	sess.Touch(s.now())
	if err := store.SaveSession(l, sess); err != nil {
		return err
	}
	removed, gcErrs := s.trimQueue(l, cfg, q, true)
	res.Removed = append(res.Removed, removed...)
	for _, e := range gcErrs {
		res.Warnings = append(res.Warnings, e.Error())
	}
	return nil
}

func (s *Service) handleInterrupt(l *store.Layout, cfg config.Config, sess *store.Session, p Payload, res *Result) error {
	limits := displayLimits(cfg)
	q, err := store.LoadQueue(l, sess.SessionID, cfg.Retention.MaxRetainedTurns, limits)
	if err != nil {
		return err
	}
	turnID := p.TurnID
	if turnID == "" {
		turnID = sess.ActiveTurn()
	}
	res.TurnID = turnID
	if turnID != "" {
		if it := findItem(l, sess.SessionID, turnID, q); it != nil && it.Status == store.TurnRunning {
			it.Status = store.TurnInterrupted
			ended := s.now()
			it.EndedAt = &ended
			it.NeedsFinalize = true
			if err := store.SaveTurnItem(l, sess.SessionID, it); err != nil {
				return err
			}
			q.Upsert(it, limits)
			if err := store.SaveQueue(l, q); err != nil {
				return err
			}
		}
	}
	sess.ClearActive()
	sess.Touch(s.now())
	if err := store.SaveSession(l, sess); err != nil {
		return err
	}
	removed, gcErrs := s.trimQueue(l, cfg, q, false)
	res.Removed = append(res.Removed, removed...)
	for _, e := range gcErrs {
		res.Warnings = append(res.Warnings, e.Error())
	}
	return nil
}

func (s *Service) handleSessionEnd(l *store.Layout, cfg config.Config, sess *store.Session, res *Result) error {
	if _, err := s.lazyFinalize(l, cfg, sess, ""); err != nil {
		res.Warnings = append(res.Warnings, "lazy finalize: "+err.Error())
	}
	limits := displayLimits(cfg)
	q, err := store.LoadQueue(l, sess.SessionID, cfg.Retention.MaxRetainedTurns, limits)
	if err != nil {
		return err
	}
	removed, gcErrs := s.trimQueue(l, cfg, q, true)
	res.Removed = append(res.Removed, removed...)
	for _, e := range gcErrs {
		res.Warnings = append(res.Warnings, e.Error())
	}
	sess.Close(s.now())
	return store.SaveSession(l, sess)
}

// lazyFinalize repairs running or needsFinalize turns other than currentTurnID
// by capturing the current workspace once.
func (s *Service) lazyFinalize(l *store.Layout, cfg config.Config, sess *store.Session, currentTurnID string) ([]string, error) {
	items, errs := store.ListTurnItems(l, sess.SessionID)
	if len(errs) > 0 && len(items) == 0 {
		return nil, errs[0]
	}
	var candidates []*store.TurnItem
	for _, it := range items {
		if it.TurnID == currentTurnID {
			continue
		}
		if it.Status == store.TurnRunning || it.NeedsFinalize {
			candidates = append(candidates, it)
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	limits := displayLimits(cfg)
	q, err := store.LoadQueue(l, sess.SessionID, cfg.Retention.MaxRetainedTurns, limits)
	if err != nil {
		return nil, err
	}
	cas := snapshot.NewCAS(l)
	prev := s.latestFinalState(l, q)
	final, cerr := snapshot.Capture(l, cas, snapshot.CaptureOptions{Previous: prev, Now: s.now})
	if cerr != nil {
		return nil, cerr
	}
	var done []string
	for _, it := range candidates {
		var baseline *snapshot.State
		if it.BaselineStateID != "" {
			baseline, _ = snapshot.LoadState(l, it.BaselineStateID)
		}
		it.FinalStateID = final.StateID
		it.Changes = snapshot.Diff(baseline, final, cas)
		it.Unstable = anyUnstable(final)
		it.NeedsFinalize = false
		if it.Status == store.TurnRunning {
			it.Status = store.TurnInterrupted
		}
		ended := s.now()
		it.EndedAt = &ended
		if err := store.SaveTurnItem(l, sess.SessionID, it); err != nil {
			return done, err
		}
		q.Upsert(it, limits)
		done = append(done, it.TurnID)
		if sess.ActiveTurn() == it.TurnID {
			sess.ClearActive()
		}
	}
	if err := store.SaveQueue(l, q); err != nil {
		return done, err
	}
	sess.Touch(s.now())
	if err := store.SaveSession(l, sess); err != nil {
		return done, err
	}
	return done, nil
}

func (s *Service) trimQueue(l *store.Layout, cfg config.Config, q *store.Queue, doGC bool) ([]string, []error) {
	var errs []error
	removed := q.Trim(cfg.Retention.MaxRetainedTurns)
	var names []string
	if len(removed) > 0 {
		if err := store.SaveQueue(l, q); err != nil {
			return nil, []error{err}
		}
		for _, t := range removed {
			names = append(names, t.TurnID)
			if err := store.DeleteTurnFiles(l, q.SessionID, t); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if doGC {
		res, err := gc.Collect(l, gc.Options{})
		if err != nil {
			errs = append(errs, err)
		}
		if res != nil {
			errs = append(errs, res.Errors...)
		}
	}
	return names, errs
}

func findItem(l *store.Layout, sessionID, turnID string, q *store.Queue) *store.TurnItem {
	if t := q.Turn(turnID); t != nil {
		if it, err := store.LoadTurnItem(l, sessionID, t.Seq, turnID); err == nil {
			return it
		}
	}
	items, _ := store.ListTurnItems(l, sessionID)
	for _, it := range items {
		if it.TurnID == turnID {
			return it
		}
	}
	return nil
}

func (s *Service) latestFinalState(l *store.Layout, q *store.Queue) *snapshot.State {
	for i := len(q.Turns) - 1; i >= 0; i-- {
		id := q.Turns[i].FinalStateID
		if id == "" {
			continue
		}
		st, err := snapshot.LoadState(l, id)
		if err == nil {
			return st
		}
	}
	return nil
}

func (s *Service) newTurnID(seq int64) string {
	buf := make([]byte, 4)
	if s.opts.Rand != nil {
		if b, err := s.opts.Rand(4); err == nil && len(b) == 4 {
			buf = b
		}
	} else if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("turn-%d", seq)
	}
	return fmt.Sprintf("turn-%d-%s", seq, hex.EncodeToString(buf))
}

func (s *Service) now() time.Time {
	if s.opts.Now != nil {
		return s.opts.Now()
	}
	return time.Now()
}

func displayLimits(cfg config.Config) display.Limits {
	return display.Limits{
		MaxPromptBytes:    cfg.Limits.MaxPromptBytes,
		MaxAssistantBytes: cfg.Limits.MaxAssistantBytes,
		MaxFiles:          cfg.Limits.MaxDisplayFiles,
		MaxTextChars:      cfg.Limits.MaxDisplayTextChars,
	}
}

func anyUnstable(st *snapshot.State) bool {
	if st == nil {
		return false
	}
	for _, f := range st.Files {
		if f.Unstable {
			return true
		}
	}
	return false
}

type hookLogEntry struct {
	SchemaVersion int       `json:"schemaVersion"`
	At            time.Time `json:"at"`
	Event         string    `json:"event"`
	SessionID     string    `json:"sessionId,omitempty"`
	TurnID        string    `json:"turnId,omitempty"`
	Locked        bool      `json:"locked,omitempty"`
	Error         string    `json:"error,omitempty"`
	Warnings      []string  `json:"warnings,omitempty"`
	Removed       []string  `json:"removed,omitempty"`
}

func (s *Service) logHook(l *store.Layout, res *Result, err error) {
	entry := hookLogEntry{SchemaVersion: 1, At: s.now()}
	if res != nil {
		entry.Event = res.Event
		entry.SessionID = res.SessionID
		entry.TurnID = res.TurnID
		entry.Locked = res.Locked
		entry.Warnings = res.Warnings
		entry.Removed = res.Removed
	}
	if err != nil {
		entry.Error = err.Error()
	}
	data, merr := json.Marshal(entry)
	if merr != nil {
		return
	}
	data = append(data, '\n')
	dir := l.LogsDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return
	}
	path := filepath.Join(dir, "hook-"+entry.At.Format("2006-01-02")+".jsonl")
	f, oerr := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if oerr != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(data)
}
