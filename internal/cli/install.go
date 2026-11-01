package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/zzjha-cn/codex-rewind/internal/config"
	"github.com/zzjha-cn/codex-rewind/internal/hooks"
	"github.com/zzjha-cn/codex-rewind/internal/store"
)

func cmdInstall(args []string, out, errw io.Writer) int {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	user := fs.Bool("user", false, "install into CODEX_HOME/hooks.json for all projects")
	repo := fs.Bool("repo", false, "install into <repo>/.codex/hooks.json")
	rootFlag := fs.String("root", "", "repository root")
	if !parseFlags(fs, args, errw) {
		return 2
	}
	if *user == *repo {
		return fail(errw, fmt.Errorf("specify exactly one of --user or --repo"))
	}
	binary, err := binaryPath()
	if err != nil {
		return fail(errw, err)
	}
	var path string
	if *user {
		home, err := codexHomeDir()
		if err != nil {
			return fail(errw, err)
		}
		if err := os.MkdirAll(home, 0700); err != nil {
			return fail(errw, err)
		}
		path = filepath.Join(home, "hooks.json")
	} else {
		root, err := resolveRoot(*rootFlag)
		if err != nil {
			return fail(errw, err)
		}
		l := store.New(root)
		if err := l.Ensure(); err != nil {
			return fail(errw, err)
		}
		if _, err := config.Ensure(l.ConfigPath()); err != nil {
			return fail(errw, err)
		}
		dir := filepath.Join(root, ".codex")
		if err := os.MkdirAll(dir, 0700); err != nil {
			return fail(errw, err)
		}
		path = filepath.Join(dir, "hooks.json")
	}
	added, err := hooks.Install(path, binary)
	if err != nil {
		return fail(errw, err)
	}
	fmt.Fprintf(out, "Installed `%s hook` in %s\n", binary, path)
	fmt.Fprintf(out, "Hook events: %d configured (%d newly added).\n", len(hooks.DefaultEvents), added)
	fmt.Fprintln(out, "Codex will ask you to trust non-managed hooks on the next session; review and approve them there.")
	fmt.Fprintln(out, "Run `cxr doctor` to verify Codex version, app-server and hook health.")
	fmt.Fprintln(out, "Note: .codex-rewind/ lives at the repository root and can be removed by `git clean -fdx`; it is not a backup.")
	return 0
}

func cmdUninstall(args []string, out, errw io.Writer) int {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	user := fs.Bool("user", false, "remove from CODEX_HOME/hooks.json")
	repo := fs.Bool("repo", false, "remove from <repo>/.codex/hooks.json")
	rootFlag := fs.String("root", "", "repository root")
	if !parseFlags(fs, args, errw) {
		return 2
	}
	if *user == *repo {
		return fail(errw, fmt.Errorf("specify exactly one of --user or --repo"))
	}
	var path string
	if *user {
		home, err := codexHomeDir()
		if err != nil {
			return fail(errw, err)
		}
		path = filepath.Join(home, "hooks.json")
	} else {
		root, err := resolveRoot(*rootFlag)
		if err != nil {
			return fail(errw, err)
		}
		path = filepath.Join(root, ".codex", "hooks.json")
	}
	removed, err := hooks.Uninstall(path)
	if err != nil {
		return fail(errw, err)
	}
	if removed == 0 {
		fmt.Fprintf(out, "No `cxr hook` entries found in %s\n", path)
	} else {
		fmt.Fprintf(out, "Removed %d `cxr hook` entr%s from %s\n", removed, plural(removed, "y", "ies"), path)
	}
	fmt.Fprintln(out, "Recorded history in .codex-rewind/ was left untouched.")
	return 0
}

func binaryPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Abs(exe)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
