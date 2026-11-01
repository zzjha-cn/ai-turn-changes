package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zzjha-cn/codex-rewind/internal/display"
)

const TurnSchemaVersion = 1

// EventSchemaVersion versions each line of events/<seq>-<turnId>.jsonl.
const EventSchemaVersion = 1

// Turn statuses.
const (
	TurnRunning     = "running"
	TurnCompleted   = "completed"
	TurnInterrupted = "interrupted"
	TurnFailed      = "failed"
)

// FileChange describes one path changed by a turn.
type FileChange struct {
	Path        string `json:"path"`
	Status      string `json:"status"`
	PreviousOID string `json:"previousOid"`
	CurrentOID  string `json:"currentOid"`
	Insertions  int    `json:"insertions"`
	Deletions   int    `json:"deletions"`
	IsBinary    bool   `json:"isBinary"`
}

// TurnItem mirrors items/<seq>-<turnId>.json.
type TurnItem struct {
	SchemaVersion        int          `json:"schemaVersion"`
	TurnID               string       `json:"turnId"`
	Seq                  int64        `json:"seq"`
	ParentTurnID         *string      `json:"parentTurnId"`
	Status               string       `json:"status"`
	Prompt               string       `json:"prompt"`
	LastAssistantMessage string       `json:"lastAssistantMessage"`
	StartedAt            time.Time    `json:"startedAt"`
	EndedAt              *time.Time   `json:"endedAt"`
	BaselineStateID      string       `json:"baselineStateId"`
	FinalStateID         string       `json:"finalStateId"`
	Changes              []FileChange `json:"changes"`
	Error                *string      `json:"error"`
	NeedsFinalize        bool         `json:"needsFinalize"`
	Unstable             bool         `json:"unstable,omitempty"`
}

// IsTerminal reports whether the turn no longer occupies the active slot.
func (t *TurnItem) IsTerminal() bool { return t.Status != TurnRunning }

func (t *TurnItem) Display(limits display.Limits) display.Display {
	changes := make([]display.Change, 0, len(t.Changes))
	for _, c := range t.Changes {
		changes = append(changes, display.Change{
			Path: c.Path, Status: c.Status, Insertions: c.Insertions, Deletions: c.Deletions, IsBinary: c.IsBinary,
		})
	}
	return display.Build(t.Prompt, t.LastAssistantMessage, changes, limits)
}

func TurnItemPath(l *Layout, sessionID string, seq int64, turnID string) (string, error) {
	rel, err := ItemRelPath(seq, turnID)
	if err != nil {
		return "", err
	}
	dir, err := l.SessionDir(sessionID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, filepath.FromSlash(rel)), nil
}

func LoadTurnItem(l *Layout, sessionID string, seq int64, turnID string) (*TurnItem, error) {
	path, err := TurnItemPath(l, sessionID, seq, turnID)
	if err != nil {
		return nil, err
	}
	var it TurnItem
	if err := ReadJSON(path, &it); err != nil {
		return nil, err
	}
	if it.SchemaVersion != TurnSchemaVersion {
		return nil, fmt.Errorf("turn %s: unsupported schemaVersion %d", turnID, it.SchemaVersion)
	}
	if it.Seq != seq || it.TurnID != turnID {
		return nil, fmt.Errorf("turn %s: item identity mismatch (seq %d, turn %s)", turnID, it.Seq, it.TurnID)
	}
	if it.Changes == nil {
		it.Changes = []FileChange{}
	}
	return &it, nil
}

func SaveTurnItem(l *Layout, sessionID string, it *TurnItem) error {
	if it == nil {
		return fmt.Errorf("nil turn item")
	}
	if it.SchemaVersion == 0 {
		it.SchemaVersion = TurnSchemaVersion
	}
	if it.Changes == nil {
		it.Changes = []FileChange{}
	}
	if len(it.Changes) == 0 {
		it.Changes = []FileChange{}
	}
	path, err := TurnItemPath(l, sessionID, it.Seq, it.TurnID)
	if err != nil {
		return err
	}
	return WriteJSON(path, it, 0600)
}

// ListTurnItems scans items/ and returns valid items by seq, plus errors for
// unreadable files.
func ListTurnItems(l *Layout, sessionID string) ([]*TurnItem, []error) {
	dir, err := l.ItemsDir(sessionID)
	if err != nil {
		return nil, []error{err}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []error{err}
	}
	var items []*TurnItem
	var errs []error
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		var it TurnItem
		path := filepath.Join(dir, e.Name())
		if err := ReadJSON(path, &it); err != nil {
			errs = append(errs, err)
			continue
		}
		if it.SchemaVersion != TurnSchemaVersion || it.TurnID == "" {
			errs = append(errs, fmt.Errorf("%s: invalid turn item", path))
			continue
		}
		if it.Changes == nil {
			it.Changes = []FileChange{}
		}
		items = append(items, &it)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Seq == items[j].Seq {
			return items[i].TurnID < items[j].TurnID
		}
		return items[i].Seq < items[j].Seq
	})
	return items, errs
}

// Event is one bounded turn event log record.
type Event struct {
	SchemaVersion int       `json:"schemaVersion"`
	At            time.Time `json:"at"`
	Event         string    `json:"event"`
	Tool          string    `json:"tool,omitempty"`
	Paths         []string  `json:"paths"`
}

// EventPath returns the jsonl path for a turn.
func EventPath(l *Layout, sessionID string, seq int64, turnID string) (string, error) {
	rel, err := EventRelPath(seq, turnID)
	if err != nil {
		return "", err
	}
	dir, err := l.SessionDir(sessionID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, filepath.FromSlash(rel)), nil
}

// AppendEvent appends ev while keeping the log bounded. The oldest events are
// dropped once either maxEvents or maxBytes would be exceeded.
func AppendEvent(l *Layout, sessionID string, seq int64, turnID string, ev Event, maxEvents, maxBytes int) error {
	path, err := EventPath(l, sessionID, seq, turnID)
	if err != nil {
		return err
	}
	if ev.Paths == nil {
		ev.Paths = []string{}
	}
	ev.SchemaVersion = EventSchemaVersion
	if len(ev.Paths) > 50 {
		ev.Paths = ev.Paths[:50]
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	lines := [][]byte{}
	if data, err := os.ReadFile(path); err == nil {
		sc := bufio.NewScanner(bytes.NewReader(data))
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			if len(sc.Bytes()) == 0 {
				continue
			}
			cp := append([]byte(nil), sc.Bytes()...)
			lines = append(lines, cp)
		}
		if err := sc.Err(); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	lines = append(lines, line)
	if maxEvents <= 0 {
		maxEvents = 200
	}
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	total := 0
	for _, l := range lines {
		total += len(l) + 1
	}
	for len(lines) > 1 && (len(lines) > maxEvents || total > maxBytes) {
		total -= len(lines[0]) + 1
		lines = lines[1:]
	}
	var buf bytes.Buffer
	for _, l := range lines {
		buf.Write(l)
		buf.WriteByte('\n')
	}
	return WriteFileAtomic(path, buf.Bytes(), 0600)
}

// ReadEvents decodes all events in a turn log.
func ReadEvents(l *Layout, sessionID string, seq int64, turnID string) ([]Event, error) {
	path, err := EventPath(l, sessionID, seq, turnID)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var ev Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			continue
		}
		out = append(out, ev)
	}
	return out, sc.Err()
}
