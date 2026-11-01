package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/zzjha-cn/codex-rewind/internal/app/cleanservice"
	"github.com/zzjha-cn/codex-rewind/internal/gc"
	"github.com/zzjha-cn/codex-rewind/internal/store"
)

func cmdClean(args []string, out, errw io.Writer) int {
	fs := flag.NewFlagSet("clean", flag.ContinueOnError)
	older := fs.String("older-than", "", "age such as 30d, 48h or 2w (default from config)")
	dryRun := fs.Bool("dry-run", false, "show the plan without deleting")
	yes := fs.Bool("yes", false, "actually delete")
	includeOpen := fs.Bool("include-stale-open", false, "also expire sessions that never closed cleanly")
	includeIncomplete := fs.Bool("include-incomplete", false, "also delete old incomplete rewind operations")
	orphans := fs.Bool("orphans", false, "include orphan state/object collection (always on)")
	asJSON := fs.Bool("json", false, "emit JSON")
	rootFlag := fs.String("root", "", "repository root")
	if !parseFlags(fs, args, errw) {
		return 2
	}
	var age time.Duration
	if *older != "" {
		var err error
		age, err = cleanservice.ParseAge(*older)
		if err != nil {
			return fail(errw, err)
		}
	}
	root, err := resolveRoot(*rootFlag)
	if err != nil {
		return fail(errw, err)
	}
	plan, err := cleanservice.Run(context.Background(), cleanservice.Options{
		Root:              root,
		OlderThan:         age,
		DryRun:            *dryRun,
		Yes:               *yes,
		IncludeStaleOpen:  *includeOpen,
		IncludeIncomplete: *includeIncomplete,
	})
	if err != nil {
		return fail(errw, err)
	}
	_ = orphans
	for _, w := range plan.Warnings {
		fmt.Fprintln(errw, "warning: "+w)
	}
	if *asJSON {
		if err := writeJSON(out, plan); err != nil {
			return fail(errw, err)
		}
		return 0
	}
	printCleanPlan(out, plan)
	return 0
}

func printCleanPlan(out io.Writer, plan *cleanservice.Plan) {
	if len(plan.Sessions) == 0 && len(plan.Ops) == 0 && len(plan.States) == 0 && len(plan.Objects) == 0 && len(plan.Tmp) == 0 {
		fmt.Fprintln(out, "Nothing to clean.")
		return
	}
	mode := "Clean plan (dry-run)"
	if plan.Executed {
		mode = "Clean report"
	}
	fmt.Fprintf(out, "%s, cutoff %s\n", mode, plan.Cutoff.Local().Format(time.RFC3339))
	if len(plan.Sessions) > 0 {
		fmt.Fprintf(out, "Sessions (%d):\n", len(plan.Sessions))
		for _, s := range plan.Sessions {
			state := "open"
			if s.Closed {
				state = "closed"
			}
			fmt.Fprintf(out, "  %s  last %s  %s  %d turn(s)  %s\n",
				s.SessionID, s.LastActivity.Local().Format("2006-01-02 15:04"), state, s.TurnCount, humanBytes(s.Bytes))
		}
	}
	if len(plan.Ops) > 0 {
		fmt.Fprintf(out, "Rewind operations (%d):\n", len(plan.Ops))
		for _, o := range plan.Ops {
			fmt.Fprintf(out, "  %s  %s  %s\n", o.OpID, o.Phase, o.UpdatedAt.Local().Format("2006-01-02 15:04"))
		}
	}
	printStringList(out, "States", plan.States)
	printStringList(out, "Objects", plan.Objects)
	if len(plan.Tmp) > 0 {
		fmt.Fprintf(out, "Temporary entries: %d\n", len(plan.Tmp))
	}
	fmt.Fprintf(out, "Reclaimable: %s\n", humanBytes(plan.ReclaimBytes))
	if plan.Executed {
		fmt.Fprintln(out, "Clean completed.")
	} else {
		fmt.Fprintln(out, "No changes made. Re-run with --yes to delete.")
	}
}

func printStringList(out io.Writer, name string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(out, "%s (%d):\n", name, len(items))
	limit := len(items)
	if limit > 10 {
		limit = 10
	}
	for _, s := range items[:limit] {
		fmt.Fprintf(out, "  %s\n", s)
	}
	if len(items) > limit {
		fmt.Fprintf(out, "  ... %d more\n", len(items)-limit)
	}
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func cmdGC(args []string, out, errw io.Writer) int {
	fs := flag.NewFlagSet("gc", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "show what would be collected")
	yes := fs.Bool("yes", false, "actually delete unreachable data")
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
		if *asJSON {
			_ = writeJSON(out, gc.Result{DryRun: true})
		} else {
			fmt.Fprintf(out, "No rewind data at %s\n", l.DataDir())
		}
		return 0
	}
	lock, err := store.TryAcquire(l.LockPath(), "cxr gc")
	if err != nil {
		if errors.Is(err, store.ErrLocked) {
			return fail(errw, errors.New("repository lock is held by a running hook or rewind"))
		}
		return fail(errw, err)
	}
	defer lock.Release()
	effectiveDry := *dryRun || !*yes
	res, err := gc.Collect(l, gc.Options{DryRun: effectiveDry})
	if err != nil {
		return fail(errw, err)
	}
	if *asJSON {
		if err := writeJSON(out, res); err != nil {
			return fail(errw, err)
		}
		return 0
	}
	for _, e := range res.Errors {
		fmt.Fprintln(errw, "warning: "+e.Error())
	}
	if res.DryRun {
		fmt.Fprintf(out, "GC plan: %d reachable state(s), %d reachable object(s)\n", res.ReachableStates, res.ReachableObjects)
		printStringList(out, "Unreachable states", res.UnreachableStates)
		printStringList(out, "Unreachable objects", res.UnreachableObjects)
		fmt.Fprintln(out, "No changes made. Re-run with --yes to delete.")
		return 0
	}
	fmt.Fprintf(out, "GC completed: deleted %d state(s), %d object(s), freed %s\n",
		len(res.DeletedStates), len(res.DeletedObjects), humanBytes(res.BytesFreed))
	return 0
}
