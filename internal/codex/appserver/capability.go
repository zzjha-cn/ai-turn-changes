package appserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Capabilities records the app-server features rewind relies on.
type Capabilities struct {
	Version              string   `json:"version"`
	SupportsBeforeTurnID bool     `json:"supportsBeforeTurnId"`
	SupportsLastTurnID   bool     `json:"supportsLastTurnId"`
	SupportsRevert       bool     `json:"supportsRevert"`
	SchemaAvailable      bool     `json:"schemaAvailable"`
	Notes                []string `json:"notes"`
}

var semverRe = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// Detect inspects the installed Codex CLI version and generated app-server
// schema. It never creates a thread.
func Detect(ctx context.Context, opts Options) (*Capabilities, error) {
	bin := resolveBinary(opts)
	if ctx == nil {
		ctx = context.Background()
	}
	versionOut, err := runCommand(ctx, bin, nil, opts.Env, "--version")
	if err != nil {
		return nil, fmt.Errorf("codex --version: %w", err)
	}
	caps := &Capabilities{Version: parseVersion(string(versionOut))}
	if caps.Version == "" {
		caps.Notes = append(caps.Notes, "could not parse codex version from "+strings.TrimSpace(string(versionOut)))
	}

	tmp, err := os.MkdirTemp("", "cxr-schema-*")
	if err == nil {
		defer os.RemoveAll(tmp)
		if _, err := runCommand(ctx, bin, nil, opts.Env, "app-server", "generate-json-schema", "--out", tmp); err == nil {
			caps.SchemaAvailable = true
			if hasJSONKey(filepath.Join(tmp, "ThreadForkParams.json"), "beforeTurnId") {
				caps.SupportsBeforeTurnID = true
			}
			if hasJSONKey(filepath.Join(tmp, "ThreadForkParams.json"), "lastTurnId") {
				caps.SupportsLastTurnID = true
			}
			if hasJSONKey(filepath.Join(tmp, "ThreadRevertParams.json"), "beforeTurnId") {
				caps.SupportsRevert = true
			}
		} else {
			caps.Notes = append(caps.Notes, "could not generate app-server JSON schema")
		}
	}

	// Codex 0.164.0 accepts thread/fork.beforeTurnId although the generated
	// schema omits it. Fall back to the empirically verified version floor.
	if !caps.SupportsBeforeTurnID && atLeast(caps.Version, 0, 164, 0) {
		caps.SupportsBeforeTurnID = true
		caps.Notes = append(caps.Notes, "beforeTurnId not in fork schema; enabled by verified 0.164.0+ fallback")
	}
	if !caps.SupportsLastTurnID && atLeast(caps.Version, 0, 164, 0) {
		caps.SupportsLastTurnID = true
		caps.Notes = append(caps.Notes, "lastTurnId enabled by version fallback")
	}
	if !caps.SupportsRevert && atLeast(caps.Version, 0, 164, 0) {
		caps.SupportsRevert = true
	}
	if !caps.SupportsBeforeTurnID {
		caps.Notes = append(caps.Notes, "thread/fork.beforeTurnId is unavailable; --before rewinds cannot fork")
	}
	return caps, nil
}

func resolveBinary(opts Options) string {
	if opts.Binary != "" {
		return opts.Binary
	}
	if v := os.Getenv("CXR_CODEX_BIN"); v != "" {
		return v
	}
	return "codex"
}

func runCommand(ctx context.Context, bin string, dir *string, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	if dir != nil {
		cmd.Dir = *dir
	}
	if env != nil {
		cmd.Env = env
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return nil, fmt.Errorf("%w: %s", err, msg)
		}
		return nil, err
	}
	return out, nil
}

func parseVersion(out string) string {
	m := semverRe.FindStringSubmatch(out)
	if m == nil {
		return ""
	}
	return m[1] + "." + m[2] + "." + m[3]
}

func atLeast(version string, major, minor, patch int) bool {
	if version == "" {
		return false
	}
	m := semverRe.FindStringSubmatch(version)
	if m == nil {
		return false
	}
	v := make([]int, 3)
	for i := 0; i < 3; i++ {
		v[i], _ = strconv.Atoi(m[i+1])
	}
	have := []int{major, minor, patch}
	for i := 0; i < 3; i++ {
		if v[i] > have[i] {
			return true
		}
		if v[i] < have[i] {
			return false
		}
	}
	return true
}

func hasJSONKey(path, key string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return strings.Contains(string(data), `"`+key+`"`)
	}
	return containsKey(v, key)
}

func containsKey(v any, key string) bool {
	switch t := v.(type) {
	case map[string]any:
		if _, ok := t[key]; ok {
			return true
		}
		for _, x := range t {
			if containsKey(x, key) {
				return true
			}
		}
	case []any:
		for _, x := range t {
			if containsKey(x, key) {
				return true
			}
		}
	}
	return false
}

// ForkOutcome reports a forked thread and whether it could be named.
type ForkOutcome struct {
	ThreadID string
	Named    bool
	Warnings []string
}

// Fork performs one thread fork through a temporary app-server and closes it.
func Fork(ctx context.Context, opts Options, params ThreadForkParams) (string, error) {
	out, err := ForkWithName(ctx, opts, params, "")
	if err != nil {
		return "", err
	}
	return out.ThreadID, nil
}

// ForkWithName forks a thread and, when name is non-empty, names it in the same
// app-server process. Naming is best-effort: a failed name call is returned as a
// warning so the fork and subsequent file restore can still succeed.
func ForkWithName(ctx context.Context, opts Options, params ThreadForkParams, name string) (ForkOutcome, error) {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c, err := Start(callCtx, opts)
	if err != nil {
		return ForkOutcome{}, err
	}
	defer c.Close()
	res, err := c.ThreadFork(callCtx, params)
	if err != nil {
		return ForkOutcome{}, err
	}
	if res == nil || res.Thread.ID == "" {
		return ForkOutcome{}, fmt.Errorf("thread/fork returned no thread id")
	}
	out := ForkOutcome{ThreadID: res.Thread.ID}
	if name != "" {
		if nerr := c.ThreadSetName(callCtx, res.Thread.ID, name); nerr != nil {
			out.Warnings = append(out.Warnings, "thread/name/set failed: "+nerr.Error())
		} else {
			out.Named = true
		}
	}
	return out, nil
}

// SetThreadName names an existing thread through a temporary app-server.
func SetThreadName(ctx context.Context, opts Options, threadID, name string) error {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c, err := Start(callCtx, opts)
	if err != nil {
		return err
	}
	defer c.Close()
	return c.ThreadSetName(callCtx, threadID, name)
}
