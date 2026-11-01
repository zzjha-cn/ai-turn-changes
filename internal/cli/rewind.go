package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/zzjha-cn/codex-rewind/internal/app/rewindservice"
	"github.com/zzjha-cn/codex-rewind/internal/store"
)

func cmdRewind(args []string, out, errw io.Writer) int {
	fs := flag.NewFlagSet("rewind", flag.ContinueOnError)
	sessionFlag := fs.String("session", "", "session id (default: most recent)")
	seq := fs.Int64("seq", 0, "turn sequence number")
	turnFlag := fs.String("turn", "", "full turn id")
	before := fs.Bool("before", false, "fork before the target turn and restore its baseline")
	through := fs.Bool("through", false, "fork through the target turn and restore its final state")
	last := fs.Bool("last", false, "undo the newest retained turn")
	dryRun := fs.Bool("dry-run", false, "plan only; do not fork or modify files")
	resume := fs.Bool("resume", false, "continue an incomplete rewind operation")
	force := fs.Bool("force", false, "overwrite conflicts / stale active markers (never bypasses a running turn or held lock)")
	inPlace := fs.Bool("in-place", false, "unsupported in the first version")
	rootFlag := fs.String("root", "", "repository root")
	if !parseFlags(fs, args, errw) {
		return 2
	}
	if *inPlace {
		return fail(errw, errors.New("--in-place is not implemented; the default soft fork keeps the original thread"))
	}
	if *last {
		if *seq != 0 || *turnFlag != "" || *before || *through {
			return fail(errw, errors.New("--last cannot be combined with --seq, --turn, --before or --through"))
		}
	} else {
		if *before && *through {
			return fail(errw, errors.New("use either --before or --through"))
		}
		if !*before && !*through {
			return fail(errw, errors.New("specify --before, --through or --last"))
		}
		if *seq == 0 && *turnFlag == "" {
			return fail(errw, errors.New("specify --seq or --turn"))
		}
	}
	mode := store.ModeBefore
	if *through {
		mode = store.ModeThrough
	}
	if *last {
		mode = store.ModeBefore
	}
	root, err := resolveRoot(*rootFlag)
	if err != nil {
		return fail(errw, err)
	}
	svc := rewindservice.New(rewindservice.Options{
		Root:   root,
		DryRun: *dryRun,
		Force:  *force,
		Resume: *resume,
		Forker: &rewindservice.RealForker{},
	})
	res, err := svc.Rewind(context.Background(), rewindservice.Request{
		SessionID: *sessionFlag,
		Seq:       *seq,
		TurnID:    *turnFlag,
		Last:      *last,
		Mode:      mode,
	})
	var conflictErr *rewindservice.ConflictError
	if err != nil && errors.As(err, &conflictErr) {
		fmt.Fprintln(errw, "cxr: refusing to overwrite manual changes")
		for _, c := range conflictErr.Conflicts {
			fmt.Fprintf(errw, "  conflict: %s: %s\n", c.Path, c.Reason)
		}
		fmt.Fprintln(errw, "Re-run with --force to replace these files, or stash your changes first.")
		printWarnings(errw, res)
		return 1
	}
	if res != nil {
		printWarnings(errw, res)
	}
	if err != nil {
		return fail(errw, err)
	}
	if *dryRun {
		printRewindPlan(out, res)
		return 0
	}
	printRewindResult(out, res)
	return 0
}

func printWarnings(w io.Writer, res *rewindservice.Result) {
	if res == nil {
		return
	}
	for _, warning := range res.Warnings {
		fmt.Fprintln(w, "warning: "+warning)
	}
}

func printRewindPlan(out io.Writer, res *rewindservice.Result) {
	fmt.Fprintln(out, "Rewind plan (dry-run; no changes made)")
	fmt.Fprintf(out, "Session:      %s  %s\n", res.Session.SessionID, res.Session.Title)
	fmt.Fprintf(out, "Target turn:  [%d] %s\n", res.Target.Seq, res.Target.TurnID)
	fmt.Fprintf(out, "Mode:         %s\n", res.Mode)
	fmt.Fprintf(out, "Target state: %s\n", res.TargetStateID)
	if res.Plan == nil {
		fmt.Fprintln(out, "No restore plan available.")
		return
	}
	fmt.Fprintf(out, "Actions:      %d write(s), %d delete(s)\n", res.Plan.Writes(), res.Plan.Deletes())
	for _, a := range res.Plan.Actions {
		fmt.Fprintf(out, "  %-6s %s\n", a.Kind, a.Path)
	}
	if len(res.Plan.Conflicts) > 0 {
		fmt.Fprintln(out, "Conflicts:")
		for _, c := range res.Plan.Conflicts {
			fmt.Fprintf(out, "  %s: %s\n", c.Path, c.Reason)
		}
	}
	if len(res.Plan.Conflicts) > 0 {
		fmt.Fprintln(out, "Resolve the conflicts or re-run with --force to overwrite them.")
	} else {
		fmt.Fprintln(out, "Run without --dry-run to fork and restore.")
	}
}

func printRewindResult(out io.Writer, res *rewindservice.Result) {
	fmt.Fprintln(out, "Rewind completed.")
	fmt.Fprintf(out, "Session:      %s\n", res.Session.SessionID)
	fmt.Fprintf(out, "Target turn:  [%d] %s (%s)\n", res.Target.Seq, res.Target.TurnID, res.Mode)
	fmt.Fprintf(out, "Target state: %s\n", res.TargetStateID)
	if res.SafetyStateID != "" {
		fmt.Fprintf(out, "Safety state: %s\n", res.SafetyStateID)
	}
	if res.Plan != nil {
		fmt.Fprintf(out, "Files:        %d written, %d deleted\n", res.Plan.Writes(), res.Plan.Deletes())
	}
	if len(res.Applied) > 0 {
		kinds := map[string]string{}
		if res.Plan != nil {
			for _, a := range res.Plan.Actions {
				kinds[a.Path] = string(a.Kind)
			}
		}
		for _, p := range res.Applied {
			fmt.Fprintf(out, "  %-6s %s\n", kinds[p], p)
		}
	}
	if res.NewThreadID != "" {
		fmt.Fprintf(out, "New thread:   %s (forked from %s)\n", res.NewThreadID, res.Session.ThreadID)
		fmt.Fprintln(out)
		fmt.Fprintf(out, "Continue with:\n  codex resume %s\n", res.NewThreadID)
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Note: Git index, .git/, ignored files, databases, network calls, containers")
	fmt.Fprintln(out, "and background processes were not restored.")
}
