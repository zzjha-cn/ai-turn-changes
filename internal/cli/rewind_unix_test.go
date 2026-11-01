//go:build unix

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zzjha-cn/codex-rewind/internal/testutil"
)

func fakeCodexForCLI(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "requests.log")
	script := filepath.Join(dir, "fake-codex")
	body := `#!/bin/sh
while IFS= read -r line; do
  if [ -n "$FAKE_LOG" ]; then printf '%s\n' "$line" >> "$FAKE_LOG"; fi
  id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9][0-9]*\).*/\1/p')
  case "$line" in
    *initialize*) printf '{"id":%s,"result":{"userAgent":"fake/0.164.0"}}\n' "$id" ;;
    *thread/fork*) printf '{"id":%s,"result":{"thread":{"id":"new-thread-cli"}}}\n' "$id" ;;
    *) printf '{"id":%s,"result":{}}\n' "$id" ;;
  esac
done
`
	if err := os.WriteFile(script, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CXR_CODEX_BIN", script)
	t.Setenv("FAKE_LOG", log)
}

func readOptional(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func TestCLIRewindBefore(t *testing.T) {
	root := testutil.TempRepo(t)
	cliStart(t, root)
	for i := 1; i <= 3; i++ {
		cliTurn(t, root, i)
	}
	fakeCodexForCLI(t)
	code, out, stderr := runCLI(t, []string{"rewind", "--root", root, "--session", cliSession, "--seq", "2", "--before"}, "")
	if code != 0 {
		t.Fatalf("rewind exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	if !strings.Contains(out, "Rewind completed") || !strings.Contains(out, "codex resume new-thread-cli") {
		t.Fatalf("rewind output=%s", out)
	}
	if got := testutil.Read(t, root, "app.txt"); got != "base\nchange 1\n" {
		t.Fatalf("app=%q", got)
	}
	if !testutil.Exists(root, "f1.txt") || testutil.Exists(root, "f2.txt") || testutil.Exists(root, "f3.txt") {
		t.Fatal("file set not restored")
	}
	log := readOptional(os.Getenv("FAKE_LOG"))
	if !strings.Contains(log, `"beforeTurnId":"turn-2"`) {
		t.Fatalf("fork request log=%s", log)
	}
}

func TestCLIRewindLast(t *testing.T) {
	root := testutil.TempRepo(t)
	cliStart(t, root)
	for i := 1; i <= 3; i++ {
		cliTurn(t, root, i)
	}
	fakeCodexForCLI(t)
	code, out, stderr := runCLI(t, []string{"rewind", "--root", root, "--last"}, "")
	if code != 0 || !strings.Contains(out, "Rewind completed") {
		t.Fatalf("last exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	if got := testutil.Read(t, root, "app.txt"); got != "base\nchange 1\nchange 2\n" {
		t.Fatalf("app=%q", got)
	}
	if testutil.Exists(root, "f3.txt") {
		t.Fatal("last did not remove f3")
	}
}

func TestCLIRewindDryRunConflictAndForce(t *testing.T) {
	root := testutil.TempRepo(t)
	cliStart(t, root)
	for i := 1; i <= 3; i++ {
		cliTurn(t, root, i)
	}
	fakeCodexForCLI(t)
	content := testutil.Read(t, root, "app.txt")
	testutil.Write(t, root, "app.txt", content+"manual edit\n")

	code, out, stderr := runCLI(t, []string{"rewind", "--root", root, "--session", cliSession, "--seq", "2", "--before", "--dry-run"}, "")
	if code != 0 {
		t.Fatalf("dry-run exit=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(out, "dry-run") || !strings.Contains(out, "Conflicts:") || !strings.Contains(out, "app.txt") {
		t.Fatalf("dry-run output=%s", out)
	}
	if log := readOptional(os.Getenv("FAKE_LOG")); strings.Contains(log, "thread/fork") {
		t.Fatalf("dry-run forked: %s", log)
	}

	code, out, stderr = runCLI(t, []string{"rewind", "--root", root, "--session", cliSession, "--seq", "2", "--before"}, "")
	if code != 1 {
		t.Fatalf("conflict exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	if !strings.Contains(stderr, "refusing to overwrite") || !strings.Contains(stderr, "conflict: app.txt") {
		t.Fatalf("conflict stderr=%s", stderr)
	}
	if got := testutil.Read(t, root, "app.txt"); !strings.Contains(got, "manual edit") {
		t.Fatalf("conflict modified file: %q", got)
	}

	code, out, stderr = runCLI(t, []string{"rewind", "--root", root, "--session", cliSession, "--seq", "2", "--before", "--force"}, "")
	if code != 0 {
		t.Fatalf("force exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	if got := testutil.Read(t, root, "app.txt"); got != "base\nchange 1\n" {
		t.Fatalf("force app=%q", got)
	}
	if log := readOptional(os.Getenv("FAKE_LOG")); !strings.Contains(log, "thread/fork") {
		t.Fatalf("force did not fork: %s", log)
	}
}

func TestCLIRewindRequiresMode(t *testing.T) {
	root := testutil.TempRepo(t)
	cliStart(t, root)
	cliTurn(t, root, 1)
	code, _, stderr := runCLI(t, []string{"rewind", "--root", root, "--session", cliSession, "--seq", "1"}, "")
	if code != 1 || !strings.Contains(stderr, "specify --before, --through or --last") {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	code, _, stderr = runCLI(t, []string{"rewind", "--root", root, "--last", "--in-place"}, "")
	if code != 1 || !strings.Contains(stderr, "not implemented") {
		t.Fatalf("in-place code=%d stderr=%s", code, stderr)
	}
}
