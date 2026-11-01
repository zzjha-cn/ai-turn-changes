package store

import (
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/zzjha-cn/codex-rewind/internal/display"
)

const QueueSchemaVersion = 1

// QueueTurn is the human readable entry embedded in queue.json.
type QueueTurn struct {
	TurnID          string          `json:"turnId"`
	Seq             int64           `json:"seq"`
	ShortID         string          `json:"shortId"`
	Status          string          `json:"status"`
	StartedAt       time.Time       `json:"startedAt"`
	EndedAt         *time.Time      `json:"endedAt"`
	Display         display.Display `json:"display"`
	BaselineStateID string          `json:"baselineStateId"`
	FinalStateID    string          `json:"finalStateId"`
	ItemPath        string          `json:"itemPath"`
}

// Queue mirrors queue.json: a sliding window of turns for one session.
type Queue struct {
	SchemaVersion     int         `json:"schemaVersion"`
	SessionID         string      `json:"sessionId"`
	MaxRetainedTurns  int         `json:"maxRetainedTurns"`
	NextSeq           int64       `json:"nextSeq"`
	FirstSeq          int64       `json:"firstSeq"`
	LastSeq           int64       `json:"lastSeq"`
	RetainedTurnCount int         `json:"retainedTurnCount"`
	Turns             []QueueTurn `json:"turns"`
	Rebuilt           bool        `json:"-"`
}

func NewQueue(sessionID string, maxRetained int) *Queue {
	if maxRetained <= 0 {
		maxRetained = 10
	}
	return &Queue{
		SchemaVersion:    QueueSchemaVersion,
		SessionID:        sessionID,
		MaxRetainedTurns: maxRetained,
		NextSeq:          1,
		Turns:            []QueueTurn{},
	}
}

func (q *Queue) Turn(turnID string) *QueueTurn {
	for i := range q.Turns {
		if q.Turns[i].TurnID == turnID {
			return &q.Turns[i]
		}
	}
	return nil
}

func (q *Queue) TurnBySeq(seq int64) *QueueTurn {
	for i := range q.Turns {
		if q.Turns[i].Seq == seq {
			return &q.Turns[i]
		}
	}
	return nil
}

// LastTerminal returns the newest terminal turn, or nil.
func (q *Queue) LastTerminal() *QueueTurn {
	for i := len(q.Turns) - 1; i >= 0; i-- {
		if q.Turns[i].Status != TurnRunning {
			return &q.Turns[i]
		}
	}
	return nil
}

// QueueTurnFromItem copies an item into the queue representation.
func QueueTurnFromItem(it *TurnItem, limits display.Limits) QueueTurn {
	rel, _ := ItemRelPath(it.Seq, it.TurnID)
	return QueueTurn{
		TurnID:          it.TurnID,
		Seq:             it.Seq,
		ShortID:         ShortID(it.TurnID),
		Status:          it.Status,
		StartedAt:       it.StartedAt,
		EndedAt:         it.EndedAt,
		Display:         it.Display(limits),
		BaselineStateID: it.BaselineStateID,
		FinalStateID:    it.FinalStateID,
		ItemPath:        rel,
	}
}

// ShortID is the display-only prefix of a turn id.
func ShortID(turnID string) string {
	if len(turnID) <= 8 {
		return turnID
	}
	return turnID[:8]
}

// Upsert replaces or appends the queue entry for it and sorts by seq.
func (q *Queue) Upsert(it *TurnItem, limits display.Limits) {
	entry := QueueTurnFromItem(it, limits)
	for i := range q.Turns {
		if q.Turns[i].TurnID == it.TurnID {
			q.Turns[i] = entry
			q.recompute()
			return
		}
	}
	q.Turns = append(q.Turns, entry)
	q.recompute()
}

func (q *Queue) RemoveTurn(turnID string) {
	out := q.Turns[:0]
	for _, t := range q.Turns {
		if t.TurnID != turnID {
			out = append(out, t)
		}
	}
	q.Turns = out
	q.recompute()
}

// Trim drops oldest terminal turns until at most max terminal turns remain.
// Running turns never count against the window and are never removed.
func (q *Queue) Trim(max int) []QueueTurn {
	if max <= 0 {
		max = q.MaxRetainedTurns
	}
	if max <= 0 {
		max = 10
	}
	q.MaxRetainedTurns = max
	terminal := 0
	for _, t := range q.Turns {
		if t.Status != TurnRunning {
			terminal++
		}
	}
	var removed []QueueTurn
	for terminal > max {
		idx := -1
		for i := range q.Turns {
			if q.Turns[i].Status == TurnRunning {
				continue
			}
			if idx == -1 || q.Turns[i].Seq < q.Turns[idx].Seq {
				idx = i
			}
		}
		if idx == -1 {
			break
		}
		removed = append(removed, q.Turns[idx])
		q.Turns = append(q.Turns[:idx], q.Turns[idx+1:]...)
		terminal--
	}
	q.recompute()
	return removed
}

