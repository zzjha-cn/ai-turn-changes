// Package hooks reads and updates Codex hooks.json files without discarding
// unrelated user configuration.
package hooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zzjha-cn/codex-rewind/internal/fsutil"
)

// EventSpec is one hook event and its timeout in seconds.
type EventSpec struct {
	Event   string
	Timeout int
}

// DefaultEvents is the installation matrix from the design document.
var DefaultEvents = []EventSpec{
	{Event: "SessionStart", Timeout: 60},
	{Event: "UserPromptSubmit", Timeout: 60},
	{Event: "PostToolUse", Timeout: 60},
	{Event: "Stop", Timeout: 60},
	{Event: "Interrupt", Timeout: 3},
	{Event: "SessionEnd", Timeout: 60},
}

// Document is a hooks.json file. Unknown top-level keys, groups and handler
// fields are preserved verbatim.
type Document struct {
	path    string
	root    map[string]json.RawMessage
	hooks   map[string][]json.RawMessage
	Existed bool
}

// New returns an empty document.
func New() *Document {
	return &Document{root: map[string]json.RawMessage{}, hooks: map[string][]json.RawMessage{}}
}

// Load reads path. A missing file yields an empty document.
func Load(path string) (*Document, error) {
	d := New()
	d.path = path
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return d, nil
		}
		return nil, err
	}
	d.Existed = true
	if err := json.Unmarshal(data, &d.root); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if d.root == nil {
		d.root = map[string]json.RawMessage{}
	}
	if raw, ok := d.root["hooks"]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &d.hooks); err != nil {
			return nil, fmt.Errorf("parse %s hooks: %w", path, err)
		}
	}
	if d.hooks == nil {
		d.hooks = map[string][]json.RawMessage{}
	}
	return d, nil
}

// Events returns the hook event names present in the document.
func (d *Document) Events() []string {
	out := make([]string, 0, len(d.hooks))
	for k := range d.hooks {
		out = append(out, k)
	}
	return out
}

// HasCxrCommand reports whether any handler invokes `cxr hook`.
func (d *Document) HasCxrCommand() bool {
	for _, groups := range d.hooks {
		for _, raw := range groups {
			for _, command := range handlerCommands(raw) {
				if IsCxrCommand(command) {
					return true
				}
			}
		}
	}
	return false
}

// AddHandler appends a command hook to event unless an identical command is
// already present in that event.
func (d *Document) AddHandler(event string, handler map[string]any) error {
	command, _ := handler["command"].(string)
	for _, raw := range d.hooks[event] {
		for _, c := range handlerCommands(raw) {
			if c == command {
				return nil
			}
		}
	}
	group := map[string]any{"hooks": []any{handler}}
	raw, err := json.Marshal(group)
	if err != nil {
		return err
	}
	d.hooks[event] = append(d.hooks[event], raw)
	return nil
}

// RemoveCxrHandlers removes every handler that invokes `cxr hook`. It returns
// the number of removed handlers.
func (d *Document) RemoveCxrHandlers() (int, error) {
	removed := 0
	for event, groups := range d.hooks {
		var keptGroups []json.RawMessage
		for _, raw := range groups {
			var group map[string]any
			if err := json.Unmarshal(raw, &group); err != nil {
				keptGroups = append(keptGroups, raw)
				continue
			}
			hooksAny, ok := group["hooks"].([]any)
			if !ok {
				keptGroups = append(keptGroups, raw)
				continue
			}
			var keptHooks []any
			for _, h := range hooksAny {
				hm, ok := h.(map[string]any)
				if !ok {
					keptHooks = append(keptHooks, h)
					continue
				}
				cmd, _ := hm["command"].(string)
				if IsCxrCommand(cmd) {
					removed++
					continue
				}
				keptHooks = append(keptHooks, h)
			}
			if len(keptHooks) == 0 {
				continue
			}
			group["hooks"] = keptHooks
			newRaw, err := json.Marshal(group)
			if err != nil {
				return removed, err
			}
			keptGroups = append(keptGroups, newRaw)
		}
		if len(keptGroups) == 0 {
			delete(d.hooks, event)
		} else {
			d.hooks[event] = keptGroups
		}
	}
	return removed, nil
}

// Save writes the document atomically.
func (d *Document) Save() error {
	if d.path == "" {
		return errors.New("hooks document has no path")
	}
	raw, err := json.Marshal(d.hooks)
	if err != nil {
		return err
	}
	d.root["hooks"] = raw
	data, err := json.MarshalIndent(d.root, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return fsutil.WriteFileAtomic(d.path, data, 0600)
}

// Command returns the hook command for an absolute binary path.
func Command(binary string) string {
	return filepath.Clean(binary) + " hook"
}

// IsCxrCommand reports whether command invokes a cxr hook.
func IsCxrCommand(command string) bool {
	c := strings.TrimSpace(command)
	if !strings.HasSuffix(c, " hook") {
		return false
	}
	exe := strings.TrimSpace(strings.TrimSuffix(c, " hook"))
	exe = strings.Trim(exe, `"'`)
	base := filepath.Base(exe)
	return base == "cxr" || base == "cxr.exe"
}

func handlerCommands(groupRaw json.RawMessage) []string {
	var group map[string]any
	if json.Unmarshal(groupRaw, &group) != nil {
		return nil
	}
	hooksAny, ok := group["hooks"].([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, h := range hooksAny {
		if hm, ok := h.(map[string]any); ok {
			if cmd, ok := hm["command"].(string); ok {
				out = append(out, cmd)
			}
		}
	}
	return out
}

// Install adds `binary hook` to each default event at path.
func Install(path, binary string) (int, error) {
	d, err := Load(path)
	if err != nil {
		return 0, err
	}
	command := Command(binary)
	added := 0
	for _, spec := range DefaultEvents {
		before := len(d.hooks[spec.Event])
		if err := d.AddHandler(spec.Event, map[string]any{
			"type":    "command",
			"command": command,
			"timeout": spec.Timeout,
		}); err != nil {
			return added, err
		}
		if len(d.hooks[spec.Event]) > before {
			added++
		}
	}
	if err := d.Save(); err != nil {
		return added, err
	}
	return added, nil
}

// Uninstall removes `cxr hook` handlers from path. It is a no-op when the file
// does not exist.
func Uninstall(path string) (int, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	d, err := Load(path)
	if err != nil {
		return 0, err
	}
	removed, err := d.RemoveCxrHandlers()
	if err != nil {
		return removed, err
	}
	return removed, d.Save()
}
