// Package cli implements the cxr command line.
package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/zzjha-cn/codex-rewind/internal/store"
	"github.com/zzjha-cn/codex-rewind/internal/workspace"
)

// Version is overridden by release builds.
var Version = "0.1.0"

// Main dispatches one cxr invocation and returns the process exit code.
func Main(args []string, in io.Reader, out, errw io.Writer) int {
	if len(args) == 0 {
		printUsage(out)
		return 0
	}
	cmd := args[0]
	rest := args[1:]
	switch cmd {
	case "hook":
		return cmdHook(rest, in, out, errw)
	case "list", "ls":
		return cmdList(rest, out, errw)
	case "show":
		return cmdShow(rest, out, errw)
	case "rewind":
		return cmdRewind(rest, out, errw)
	case "clean":
		return cmdClean(rest, out, errw)
	case "gc":
		return cmdGC(rest, out, errw)
	case "doctor":
		return cmdDoctor(rest, out, errw)
	case "status":
		return cmdStatus(rest, out, errw)
	case "install":
		return cmdInstall(rest, out, errw)
	case "uninstall":
		return cmdUninstall(rest, out, errw)
	case "version", "--version", "-v":
		fmt.Fprintf(out, "cxr %s\n", Version)
		return 0
	case "help", "-h", "--help":
		printUsage(out)
		return 0
	default:
		fmt.Fprintf(errw, "cxr: unknown command %q\n\n", cmd)
		printUsage(errw)
		return 2
	}
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `cxr - Codex turn-level rewind

Usage:
  cxr hook [--root DIR] [--verbose]           handle a Codex lifecycle hook on stdin
  cxr list [--session ID] [--all] [--json]    show retained turns
  cxr show --session ID --seq N [--json]      show one turn in detail
  cxr rewind [--last | --session ID --seq N | --session ID --turn T]
             [--before | --through] [--dry-run] [--resume] [--force]
  cxr clean [--older-than 30d] [--dry-run] [--yes]
            [--include-stale-open] [--include-incomplete] [--orphans] [--json]
  cxr gc [--dry-run] [--yes] [--json]         collect unreachable states/objects
  cxr doctor [--json]                        check data, Codex and hook health
  cxr status [--json]                        show local rewind status
  cxr install (--user | --repo)              install Codex hooks
  cxr uninstall (--user | --repo)            remove Codex hooks
  cxr version

All commands accept --root DIR to select a repository root.

Rewind semantics:
  --before   fork the conversation before T and restore T's baseline files
  --through  fork through T and restore T's final files
  --last     undo the newest retained turn (equivalent to --before on it)

Rewind does not restore the Git index, .git/, ignored files, databases,
network calls, containers or background processes.
`)
}

// resolveRoot is used by every command that reads repository data.
func resolveRoot(flagValue string) (string, error) {
	return workspace.Resolve("", flagValue)
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func fail(errw io.Writer, err error) int {
	fmt.Fprintln(errw, "cxr: "+err.Error())
	return 1
}

func parseFlags(fs *flag.FlagSet, args []string, errw io.Writer) bool {
	fs.SetOutput(errw)
	if err := fs.Parse(args); err != nil {
		return false
	}
	return true
}

// loadAllSessions returns readable sessions and prints corrupt-session warnings.
func loadAllSessions(l *store.Layout, errw io.Writer) ([]*store.Session, []error) {
	sessions, errs := store.LoadSessions(l)
	for _, err := range errs {
		fmt.Fprintln(errw, "warning: "+err.Error())
	}
	return sessions, errs
}

func codexHomeDir() (string, error) {
	if v := os.Getenv("CODEX_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex"), nil
}
