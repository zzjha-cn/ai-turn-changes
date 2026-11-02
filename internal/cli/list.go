package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/zzjha-cn/codex-rewind/internal/config"
	"github.com/zzjha-cn/codex-rewind/internal/display"
	"github.com/zzjha-cn/codex-rewind/internal/store"
)

type sessionJSON struct {
	Session       *store.Session  `json:"session"`
	Queue         *store.Queue    `json:"queue"`
	Rewind        *rewindView     `json:"rewind,omitempty"`
	ForkedFrom    *forkedFromView `json:"forkedFrom,omitempty"`
	PendingResume bool            `json:"pendingResume,omitempty"`
}

type listJSON struct {
	Root     string        `json:"root"`
	Sessions []sessionJSON `json:"sessions"`
}

func cmdList(args []string, out, errw io.Writer) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	sessionFlag := fs.String("session", "", "session id")
	all := fs.Bool("all", false, "list every session, including pending rewind forks")
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
	if !l.Exists() {
		fmt.Fprintf(out, "No rewind data at %s\n", l.DataDir())
		fmt.Fprintln(out, "Run `cxr install --user` and start a Codex session to record turns.")
		return 0
	}
	cfg, _ := config.Load(l.ConfigPath())
	limits := display.Limits{MaxPromptBytes: cfg.Limits.MaxPromptBytes, MaxAssistantBytes: cfg.Limits.MaxAssistantBytes,
		MaxFiles: cfg.Limits.MaxDisplayFiles, MaxTextChars: cfg.Limits.MaxDisplayTextChars}
	sessions, _ := loadAllSessions(l, errw)
	if len(sessions) == 0 {
		fmt.Fprintln(out, "No sessions recorded yet.")
		return 0
	}
	idx := buildRewindIndex(l, sessions)
	var selected []*store.Session
	if *all {
		selected = sessions
	} else if *sessionFlag != "" {
		s, err := store.LoadSession(l, *sessionFlag)
		if err != nil {
			return fail(errw, err)
		}
		selected = []*store.Session{s}
	} else {
		// Focus the current branch: follow completed rewinds from the most
		// recently active session to its fork instead of stopping at the source.
		selected = []*store.Session{idx.branchTip(sessions, sessions[0])}
	}

	payload := listJSON{Root: root}
	for _, s := range selected {
		q, err := store.LoadQueueReadOnly(l, s.SessionID, cfg.Retention.MaxRetainedTurns, limits)
		if err != nil {
			fmt.Fprintf(errw, "warning: session %s: %v\n", s.SessionID, err)
			continue
		}
		if q.Rebuilt && len(q.Turns) > 0 {
			fmt.Fprintf(errw, "warning: session %s queue was rebuilt from items\n", s.SessionID)
		}
		idx.rememberQueue(s.SessionID, q)
		entry := sessionJSON{Session: s, Queue: q}
		if op := idx.sourceFor(s); op != nil {
			entry.Rewind = idx.sourceView(l, cfg, limits, op)
		}
		if op := idx.forkFor(s); op != nil {
			entry.ForkedFrom = idx.forkedFromView(l, cfg, limits, op)
		}
		payload.Sessions = append(payload.Sessions, entry)
	}
	if *all {
		payload.Sessions = idx.addPendingForks(l, payload.Sessions, cfg, limits)
	}
	if *asJSON {
		if err := writeJSON(out, payload); err != nil {
			return fail(errw, err)
		}
		return 0
	}
	for i, s := range payload.Sessions {
		if i > 0 {
			fmt.Fprintln(out)
		}
		printSessionTurns(out, s.Session, s.Queue, s.Rewind, s.ForkedFrom)
	}
	return 0
}

func printSessionTurns(out io.Writer, sess *store.Session, q *store.Queue, rewind *rewindView, forkedFrom *forkedFromView) {
	title := sess.Title
	if title == "" {
		title = "(untitled)"
	}
	fmt.Fprintf(out, "SESSION %s  %s", shortSession(sess.SessionID), title)
	if role := rewindRole(rewind, forkedFrom); role != "" {
		fmt.Fprintf(out, "  [%s]", role)
	}
	fmt.Fprintln(out)
	if sess.ActiveTurn() != "" {
		fmt.Fprintf(out, "  active turn: %s\n", sess.ActiveTurn())
	}
	printRewindMarkers(out, rewind, forkedFrom)
	if len(q.Turns) == 0 {
		fmt.Fprintln(out, "  (no retained turns)")
		return
	}
	for _, t := range q.Turns {
		target := rewind != nil && rewind.TargetTurnID == t.TurnID
		fmt.Fprintf(out, "\n[%d] %s  %s  %s", t.Seq, t.ShortID, t.StartedAt.Local().Format("15:04"), t.Status)
		if target {
			fmt.Fprint(out, "  [rewind target]")
		}
		fmt.Fprintln(out)
		if target {
			fmt.Fprintf(out, "     rewind %s: %s\n", rewind.Mode, rewindTurnNote(rewind))
		}
		if t.Display.UserText != "" {
			fmt.Fprintf(out, "     You: %s\n", display.OneLine(t.Display.UserText))
		}
		if t.Display.AssistantText != "" {
			fmt.Fprintf(out, "     AI : %s\n", display.OneLine(t.Display.AssistantText))
		}
		if len(t.Display.Files) > 0 {
			fmt.Fprintf(out, "     %s\n", t.Display.FileSummary)
			fmt.Fprintf(out, "     files: %s\n", strings.Join(t.Display.Files, ", "))
		}
	}
}

