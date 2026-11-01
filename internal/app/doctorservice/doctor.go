// Package doctorservice checks rewind data consistency and Codex compatibility.
package doctorservice

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zzjha-cn/codex-rewind/internal/codex/appserver"
	"github.com/zzjha-cn/codex-rewind/internal/config"
	"github.com/zzjha-cn/codex-rewind/internal/display"
	"github.com/zzjha-cn/codex-rewind/internal/hooks"
	"github.com/zzjha-cn/codex-rewind/internal/snapshot"
	"github.com/zzjha-cn/codex-rewind/internal/store"
	"github.com/zzjha-cn/codex-rewind/internal/workspace"
)

type Level string

const (
	LevelOK    Level = "ok"
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

type Check struct {
	Level  Level  `json:"level"`
	Name   string `json:"name"`
	Detail string `json:"detail"`
}

type Report struct {
	Root    string                  `json:"root"`
	Version string                  `json:"version,omitempty"`
	Checks  []Check                 `json:"checks"`
	Caps    *appserver.Capabilities `json:"capabilities,omitempty"`
}

func (r *Report) Add(level Level, name, detail string) {
	r.Checks = append(r.Checks, Check{Level: level, Name: name, Detail: detail})
}

func (r *Report) HasError() bool {
	for _, c := range r.Checks {
		if c.Level == LevelError {
			return true
		}
	}
	return false
}

func (r *Report) HasWarning() bool {
	for _, c := range r.Checks {
		if c.Level == LevelWarn {
			return true
		}
	}
	return false
}

type Options struct {
	Root       string
	Binary     string
	Env        []string
	CodexHome  string
	CxrVersion string
	Timeout    time.Duration
}

// Run performs all doctor checks.
func Run(ctx context.Context, opts Options) (*Report, error) {
	root, err := workspace.Resolve("", opts.Root)
	if err != nil {
		return nil, err
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 20 * time.Second
	}
	rep := &Report{Root: root, Version: opts.CxrVersion}
	l := store.New(root)
	rep.Add(LevelInfo, "workspace", root)
	if !l.Exists() {
		rep.Add(LevelWarn, "layout", "no .codex-rewind/ yet; run `cxr install --user` and start a Codex session")
	} else {
		rep.Add(LevelOK, "layout", l.DataDir())
	}

	cfg := config.Default()
	if l.Exists() {
		cfg, err = config.Load(l.ConfigPath())
		if err != nil {
			rep.Add(LevelError, "config", err.Error())
		} else if _, serr := os.Stat(l.ConfigPath()); errors.Is(serr, os.ErrNotExist) {
			rep.Add(LevelWarn, "config", "config.json missing; built-in defaults are being used")
		} else {
			rep.Add(LevelOK, "config", fmt.Sprintf("schema %d, window %d turns, clean after %d days",
				cfg.SchemaVersion, cfg.Retention.MaxRetainedTurns, cfg.Retention.CleanAfterDays))
		}
	}

	checkLayout(rep, l)
	if l.Exists() {
		checkLock(rep, l)
		checkSessions(ctx, rep, l, cfg)
		checkOps(rep, l)
		checkStorage(rep, l)
	}

	checkCodex(ctx, rep, opts)
	checkHooks(rep, opts, root)
	if l.Exists() {
		rep.Add(LevelWarn, "data-safety", ".codex-rewind/ can be deleted by `git clean -fdx` or similar; do not rely on it as a backup")
	}
	return rep, nil
}

func checkLayout(rep *Report, l *store.Layout) {
	if !l.Exists() {
		return
	}
	data, err := os.ReadFile(l.IgnorePath())
	if err != nil {
		rep.Add(LevelError, "self-ignore", ".gitignore missing: "+err.Error())
	} else if strings.TrimSpace(string(data)) != "*" {
		rep.Add(LevelError, "self-ignore", "expected .codex-rewind/.gitignore to contain only *")
	} else {
		rep.Add(LevelOK, "self-ignore", "data directory is self-ignored")
	}
	if !workspace.IsGitRepo(l.Root) {
		rep.Add(LevelWarn, "git", "workspace is not a Git repository; scanning is unsupported in the first version")
		return
	}
	lines, err := workspace.CheckDataIgnored(l.Root)
	if err != nil {
		rep.Add(LevelWarn, "git", "git status check failed: "+err.Error())
		return
	}
	if len(lines) > 0 {
		rep.Add(LevelError, "git", fmt.Sprintf(".codex-rewind appears in git status: %s", strings.Join(lines, "; ")))
	} else {
		rep.Add(LevelOK, "git", ".codex-rewind does not appear in git status")
	}
}

func checkLock(rep *Report, l *store.Layout) {
	if _, err := os.Stat(l.LockPath()); err != nil {
		return
	}
	stale, err := store.Stale(l.LockPath())
	if err != nil {
		rep.Add(LevelWarn, "lock", "cannot inspect lock: "+err.Error())
		return
	}
	if stale {
		rep.Add(LevelWarn, "lock", "stale lock file detected; no process holds the flock (safe to proceed)")
		return
	}
	info, _ := store.LockInfoPath(l.LockPath())
	if info.PID > 0 {
		rep.Add(LevelInfo, "lock", fmt.Sprintf("lock file present; last holder pid %d, started %s", info.PID, info.StartedAt.Format(time.RFC3339)))
	} else {
		rep.Add(LevelInfo, "lock", "lock file present; no stale holder detected")
	}
}

func checkSessions(ctx context.Context, rep *Report, l *store.Layout, cfg config.Config) {
	_ = ctx
	limits := display.Limits{MaxPromptBytes: cfg.Limits.MaxPromptBytes, MaxAssistantBytes: cfg.Limits.MaxAssistantBytes,
		MaxFiles: cfg.Limits.MaxDisplayFiles, MaxTextChars: cfg.Limits.MaxDisplayTextChars}
	sessions, errs := store.LoadSessions(l)
	for _, e := range errs {
		rep.Add(LevelError, "session", e.Error())
	}
	if len(sessions) == 0 && len(errs) == 0 {
		rep.Add(LevelInfo, "sessions", "no sessions recorded yet")
		return
	}
	rep.Add(LevelOK, "sessions", fmt.Sprintf("%d readable session(s)", len(sessions)))
	stateRefs := map[string][]string{}
	cas := snapshot.NewCAS(l)
	missingObjects := map[string]string{}
	corruptStates := 0
	for _, s := range sessions {
		q, err := store.LoadQueueReadOnly(l, s.SessionID, cfg.Retention.MaxRetainedTurns, limits)
		if err != nil {
			rep.Add(LevelError, "queue "+s.SessionID, err.Error())
			continue
		}
		if q.Rebuilt {
			rep.Add(LevelWarn, "queue "+s.SessionID, "queue.json was missing or corrupt and was rebuilt from items/")
		}
		inQueue := map[string]bool{}
		for _, t := range q.Turns {
			inQueue[t.TurnID] = true
			it, ierr := store.LoadTurnItem(l, s.SessionID, t.Seq, t.TurnID)
			if ierr != nil {
				rep.Add(LevelError, "turn "+t.TurnID, "queue references unreadable item: "+ierr.Error())
			} else {
				if it.Status != t.Status {
					rep.Add(LevelWarn, "turn "+t.TurnID, fmt.Sprintf("queue status %s != item status %s", t.Status, it.Status))
				}
				if it.NeedsFinalize {
					rep.Add(LevelWarn, "turn "+t.TurnID, "needsFinalize=true; the next hook or --resume should finalize it")
				}
			}
			if t.Status == store.TurnCompleted && t.FinalStateID == "" {
				rep.Add(LevelError, "turn "+t.TurnID, "completed turn has no final state")
			}
			if t.BaselineStateID != "" {
				stateRefs[t.BaselineStateID] = append(stateRefs[t.BaselineStateID], t.TurnID+" baseline")
			}
			if t.FinalStateID != "" {
				stateRefs[t.FinalStateID] = append(stateRefs[t.FinalStateID], t.TurnID+" final")
			}
		}
		items, ierrs := store.ListTurnItems(l, s.SessionID)
		for _, e := range ierrs {
			rep.Add(LevelError, "turn", e.Error())
		}
		for _, it := range items {
			if !inQueue[it.TurnID] {
				rep.Add(LevelWarn, "turn "+it.TurnID, "item file is outside the retained queue window")
			}
		}
		if s.IsActive() {
			t := q.Turn(s.ActiveTurn())
			if t == nil {
				rep.Add(LevelWarn, "active "+s.SessionID, "activeTurnId points to a turn outside the retained window")
			} else if t.Status != store.TurnRunning {
				rep.Add(LevelWarn, "active "+s.SessionID, "activeTurnId points to a "+t.Status+" turn")
			} else {
				rep.Add(LevelInfo, "active "+s.SessionID, "turn "+t.TurnID+" is active")
			}
		}
	}
	stateIDs := make([]string, 0, len(stateRefs))
	for id := range stateRefs {
		stateIDs = append(stateIDs, id)
	}
	sort.Strings(stateIDs)
	for _, id := range stateIDs {
		st, err := snapshot.LoadState(l, id)
		if err != nil {
			rep.Add(LevelError, "state "+id, err.Error())
			corruptStates++
			continue
		}
		for _, f := range st.Files {
			if !f.Exists || f.OID == "" || f.IsSymlink {
				continue
			}
			if !cas.Has(f.OID) {
				if _, ok := missingObjects[f.OID]; !ok {
					missingObjects[f.OID] = f.Path
				}
			}
		}
	}
	if corruptStates == 0 && len(missingObjects) == 0 {
		rep.Add(LevelOK, "states", fmt.Sprintf("%d referenced state(s) present, all objects exist", len(stateIDs)))
	}
	if len(missingObjects) > 0 {
		var examples []string
		for oid, path := range missingObjects {
			examples = append(examples, oid+" ("+path+")")
			if len(examples) >= 3 {
				break
			}
		}
		rep.Add(LevelError, "objects", fmt.Sprintf("%d referenced object(s) missing; rewind will fail closed: %s", len(missingObjects), strings.Join(examples, ", ")))
	}
}

func checkOps(rep *Report, l *store.Layout) {
	ops, errs := store.ListRewindOps(l)
	for _, e := range errs {
		rep.Add(LevelError, "rewind-op", e.Error())
	}
	incomplete := 0
	for _, op := range ops {
		if !op.Phase.Incomplete() {
			continue
		}
		incomplete++
		detail := fmt.Sprintf("%s phase=%s target=%s", op.OpID, op.Phase, op.TargetTurnID)
		if op.NewThreadID != nil && *op.NewThreadID != "" {
			detail += " newThread=" + *op.NewThreadID
		}
		if op.SafetyStateID != nil && *op.SafetyStateID != "" {
			detail += " safety=" + *op.SafetyStateID
		}
		switch op.Phase {
		case "planned", "safety_captured":
			detail += "; no thread forked yet, re-run the rewind"
		case "thread_forked", "files_restoring", "failed_retryable":
			detail += "; run `cxr rewind --resume` to continue"
		case "verified":
			detail += "; files verified, resume to mark completed"
		}
		rep.Add(LevelWarn, "rewind-op "+op.OpID, detail)
	}
	if incomplete == 0 {
		rep.Add(LevelOK, "rewind-ops", fmt.Sprintf("%d operation(s), none incomplete", len(ops)))
	}
}

func checkStorage(rep *Report, l *store.Layout) {
	var objects int
	var objectBytes int64
	_ = filepath.WalkDir(l.ObjectsDir(), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		objects++
		if info, err := d.Info(); err == nil {
			objectBytes += info.Size()
		}
		return nil
	})
	ids, _ := snapshot.ListStateIDs(l)
	rep.Add(LevelInfo, "storage", fmt.Sprintf("%d object(s), %.1f MiB; %d state file(s)", objects, float64(objectBytes)/(1<<20), len(ids)))
}

