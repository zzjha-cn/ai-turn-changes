package cli

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/zzjha-cn/codex-rewind/internal/app/hookservice"
)

// cmdHook reads one hook payload from stdin. It is fail-open unless
// config.strictHook is enabled.
func cmdHook(args []string, in io.Reader, out, errw io.Writer) int {
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	root := fs.String("root", "", "repository root")
	verbose := fs.Bool("verbose", false, "print diagnostics to stdout")
	if !parseFlags(fs, args, errw) {
		return 2
	}
	payload, err := hookservice.DecodePayload(in)
	if err != nil {
		fmt.Fprintln(errw, "cxr hook: "+err.Error())
		return 0
	}
	svc := hookservice.New(hookservice.Options{RootOverride: *root})
	res, err := svc.Handle(context.Background(), payload)
	if err != nil {
		fmt.Fprintln(errw, "cxr hook: "+err.Error())
		if res != nil {
			for _, w := range res.Warnings {
				fmt.Fprintln(errw, "cxr hook: warning: "+w)
			}
			if res.Strict {
				return 2
			}
		}
		return 0
	}
	if *verbose && res != nil {
		fmt.Fprintf(out, "event=%s session=%s turn=%s root=%s\n", res.Event, res.SessionID, res.TurnID, res.Root)
		for _, w := range res.Warnings {
			fmt.Fprintln(out, "warning: "+w)
		}
	}
	return 0
}
