//go:build unix

package appserver

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const fakeServer = `#!/bin/sh
if [ -n "$FAKE_PID_FILE" ]; then printf '%s\n' "$$" > "$FAKE_PID_FILE"; fi
while IFS= read -r line; do
  if [ -n "$FAKE_LOG" ]; then printf '%s\n' "$line" >> "$FAKE_LOG"; fi
  id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9][0-9]*\).*/\1/p')
  case "$line" in
    *initialize*)
      printf '{"id":%s,"result":{"userAgent":"fake/0.164.0","codexHome":"/tmp/fake","platformOs":"test"}}\n' "$id"
      ;;
    *thread/fork*)
      printf '{"id":%s,"result":{"thread":{"id":"new-1","forkedFromId":"old"}}}\n' "$id"
      ;;
    *thread/turns/list*)
      printf '{"id":%s,"result":{"data":[{"id":"t1","status":"completed","items":[{"type":"userMessage","content":[{"type":"text","text":"hi there"}]},{"type":"agentMessage","text":"hello"}]}]}}\n' "$id"
      ;;
    *thread/list*)
      printf '{"id":%s,"result":{"data":[{"id":"s1","cwd":"/tmp"}]}}\n' "$id"
      ;;
    *)
      printf '{"id":%s,"result":{}}\n' "$id"
      ;;
  esac
done
`

const fakeTimeoutServer = `#!/bin/sh
if [ -n "$FAKE_PID_FILE" ]; then printf '%s\n' "$$" > "$FAKE_PID_FILE"; fi
sleep 30
`

func writeScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-codex.sh")
	if err := os.WriteFile(path, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	return path
}

func fakeEnv(pidFile, logFile string) []string {
	env := append(os.Environ(), "FAKE_PID_FILE="+pidFile)
	if logFile != "" {
		env = append(env, "FAKE_LOG="+logFile)
	}
	return env
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, perr := strconv.Atoi(strings.TrimSpace(string(data)))
			if perr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("fake server did not write pid file %s", path)
	return 0
}

func processGone(pid int) bool {
	err := syscall.Kill(pid, 0)
	return errors.Is(err, syscall.ESRCH)
}

func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if processGone(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("app-server process %d still exists", pid)
}