func checkCodex(ctx context.Context, rep *Report, opts Options) {
	caps, err := appserver.Detect(ctx, appserver.Options{Binary: opts.Binary, Env: opts.Env, Timeout: opts.Timeout})
	if err != nil {
		rep.Add(LevelError, "codex", "cannot inspect Codex CLI: "+err.Error())
	} else {
		rep.Caps = caps
		if caps.Version == "" {
			rep.Add(LevelError, "codex", "could not determine Codex CLI version")
		} else {
			rep.Add(LevelOK, "codex", "version "+caps.Version+fmt.Sprintf(" (schema=%v)", caps.SchemaAvailable))
		}
		for _, n := range caps.Notes {
			rep.Add(LevelInfo, "capability", n)
		}
		if !caps.SupportsBeforeTurnID {
			rep.Add(LevelError, "capability", "thread/fork.beforeTurnId is unavailable; --before rewinds cannot fork")
		} else {
			rep.Add(LevelOK, "capability", "thread/fork.beforeTurnId available")
		}
		if !caps.SupportsLastTurnID {
			rep.Add(LevelWarn, "capability", "thread/fork.lastTurnId unavailable; --through rewinds cannot fork")
		}
		if !caps.SupportsRevert {
			rep.Add(LevelInfo, "capability", "thread/revert unavailable (in-place mode is not implemented)")
		}
	}
	init, err := appserver.Handshake(ctx, appserver.Options{Binary: opts.Binary, Env: opts.Env, Timeout: opts.Timeout})
	if err != nil {
		rep.Add(LevelError, "app-server", "initialize failed: "+err.Error())
	} else {
		rep.Add(LevelOK, "app-server", "initialize ok: "+init.UserAgent+"; temporary process exited")
	}
}

func checkHooks(rep *Report, opts Options, root string) {
	codexHome := opts.CodexHome
	if codexHome == "" {
		codexHome = os.Getenv("CODEX_HOME")
	}
	if codexHome == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			codexHome = filepath.Join(home, ".codex")
		}
	}
	var paths []string
	if codexHome != "" {
		paths = append(paths, filepath.Join(codexHome, "hooks.json"))
	}
	paths = append(paths, filepath.Join(root, ".codex", "hooks.json"))
	var found []string
	for _, p := range paths {
		d, err := hooks.Load(p)
		if err != nil {
			rep.Add(LevelError, "hooks", p+": "+err.Error())
			continue
		}
		if d.HasCxrCommand() {
			found = append(found, p)
		}
	}
	if len(found) == 0 {
		rep.Add(LevelWarn, "hooks", "no `cxr hook` entries found; run `cxr install --user` or `cxr install --repo`")
		return
	}
	rep.Add(LevelOK, "hooks", "installed in "+strings.Join(found, ", ")+"; Codex may require trusting non-managed hooks")
}
