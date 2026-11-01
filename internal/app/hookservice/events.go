package hookservice

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/zzjha-cn/codex-rewind/internal/display"
	"github.com/zzjha-cn/codex-rewind/internal/store"
	"github.com/zzjha-cn/codex-rewind/internal/workspace"
)

// ExtractPaths pulls candidate workspace-relative paths out of a tool input.
func ExtractPaths(toolName string, input json.RawMessage) []string {
	if len(input) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(input, &v); err != nil {
		return nil
	}
	if s, ok := v.(string); ok {
		var inner any
		if json.Unmarshal([]byte(s), &inner) == nil {
			v = inner
		}
	}
	var raw []string
	collectToolPaths(v, &raw)
	if strings.Contains(strings.ToLower(toolName), "apply_patch") {
		raw = append(raw, parsePatchPaths(v)...)
	}
	seen := map[string]struct{}{}
	var out []string
	for _, p := range raw {
		p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
		if p == "" || !workspace.ValidRelPath(p) {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
		if len(out) >= 50 {
			break
		}
	}
	sort.Strings(out)
	return out
}

func collectToolPaths(v any, out *[]string) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			switch strings.ToLower(k) {
			case "path", "file_path", "filepath", "notebook_path", "target_file", "file":
				appendPathValues(val, out)
			case "files", "paths":
				appendPathValues(val, out)
			}
		}
	case []any:
		for _, x := range t {
			collectToolPaths(x, out)
		}
	}
}

func appendPathValues(v any, out *[]string) {
	switch t := v.(type) {
	case string:
		*out = append(*out, t)
	case []any:
		for _, x := range t {
			if s, ok := x.(string); ok {
				*out = append(*out, s)
			}
		}
	}
}

func parsePatchPaths(v any) []string {
	var text string
	switch t := v.(type) {
	case map[string]any:
		for _, key := range []string{"patch", "input", "content"} {
			if s, ok := t[key].(string); ok && strings.Contains(s, "***") {
				text = s
				break
			}
		}
	case string:
		text = t
	}
	if text == "" {
		return nil
	}
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		for _, prefix := range []string{"*** Add File:", "*** Update File:", "*** Delete File:", "*** Move to:", "*** Rename to:"} {
			if strings.HasPrefix(line, prefix) {
				p := strings.TrimSpace(strings.TrimPrefix(line, prefix))
				if p != "" {
					out = append(out, p)
				}
			}
		}
	}
	return out
}

// AppendToolEvent records a bounded event for the turn, if the turn is known.
func AppendToolEvent(l *store.Layout, sessionID, turnID, event, tool string, paths []string, at time.Time, maxEvents, maxBytes int) error {
	if turnID == "" {
		return nil
	}
	q, err := store.LoadQueue(l, sessionID, 0, display.Limits{})
	if err != nil {
		return err
	}
	t := q.Turn(turnID)
	if t == nil {
		return nil
	}
	ev := store.Event{At: at, Event: event, Tool: tool, Paths: paths}
	return store.AppendEvent(l, sessionID, t.Seq, turnID, ev, maxEvents, maxBytes)
}