func TestClientProtocolAndCleanup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	logFile := filepath.Join(dir, "requests.log")
	script := writeScript(t, fakeServer)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := Start(ctx, Options{Binary: script, Env: fakeEnv(pidFile, logFile), Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if c.InitializeResult() == nil || c.InitializeResult().UserAgent != "fake/0.164.0" {
		t.Fatalf("init=%+v", c.InitializeResult())
	}
	list, err := c.ThreadList(ctx, ThreadListParams{})
	if err != nil || len(list.Data) != 1 || list.Data[0].ID != "s1" {
		t.Fatalf("thread/list: %+v %v", list, err)
	}
	turns, err := c.ThreadTurnsList(ctx, ThreadTurnsListParams{ThreadID: "s1"})
	if err != nil || len(turns.Data) != 1 {
		t.Fatalf("turns: %+v %v", turns, err)
	}
	if got := turns.Data[0].FirstUserText(); got != "hi there" {
		t.Fatalf("user text=%q", got)
	}
	if got := turns.Data[0].LastAgentText(); got != "hello" {
		t.Fatalf("agent text=%q", got)
	}
	fork, err := c.ThreadFork(ctx, ThreadForkParams{ThreadID: "old", BeforeTurnID: "t1"})
	if err != nil || fork.Thread.ID != "new-1" {
		t.Fatalf("fork: %+v %v", fork, err)
	}
	pid := readPID(t, pidFile)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	waitGone(t, pid)
	if _, err := c.ThreadList(ctx, ThreadListParams{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("use after close err=%v", err)
	}
	requests, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(requests), `"experimentalApi":true`) {
		t.Fatalf("initialize did not declare experimentalApi: %s", requests)
	}
	if !strings.Contains(string(requests), `"clientInfo"`) {
		t.Fatalf("initialize did not send clientInfo: %s", requests)
	}
	if !strings.Contains(string(requests), `"beforeTurnId":"t1"`) {
		t.Fatalf("fork did not send beforeTurnId: %s", requests)
	}
}

func TestStartTimeoutKillsProcess(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	script := writeScript(t, fakeTimeoutServer)
	started := time.Now()
	_, err := Start(context.Background(), Options{Binary: script, Env: fakeEnv(pidFile, ""), Timeout: 5 * time.Second})
	if err == nil {
		t.Fatal("expected initialize timeout")
	}
	if time.Since(started) > 15*time.Second {
		t.Fatalf("timeout took too long: %s", time.Since(started))
	}
	pid := readPID(t, pidFile)
	waitGone(t, pid)
}

func TestForkHelper(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	logFile := filepath.Join(dir, "requests.log")
	script := writeScript(t, fakeServer)
	id, err := Fork(context.Background(), Options{Binary: script, Env: fakeEnv(pidFile, logFile), Timeout: 10 * time.Second}, ThreadForkParams{
		ThreadID: "old", LastTurnID: "t9",
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != "new-1" {
		t.Fatalf("id=%s", id)
	}
	requests, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(requests), `"lastTurnId":"t9"`) {
		t.Fatalf("through fork did not send lastTurnId: %s", requests)
	}
	if strings.Contains(string(requests), `"beforeTurnId"`) {
		t.Fatalf("through fork sent beforeTurnId: %s", requests)
	}
	pid := readPID(t, pidFile)
	waitGone(t, pid)
}

func TestStartMissingBinary(t *testing.T) {
	_, err := Start(context.Background(), Options{Binary: filepath.Join(t.TempDir(), "nope"), Timeout: time.Second})
	if err == nil {
		t.Fatal("missing binary accepted")
	}
}

func TestDetectCapabilities(t *testing.T) {
	script := writeScript(t, `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "codex-cli 0.164.0"
  exit 0
fi
if [ "$1" = "app-server" ] && [ "$2" = "generate-json-schema" ]; then
  shift 2
  out=""
  while [ $# -gt 0 ]; do
    if [ "$1" = "--out" ]; then out="$2"; shift 2; else shift; fi
  done
  mkdir -p "$out"
  printf '{"properties":{"lastTurnId":{"type":"string"}}}' > "$out/ThreadForkParams.json"
  printf '{"properties":{"beforeTurnId":{"type":"string"}}}' > "$out/ThreadRevertParams.json"
  exit 0
fi
exit 1
`)
	caps, err := Detect(context.Background(), Options{Binary: script, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if caps.Version != "0.164.0" || !caps.SchemaAvailable {
		t.Fatalf("caps=%+v", caps)
	}
	if !caps.SupportsBeforeTurnID || !caps.SupportsLastTurnID || !caps.SupportsRevert {
		t.Fatalf("capabilities not detected: %+v", caps)
	}
	foundNote := false
	for _, n := range caps.Notes {
		if strings.Contains(n, "beforeTurnId") {
			foundNote = true
		}
	}
	if !foundNote {
		t.Fatalf("missing fallback note: %v", caps.Notes)
	}
}

func TestDetectOldVersionUnsupported(t *testing.T) {
	script := writeScript(t, `#!/bin/sh
if [ "$1" = "--version" ]; then echo "codex-cli 0.100.0"; exit 0; fi
if [ "$1" = "app-server" ] && [ "$2" = "generate-json-schema" ]; then
  shift 2
  out=""
  while [ $# -gt 0 ]; do if [ "$1" = "--out" ]; then out="$2"; shift 2; else shift; fi; done
  mkdir -p "$out"
  printf '{"properties":{}}' > "$out/ThreadForkParams.json"
  exit 0
fi
exit 1
`)
	caps, err := Detect(context.Background(), Options{Binary: script, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if caps.SupportsBeforeTurnID || caps.SupportsLastTurnID || caps.SupportsRevert {
		t.Fatalf("old version advertised support: %+v", caps)
	}
}

func TestRealCodexHandshakeOptional(t *testing.T) {
	if os.Getenv("CXR_TEST_REAL_CODEX") != "1" {
		t.Skip("set CXR_TEST_REAL_CODEX=1 to probe the installed codex")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex not installed")
	}
	res, err := Handshake(context.Background(), Options{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if res.UserAgent == "" {
		t.Fatal("empty user agent")
	}
}