func (q *Queue) recompute() {
	if q.Turns == nil {
		q.Turns = []QueueTurn{}
	}
	sort.SliceStable(q.Turns, func(i, j int) bool { return q.Turns[i].Seq < q.Turns[j].Seq })
	q.RetainedTurnCount = 0
	q.FirstSeq, q.LastSeq = 0, 0
	for i, t := range q.Turns {
		if i == 0 {
			q.FirstSeq = t.Seq
		}
		q.LastSeq = t.Seq
		if t.Status != TurnRunning {
			q.RetainedTurnCount++
		}
		if t.Seq >= q.NextSeq {
			q.NextSeq = t.Seq + 1
		}
	}
	if q.NextSeq < 1 {
		q.NextSeq = 1
	}
}

// LoadQueue reads queue.json and rebuilds it from items/ when missing or
// corrupt. A rebuilt queue has Rebuilt=true. The repair is persisted, so callers
// must hold the repository lock.
func LoadQueue(l *Layout, sessionID string, maxRetained int, limits display.Limits) (*Queue, error) {
	return loadQueue(l, sessionID, maxRetained, limits, true)
}

// LoadQueueReadOnly is LoadQueue without persisting a rebuild. Read-only
// commands use it so a corrupt queue is not repaired outside the repo lock.
func LoadQueueReadOnly(l *Layout, sessionID string, maxRetained int, limits display.Limits) (*Queue, error) {
	return loadQueue(l, sessionID, maxRetained, limits, false)
}

func loadQueue(l *Layout, sessionID string, maxRetained int, limits display.Limits, persist bool) (*Queue, error) {
	path, err := l.QueuePath(sessionID)
	if err != nil {
		return nil, err
	}
	var q Queue
	ok, err := ReadJSONIfExists(path, &q)
	if err == nil && ok && q.SchemaVersion == QueueSchemaVersion && q.SessionID == sessionID && validQueue(&q) {
		if maxRetained > 0 {
			q.MaxRetainedTurns = maxRetained
		}
		q.recompute()
		return &q, nil
	}
	rebuilt, rerr := RebuildQueue(l, sessionID, maxRetained, limits)
	if rerr != nil {
		if err != nil {
			return nil, fmt.Errorf("queue unreadable (%v) and rebuild failed: %w", err, rerr)
		}
		return nil, rerr
	}
	rebuilt.Rebuilt = true
	if persist {
		if err := SaveQueue(l, rebuilt); err != nil {
			return nil, err
		}
	}
	return rebuilt, nil
}

func validQueue(q *Queue) bool {
	seenSeq := map[int64]bool{}
	seenID := map[string]bool{}
	for _, t := range q.Turns {
		if t.TurnID == "" || t.Seq <= 0 || seenSeq[t.Seq] || seenID[t.TurnID] {
			return false
		}
		seenSeq[t.Seq] = true
		seenID[t.TurnID] = true
	}
	return true
}

func SaveQueue(l *Layout, q *Queue) error {
	if q == nil {
		return fmt.Errorf("nil queue")
	}
	if q.SchemaVersion == 0 {
		q.SchemaVersion = QueueSchemaVersion
	}
	q.recompute()
	path, err := l.QueuePath(q.SessionID)
	if err != nil {
		return err
	}
	return WriteJSON(path, q, 0600)
}

// RebuildQueue scans items/ and reconstructs queue.json from authoritative turn
// items, keeping the newest maxRetained terminal turns.
func RebuildQueue(l *Layout, sessionID string, maxRetained int, limits display.Limits) (*Queue, error) {
	if maxRetained <= 0 {
		maxRetained = 10
	}
	items, errs := ListTurnItems(l, sessionID)
	if len(errs) > 0 {
		return nil, fmt.Errorf("rebuild queue for %s: %d unreadable item(s): %v", sessionID, len(errs), errs[0])
	}
	q := NewQueue(sessionID, maxRetained)
	for _, it := range items {
		if q.NextSeq <= it.Seq {
			q.NextSeq = it.Seq + 1
		}
	}
	var terminal []*TurnItem
	for _, it := range items {
		if it.Status == TurnRunning {
			q.Turns = append(q.Turns, QueueTurnFromItem(it, limits))
		} else {
			terminal = append(terminal, it)
		}
	}
	if len(terminal) > maxRetained {
		terminal = terminal[len(terminal)-maxRetained:]
	}
	for _, it := range terminal {
		q.Turns = append(q.Turns, QueueTurnFromItem(it, limits))
	}
	q.recompute()
	return q, nil
}

// DeleteTurnFiles removes the item and event files for a turn. State/object
// reachability is handled separately by GC.
func DeleteTurnFiles(l *Layout, sessionID string, t QueueTurn) error {
	if path, err := TurnItemPath(l, sessionID, t.Seq, t.TurnID); err == nil {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if path, err := EventPath(l, sessionID, t.Seq, t.TurnID); err == nil {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
