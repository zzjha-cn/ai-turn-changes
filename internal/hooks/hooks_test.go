package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeHooksFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hooks.json")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestInstallPreservesUnrelatedConfiguration(t *testing.T) {
	path := writeHooksFile(t, `{
  "customTopLevel": {"keep": true},
  "hooks": {
    "SessionStart": [
      {"matcher": "startup", "hooks": [
        {"type": "command", "command": "/usr/bin/true", "timeout": 5},
        {"type": "command", "command": "/old/cxr hook", "timeout": 1}
      ]}
    ],
    "Other": [
      {"hooks": [{"type": "command", "command": "/bin/echo", "timeout": 9, "custom": "z"}]}
    ]
  }
}`)
	added, err := Install(path, "/opt/tools/cxr")
	if err != nil {
		t.Fatal(err)
	}
	if added != len(DefaultEvents) {
		t.Fatalf("added=%d want %d", added, len(DefaultEvents))
	}
	d, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !d.HasCxrCommand() {
		t.Fatal("cxr command not detected")
	}
	if len(d.hooks["Interrupt"]) == 0 {
		t.Fatal("Interrupt hook missing")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	if root["customTopLevel"] == nil {
		t.Fatal("custom top-level key was dropped")
	}
	text := string(raw)
	if !strings.Contains(text, `"timeout": 3`) {
		t.Fatal("Interrupt timeout 3 missing")
	}
	if !strings.Contains(text, "/usr/bin/true") {
		t.Fatal("unrelated session hook dropped")
	}
	if !strings.Contains(text, "/opt/tools/cxr hook") {
		t.Fatal("new cxr command missing")
	}

	// Installing again is idempotent and leaves the old cxr entry for
	// uninstall to clean up.
	again, err := Install(path, "/opt/tools/cxr")
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Fatalf("second install added %d entries", again)
	}
}

func TestUninstallRemovesOnlyCxr(t *testing.T) {
	path := writeHooksFile(t, `{
  "hooks": {
    "SessionStart": [
      {"matcher": "startup", "hooks": [
        {"type": "command", "command": "/usr/bin/true", "timeout": 5},
        {"type": "command", "command": "/old/cxr hook", "timeout": 1}
      ]}
    ],
    "Other": [
      {"hooks": [{"type": "command", "command": "/bin/echo", "timeout": 9, "custom": "z"}]}
    ]
  }
}`)
	if _, err := Install(path, "/opt/tools/cxr"); err != nil {
		t.Fatal(err)
	}
	removed, err := Uninstall(path)
	if err != nil {
		t.Fatal(err)
	}
	if removed != len(DefaultEvents)+1 {
		t.Fatalf("removed=%d want %d", removed, len(DefaultEvents)+1)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "cxr hook") {
		t.Fatalf("cxr handler remains: %s", raw)
	}
	if !strings.Contains(string(raw), "/usr/bin/true") || !strings.Contains(string(raw), `"custom": "z"`) {
		t.Fatalf("unrelated hooks not preserved: %s", raw)
	}
	if removed == 0 {
		t.Fatal("nothing removed")
	}
}

func TestUninstallMissingFileIsNoop(t *testing.T) {
	removed, err := Uninstall(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil || removed != 0 {
		t.Fatalf("removed=%d err=%v", removed, err)
	}
}

func TestLoadMalformedFails(t *testing.T) {
	path := writeHooksFile(t, "{broken")
	if _, err := Load(path); err == nil {
		t.Fatal("malformed hooks file accepted")
	}
	if _, err := Install(path, "/opt/cxr"); err == nil {
		t.Fatal("install over malformed hooks file succeeded")
	}
}

func TestIsCxrCommandAndCommand(t *testing.T) {
	yes := []string{"cxr hook", "/usr/local/bin/cxr hook", "  /a/cxr hook  ", "/a/cxr.exe hook"}
	for _, c := range yes {
		if !IsCxrCommand(c) {
			t.Errorf("IsCxrCommand(%q)=false", c)
		}
	}
	no := []string{"", "cxr", "other hook", "cxr hook --root x", "/usr/bin/cxr-hook", "echo cxr hook"}
	for _, c := range no {
		if IsCxrCommand(c) {
			t.Errorf("IsCxrCommand(%q)=true", c)
		}
	}
	if got := Command("/a/b/cxr"); got != "/a/b/cxr hook" {
		t.Fatalf("Command=%q", got)
	}
}

func TestNewDocumentHasNoHooks(t *testing.T) {
	d := New()
	if d.HasCxrCommand() || len(d.Events()) != 0 {
		t.Fatalf("new document not empty: %+v", d)
	}
}
