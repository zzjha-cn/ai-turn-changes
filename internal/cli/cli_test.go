package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zzjha-cn/codex-rewind/internal/hooks"
	"github.com/zzjha-cn/codex-rewind/internal/testutil"
)

const cliSession = "sess-cli"

func runCLI(t *testing.T, args []string, stdin string) (int, string, string) {
	t.Helper()
	var out, stderr bytes.Buffer
	code := Main(args, strings.NewReader(stdin), &out, &stderr)
	return code, out.String(), stderr.String()
}

func hookPayload(t *testing.T, root string, fields map[string]any) string {
	t.Helper()
	base := map[string]any{
		"session_id": cliSession,
		"thread_id":  cliSession,
		"cwd":        root,
	}
	for k, v := range fields {
		base[k] = v
	}
	data, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func hookEvent(t *testing.T, root string, fields map[string]any) {
	t.Helper()
	code, _, stderr := runCLI(t, []string{"hook", "--root", root}, hookPayload(t, root, fields))
	if code != 0 {
		t.Fatalf("hook exit=%d stderr=%s", code, stderr)
	}
}

func cliStart(t *testing.T, root string) {
	t.Helper()
	hookEvent(t, root, map[string]any{"hook_event_name": "SessionStart", "source": "cli"})
}

func cliTurn(t *testing.T, root string, n int) string {
	t.Helper()
	turnID := "turn-" + string(rune('0'+n))
	hookEvent(t, root, map[string]any{
		"hook_event_name": "UserPromptSubmit", "turn_id": turnID, "prompt": "prompt " + string(rune('0'+n)),
	})
	content := testutil.Read(t, root, "app.txt")
	testutil.Write(t, root, "app.txt", content+"change "+string(rune('0'+n))+"\n")
	testutil.Write(t, root, "f"+string(rune('0'+n))+".txt", "file\n")
	hookEvent(t, root, map[string]any{
		"hook_event_name": "Stop", "turn_id": turnID, "last_assistant_message": "reply " + string(rune('0'+n)),
	})
	return turnID
}

func TestCLIHookListShowStatusCleanGC(t *testing.T) {
	root := testutil.TempRepo(t)
	cliStart(t, root)
	cliTurn(t, root, 1)
	cliTurn(t, root, 2)

	code, out, stderr := runCLI(t, []string{"list", "--root", root}, "")
	if code != 0 {
		t.Fatalf("list exit=%d stderr=%s", code, stderr)
	}
	for _, want := range []string{"SESSION", "prompt 1", "reply 1", "f1.txt", "files"} {
		if !strings.Contains(out, want) {
			t.Fatalf("list output missing %q:\n%s", want, out)
		}
	}

	code, out, stderr = runCLI(t, []string{"list", "--root", root, "--json"}, "")
	if code != 0 {
		t.Fatalf("list json exit=%d stderr=%s", code, stderr)
	}
	var lj listJSON
	if err := json.Unmarshal([]byte(out), &lj); err != nil {
		t.Fatalf("list json: %v\n%s", err, out)
	}
	if lj.Root == "" || len(lj.Sessions) != 1 || len(lj.Sessions[0].Queue.Turns) != 2 {
		t.Fatalf("list json payload=%+v", lj)
	}
	if lj.Sessions[0].Queue.SchemaVersion != 1 {
		t.Fatalf("queue schema=%d", lj.Sessions[0].Queue.SchemaVersion)
	}

	code, out, stderr = runCLI(t, []string{"show", "--root", root, "--session", cliSession, "--seq", "1"}, "")
	if code != 0 {
		t.Fatalf("show exit=%d stderr=%s", code, stderr)
	}
	for _, want := range []string{"TURN [1]", "prompt 1", "reply 1", "Baseline:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("show output missing %q:\n%s", want, out)
		}
	}
	code, out, stderr = runCLI(t, []string{"show", "--root", root, "--session", cliSession, "--seq", "1", "--json"}, "")
	if code != 0 {
		t.Fatalf("show json exit=%d stderr=%s", code, stderr)
	}
	var sj showJSON
	if err := json.Unmarshal([]byte(out), &sj); err != nil {
		t.Fatalf("show json: %v\n%s", err, out)
	}
	if sj.Item == nil || sj.Item.Prompt != "prompt 1" || sj.Item.Status != "completed" {
		t.Fatalf("show item=%+v", sj.Item)
	}

	code, out, stderr = runCLI(t, []string{"status", "--root", root, "--json"}, "")
	if code != 0 {
		t.Fatalf("status exit=%d stderr=%s", code, stderr)
	}
	var st statusJSON
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("status json: %v\n%s", err, out)
	}
	if !st.Exists || st.Sessions != 1 || st.RetainedTurns != 2 || st.ActiveTurns != 0 {
		t.Fatalf("status=%+v", st)
	}

	code, out, stderr = runCLI(t, []string{"clean", "--root", root, "--dry-run"}, "")
	if code != 0 {
		t.Fatalf("clean exit=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(out, "Nothing to clean") && !strings.Contains(out, "No changes made") {
		t.Fatalf("clean output=%s", out)
	}

	code, out, stderr = runCLI(t, []string{"gc", "--root", root, "--dry-run"}, "")
	if code != 0 {
		t.Fatalf("gc exit=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(out, "GC plan") || !strings.Contains(out, "No changes made") {
		t.Fatalf("gc output=%s", out)
	}
}

func TestCLINoDataAndUnknownCommand(t *testing.T) {
	dir := t.TempDir()
	code, out, _ := runCLI(t, []string{"list", "--root", dir}, "")
	if code != 0 || !strings.Contains(out, "No rewind data") {
		t.Fatalf("list no data: code=%d out=%s", code, out)
	}
	code, _, stderr := runCLI(t, []string{"definitely-not-a-command"}, "")
	if code != 2 || !strings.Contains(stderr, "unknown command") {
		t.Fatalf("unknown: code=%d stderr=%s", code, stderr)
	}
	code, _, stderr = runCLI(t, []string{"rewind", "--last", "--seq", "2", "--root", dir}, "")
	if code != 1 || !strings.Contains(stderr, "--last cannot") {
		t.Fatalf("invalid rewind: code=%d stderr=%s", code, stderr)
	}
}

func TestCLIHookFailOpen(t *testing.T) {
	root := testutil.TempRepo(t)
	code, _, stderr := runCLI(t, []string{"hook", "--root", root}, "{not json")
	if code != 0 || !strings.Contains(stderr, "cxr hook") {
		t.Fatalf("invalid json: code=%d stderr=%s", code, stderr)
	}
	code, _, stderr = runCLI(t, []string{"hook", "--root", root}, hookPayload(t, root, map[string]any{"hook_event_name": "Unknown"}))
	if code != 0 || !strings.Contains(stderr, "unsupported hook_event_name") {
		t.Fatalf("unsupported event: code=%d stderr=%s", code, stderr)
	}
}

func TestCLIInstallAndUninstallRepo(t *testing.T) {
	root := testutil.TempRepo(t)
	code, out, stderr := runCLI(t, []string{"install", "--repo", "--root", root}, "")
	if code != 0 {
		t.Fatalf("install exit=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(out, "Installed") || !strings.Contains(out, "Hook events: 6") {
		t.Fatalf("install output=%s", out)
	}
	if !strings.Contains(out, "git clean -fdx") {
		t.Fatalf("install did not warn about data-directory deletion: %s", out)
	}
	path := filepath.Join(root, ".codex", "hooks.json")
	d, err := hooks.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Events()) != len(hooks.DefaultEvents) {
		t.Fatalf("events=%v", d.Events())
	}
	if !testutil.Exists(root, ".codex-rewind/config.json") {
		t.Fatal("repo config not created")
	}
	code, out, stderr = runCLI(t, []string{"install", "--repo", "--root", root}, "")
	if code != 0 || !strings.Contains(out, "0 newly added") {
		t.Fatalf("second install: code=%d out=%s stderr=%s", code, out, stderr)
	}

	// A hooks file authored with the real binary name is removable.
	custom := filepath.Join(root, ".codex", "hooks.json")
	if err := os.WriteFile(custom, []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/opt/cxr hook","timeout":60},{"type":"command","command":"/bin/echo","timeout":5}]}]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr = runCLI(t, []string{"uninstall", "--repo", "--root", root}, "")
	if code != 0 {
		t.Fatalf("uninstall exit=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(out, "Removed 1") {
		t.Fatalf("uninstall output=%s", out)
	}
	data, err := os.ReadFile(custom)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "/opt/cxr hook") || !strings.Contains(string(data), "/bin/echo") {
		t.Fatalf("uninstall result=%s", data)
	}
}

func TestCLIInstallUserWarnsAboutDataDeletion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	code, out, stderr := runCLI(t, []string{"install", "--user"}, "")
	if code != 0 {
		t.Fatalf("install --user exit=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(out, "git clean -fdx") {
		t.Fatalf("install --user did not warn about data-directory deletion: %s", out)
	}
	if _, err := hooks.Load(filepath.Join(home, "hooks.json")); err != nil {
		t.Fatalf("user hooks not written: %v", err)
	}
}

func TestCLIDataDirIsSelfIgnored(t *testing.T) {
	root := testutil.TempRepo(t)
	cliStart(t, root)
	cliTurn(t, root, 1)
	status := testutil.Git(t, root, "status", "--porcelain")
	if strings.Contains(status, ".codex-rewind") {
		t.Fatalf(".codex-rewind appears in git status:\n%s", status)
	}
	ignore, err := os.ReadFile(filepath.Join(root, ".codex-rewind", ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(ignore)) != "*" {
		t.Fatalf(".codex-rewind/.gitignore=%q", ignore)
	}
	for _, rel := range []string{"config.json", "turn", "objects", "states", "rewind", "logs", "tmp"} {
		if !testutil.Exists(root, filepath.Join(".codex-rewind", rel)) {
			t.Fatalf("missing data path %s", rel)
		}
	}
}

func TestCLIVersionAndUsage(t *testing.T) {
	code, out, _ := runCLI(t, []string{"version"}, "")
	if code != 0 || !strings.HasPrefix(out, "cxr ") {
		t.Fatalf("version: code=%d out=%s", code, out)
	}
	code, out, _ = runCLI(t, nil, "")
	if code != 0 || !strings.Contains(out, "Usage:") {
		t.Fatalf("usage: code=%d out=%s", code, out)
	}
}
