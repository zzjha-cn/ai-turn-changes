package store

import (
	"fmt"
	"os"
	"sort"
	"time"
)

const SessionSchemaVersion = 1

// Session mirrors turn/<id>/session.json.
type Session struct {
	SchemaVersion       int        `json:"schemaVersion"`
	SessionID           string     `json:"sessionId"`
	ThreadID            string     `json:"threadId"`
	Source              string     `json:"source"`
	Cwd                 string     `json:"cwd"`
	GitRoot             string     `json:"gitRoot"`
	OpenedAt            time.Time  `json:"openedAt"`
	LastActivityAt      time.Time  `json:"lastActivityAt"`
	ClosedAt            *time.Time `json:"closedAt"`
	Title               string     `json:"title"`
	Open                bool       `json:"open"`
	ActiveTurnID        *string    `json:"activeTurnId"`
	ForkedFromSessionID *string    `json:"forkedFromSessionId"`
	ForkedFromTurnID    *string    `json:"forkedFromTurnId"`
}

func NewSession(sessionID, threadID, source, cwd, gitRoot string, now time.Time) *Session {
	if threadID == "" {
		threadID = sessionID
	}
	return &Session{
		SchemaVersion:  SessionSchemaVersion,
		SessionID:      sessionID,
		ThreadID:       threadID,
		Source:         source,
		Cwd:            cwd,
		GitRoot:        gitRoot,
		OpenedAt:       now,
		LastActivityAt: now,
		Open:           true,
	}
}

func LoadSession(l *Layout, sessionID string) (*Session, error) {
	path, err := l.SessionJSONPath(sessionID)
	if err != nil {
		return nil, err
	}
	var s Session
	if err := ReadJSON(path, &s); err != nil {
		return nil, err
	}
	if s.SchemaVersion != SessionSchemaVersion {
		return nil, fmt.Errorf("session %s: unsupported schemaVersion %d", sessionID, s.SchemaVersion)
	}
	if s.SessionID != sessionID {
		return nil, fmt.Errorf("session %s: sessionId mismatch %q", sessionID, s.SessionID)
	}
	if s.ThreadID == "" {
		s.ThreadID = s.SessionID
	}
	return &s, nil
}

func SaveSession(l *Layout, s *Session) error {
	if s == nil {
		return fmt.Errorf("nil session")
	}
	if s.SchemaVersion == 0 {
		s.SchemaVersion = SessionSchemaVersion
	}
	if _, err := l.EnsureSession(s.SessionID); err != nil {
		return err
	}
	path, err := l.SessionJSONPath(s.SessionID)
	if err != nil {
		return err
	}
	return WriteJSON(path, s, 0600)
}

// LoadSessions loads every readable session. Corrupt sessions are returned as
// errors so callers can report them without failing the whole listing.
func LoadSessions(l *Layout) ([]*Session, []error) {
	ids, err := l.SessionIDs()
	if err != nil {
		return nil, []error{err}
	}
	var out []*Session
	var errs []error
	for _, id := range ids {
		s, err := LoadSession(l, id)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].LastActivityAt.After(out[j].LastActivityAt)
	})
	return out, errs
}

// MostRecent returns the session with the newest lastActivityAt.
func MostRecent(l *Layout) (*Session, error) {
	sessions, errs := LoadSessions(l)
	if len(sessions) == 0 {
		if len(errs) > 0 {
			return nil, errs[0]
		}
		return nil, os.ErrNotExist
	}
	return sessions[0], nil
}

func (s *Session) Touch(now time.Time) {
	s.LastActivityAt = now
}

func (s *Session) SetActive(turnID string) {
	if turnID == "" {
		s.ActiveTurnID = nil
		return
	}
	id := turnID
	s.ActiveTurnID = &id
}

func (s *Session) ClearActive() { s.ActiveTurnID = nil }

func (s *Session) Close(now time.Time) {
	s.Open = false
	s.ClosedAt = &now
	s.LastActivityAt = now
	s.ActiveTurnID = nil
}

func (s *Session) IsActive() bool { return s.ActiveTurnID != nil && *s.ActiveTurnID != "" }

// ActiveTurn returns the active turn id or "".
func (s *Session) ActiveTurn() string {
	if s.ActiveTurnID == nil {
		return ""
	}
	return *s.ActiveTurnID
}