func shortSession(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

type showJSON struct {
	Session *store.Session   `json:"session"`
	Turn    *store.QueueTurn `json:"turn"`
	Item    *store.TurnItem  `json:"item"`
	Events  []store.Event    `json:"events"`
}

func cmdShow(args []string, out, errw io.Writer) int {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	sessionFlag := fs.String("session", "", "session id (default: most recent)")
	seq := fs.Int64("seq", 0, "turn sequence number")
	turnFlag := fs.String("turn", "", "full turn id")
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
	if !l.Exists() {
		return fail(errw, fmt.Errorf("no rewind data at %s", l.DataDir()))
	}
	cfg, _ := config.Load(l.ConfigPath())
	limits := display.Limits{MaxPromptBytes: cfg.Limits.MaxPromptBytes, MaxAssistantBytes: cfg.Limits.MaxAssistantBytes,
		MaxFiles: cfg.Limits.MaxDisplayFiles, MaxTextChars: cfg.Limits.MaxDisplayTextChars}
	var sess *store.Session
	if *sessionFlag != "" {
		sess, err = store.LoadSession(l, *sessionFlag)
	} else {
		sess, err = store.MostRecent(l)
	}
	if err != nil {
		return fail(errw, err)
	}
	q, err := store.LoadQueueReadOnly(l, sess.SessionID, cfg.Retention.MaxRetainedTurns, limits)
	if err != nil {
		return fail(errw, err)
	}
	var target *store.QueueTurn
	if *turnFlag != "" {
		target = q.Turn(*turnFlag)
	} else if *seq != 0 {
		target = q.TurnBySeq(*seq)
	} else {
		return fail(errw, fmt.Errorf("specify --seq or --turn"))
	}
	if target == nil {
		return fail(errw, fmt.Errorf("turn not found in retained window"))
	}
	item, err := store.LoadTurnItem(l, sess.SessionID, target.Seq, target.TurnID)
	if err != nil {
		return fail(errw, err)
	}
	events, _ := store.ReadEvents(l, sess.SessionID, target.Seq, target.TurnID)
	if *asJSON {
		if err := writeJSON(out, showJSON{Session: sess, Turn: target, Item: item, Events: events}); err != nil {
			return fail(errw, err)
		}
		return 0
	}
	fmt.Fprintf(out, "SESSION %s  %s\n", sess.SessionID, sess.Title)
	fmt.Fprintf(out, "TURN [%d] %s\n", target.Seq, target.TurnID)
	fmt.Fprintf(out, "Status:   %s\n", item.Status)
	fmt.Fprintf(out, "Started:  %s\n", item.StartedAt.Local().Format(time.RFC3339))
	if item.EndedAt != nil {
		fmt.Fprintf(out, "Ended:    %s\n", item.EndedAt.Local().Format(time.RFC3339))
	}
	if item.BaselineStateID != "" {
		fmt.Fprintf(out, "Baseline: %s\n", item.BaselineStateID)
	}
	if item.FinalStateID != "" {
		fmt.Fprintf(out, "Final:    %s\n", item.FinalStateID)
	}
	if item.NeedsFinalize {
		fmt.Fprintln(out, "NeedsFinalize: yes")
	}
	if item.Error != nil {
		fmt.Fprintf(out, "Error:    %s\n", *item.Error)
	}
	if item.Prompt != "" {
		fmt.Fprintf(out, "\nPrompt:\n%s\n", item.Prompt)
	}
	if item.LastAssistantMessage != "" {
		fmt.Fprintf(out, "\nAssistant:\n%s\n", item.LastAssistantMessage)
	}
	if len(item.Changes) > 0 {
		fmt.Fprintf(out, "\nFiles: %s\n", target.Display.FileSummary)
		for _, c := range item.Changes {
			fmt.Fprintf(out, "  %-9s %s  +%d -%d\n", c.Status, c.Path, c.Insertions, c.Deletions)
		}
	}
	if len(events) > 0 {
		fmt.Fprintf(out, "\nEvents: %d\n", len(events))
		for _, e := range events {
			fmt.Fprintf(out, "  %s  %s %s\n", e.At.Local().Format("15:04:05"), e.Event, e.Tool)
		}
	}
	return 0
}
