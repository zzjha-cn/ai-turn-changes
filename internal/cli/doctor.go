package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/zzjha-cn/codex-rewind/internal/app/doctorservice"
	"github.com/zzjha-cn/codex-rewind/internal/config"
	"github.com/zzjha-cn/codex-rewind/internal/display"
	"github.com/zzjha-cn/codex-rewind/internal/snapshot"
	"github.com/zzjha-cn/codex-rewind/internal/store"
)

func cmdDoctor(args []string, out, errw io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	rootFlag := fs.String("root", "", "repository root")
	binary := fs.String("codex", "", "codex binary (default: env CXR_CODEX_BIN or codex)")
	timeout := fs.Duration("timeout", 20*time.Second, "codex probe timeout")
	if !parseFlags(fs, args, errw) {
		return 2
	}
	root, err := resolveRoot(*rootFlag)
	if err != nil {
		return fail(errw, err)
	}
	rep, err := doctorservice.Run(context.Background(), doctorservice.Options{
		Root:       root,
		Binary:     *binary,
		CxrVersion: Version,
		Timeout:    *timeout,
	})
	if err != nil {
		return fail(errw, err)
	}
	if *asJSON {
		if err := writeJSON(out, rep); err != nil {
			return fail(errw, err)
		}
	} else {
		fmt.Fprintf(out, "cxr doctor  root=%s  version=%s\n\n", rep.Root, rep.Version)
		for _, c := range rep.Checks {
			fmt.Fprintf(out, "[%s] %s: %s\n", c.Level, c.Name, c.Detail)
		}
		fmt.Fprintln(out)
		if rep.HasError() {
			fmt.Fprintln(out, "Doctor found errors that must be fixed.")
		} else if rep.HasWarning() {
			fmt.Fprintln(out, "Doctor found warnings.")
		} else {
			fmt.Fprintln(out, "All checks passed.")
		}
	}
	if rep.HasError() {
		return 1
	}
	return 0
}

type statusJSON struct {
	Root           string      `json:"root"`
	DataDir        string      `json:"dataDir"`
	Exists         bool        `json:"exists"`
	ConfigError    string      `json:"configError,omitempty"`
	Sessions       int         `json:"sessions"`
	OpenSessions   int         `json:"openSessions"`
	RetainedTurns  int         `json:"retainedTurns"`
	ActiveTurns    int         `json:"activeTurns"`
	IncompleteOps  int         `json:"incompleteOps"`
	Rewinds        int         `json:"rewinds"`
	PendingResumes int         `json:"pendingResumes"`
	LastRewind     *rewindView `json:"lastRewind,omitempty"`
	States         int         `json:"states"`
	Objects        int         `json:"objects"`
	ObjectBytes    int64       `json:"objectBytes"`
	QueueErrors    []string    `json:"queueErrors,omitempty"`
}

func cmdStatus(args []string, out, errw io.Writer) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	rootFlag := fs.String("root", "", "repository root")
	if !parseFlags(fs, args, errw) {
		return 2
	}
	root, err := resolveRoot(*rootFlag)
	if err != nil {
		return fail(errw, err)
	}
	l := store.New(root)
	st := statusJSON{Root: root, DataDir: l.DataDir(), Exists: l.Exists()}
	if !l.Exists() {
		if *asJSON {
			_ = writeJSON(out, st)
		} else {
			fmt.Fprintf(out, "No rewind data at %s\n", l.DataDir())
			fmt.Fprintln(out, "Run `cxr install --user` and start a Codex session.")
		}
		return 0
	}
	cfg, cfgErr := config.Load(l.ConfigPath())
	if cfgErr != nil {
		st.ConfigError = cfgErr.Error()
	}
	sessions, errs := store.LoadSessions(l)
	for _, e := range errs {
		st.QueueErrors = append(st.QueueErrors, e.Error())
	}
	st.Sessions = len(sessions)
	limits := display.Limits{MaxPromptBytes: cfg.Limits.MaxPromptBytes, MaxAssistantBytes: cfg.Limits.MaxAssistantBytes, MaxFiles: cfg.Limits.MaxDisplayFiles, MaxTextChars: cfg.Limits.MaxDisplayTextChars}
	for _, s := range sessions {
		if s.Open {
			st.OpenSessions++
		}
		if s.IsActive() {
			st.ActiveTurns++
		}
		q, err := store.LoadQueueReadOnly(l, s.SessionID, cfg.Retention.MaxRetainedTurns, limits)
		if err != nil {
			st.QueueErrors = append(st.QueueErrors, s.SessionID+": "+err.Error())
			continue
		}
		st.RetainedTurns += q.RetainedTurnCount
	}
	ops, _ := store.IncompleteOps(l)
	st.IncompleteOps = len(ops)

	idx := buildRewindIndex(l, sessions)
	st.Rewinds = len(idx.completed)
	for _, op := range idx.completed {
		if op.NewThreadID != nil && !idx.hasSessionForThread(*op.NewThreadID) {
			st.PendingResumes++
		}
	}
	if last := idx.latest(); last != nil {
		st.LastRewind = idx.sourceView(l, cfg, limits, last)
	}

	ids, _ := snapshot.ListStateIDs(l)
	st.States = len(ids)
	objs, bytes := objectStats(l.ObjectsDir())
	st.Objects = objs
	st.ObjectBytes = bytes

	if *asJSON {
		if err := writeJSON(out, st); err != nil {
			return fail(errw, err)
		}
		return 0
	}
	fmt.Fprintf(out, "Root:             %s\n", st.Root)
	fmt.Fprintf(out, "Data:             %s\n", st.DataDir)
	if st.ConfigError != "" {
		fmt.Fprintf(out, "Config error:     %s\n", st.ConfigError)
	}
	fmt.Fprintf(out, "Sessions:         %d (%d open)\n", st.Sessions, st.OpenSessions)
	fmt.Fprintf(out, "Retained turns:   %d\n", st.RetainedTurns)
	fmt.Fprintf(out, "Active turns:     %d\n", st.ActiveTurns)
	fmt.Fprintf(out, "Incomplete ops:   %d\n", st.IncompleteOps)
	fmt.Fprintf(out, "Rewinds:          %d", st.Rewinds)
	if st.PendingResumes > 0 {
		fmt.Fprintf(out, " (%d pending resume)", st.PendingResumes)
	}
	fmt.Fprintln(out)
	if st.LastRewind != nil {
		fmt.Fprintf(out, "Last rewind:      %s\n", formatRewindSummary(st.LastRewind))
	}
	fmt.Fprintf(out, "States:           %d\n", st.States)
	fmt.Fprintf(out, "Objects:          %d (%s)\n", st.Objects, humanBytes(st.ObjectBytes))
	for _, e := range st.QueueErrors {
		fmt.Fprintln(errw, "warning: "+e)
	}
	return 0
}

func objectStats(dir string) (int, int64) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0
	}
	count := 0
	var bytes int64
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sub, err := os.ReadDir(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		for _, f := range sub {
			if f.IsDir() {
				continue
			}
			count++
			if info, err := f.Info(); err == nil {
				bytes += info.Size()
			}
		}
	}
	return count, bytes
}
