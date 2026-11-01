// Package appserver is a minimal JSONL client for `codex app-server`, used
// only for turn queries and thread forks.
package appserver

import (
	"encoding/json"
)

// ClientInfo identifies this client during initialize.
type ClientInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

// InitializeCapabilities mirrors the app-server capability object.
type InitializeCapabilities struct {
	ExperimentalAPI bool `json:"experimentalApi"`
}

type InitializeParams struct {
	ClientInfo   ClientInfo              `json:"clientInfo"`
	Capabilities *InitializeCapabilities `json:"capabilities,omitempty"`
}

type InitializeResult struct {
	UserAgent  string `json:"userAgent"`
	CodexHome  string `json:"codexHome"`
	PlatformOS string `json:"platformOs"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string {
	if e == nil {
		return "rpc error"
	}
	return e.Message
}

type rpcMessage struct {
	ID     *int64          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *RPCError       `json:"error"`
}

type rpcRequest struct {
	ID     int64  `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params,omitempty"`
}

// ThreadSourceKind values used by thread/list.
const (
	SourceCLI       = "cli"
	SourceVSCode    = "vscode"
	SourceExec      = "exec"
	SourceAppServer = "appServer"
)

type ThreadListParams struct {
	Limit       int      `json:"limit,omitempty"`
	SourceKinds []string `json:"sourceKinds,omitempty"`
	Cwd         string   `json:"cwd,omitempty"`
	SearchTerm  string   `json:"searchTerm,omitempty"`
}

type Thread struct {
	ID           string `json:"id"`
	SessionID    string `json:"sessionId"`
	ForkedFromID string `json:"forkedFromId"`
	Cwd          string `json:"cwd"`
	Preview      string `json:"preview"`
	Name         string `json:"name"`
	Source       string `json:"source"`
	HistoryMode  string `json:"historyMode"`
	CreatedAt    int64  `json:"createdAt"`
	UpdatedAt    int64  `json:"updatedAt"`
}

type ThreadListResult struct {
	Data []Thread `json:"data"`
}

type ThreadTurnsListParams struct {
	ThreadID      string `json:"threadId"`
	Limit         int    `json:"limit,omitempty"`
	SortDirection string `json:"sortDirection,omitempty"`
	ItemsView     string `json:"itemsView,omitempty"`
	Cursor        string `json:"cursor,omitempty"`
}

type Turn struct {
	ID          string     `json:"id"`
	RootTurnID  string     `json:"rootTurnId"`
	Status      string     `json:"status"`
	StartedAt   int64      `json:"startedAt"`
	CompletedAt *int64     `json:"completedAt"`
	ItemsView   string     `json:"itemsView"`
	Items       []TurnItem `json:"items"`
	Error       any        `json:"error"`
}

type TurnItem struct {
	Type    string               `json:"type"`
	ID      string               `json:"id"`
	Text    string               `json:"text"`
	Content []UserMessageContent `json:"content"`
}

type UserMessageContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type ThreadTurnsListResult struct {
	Data       []Turn `json:"data"`
	NextCursor string `json:"nextCursor"`
}

// ThreadForkParams supports both experimental fork selectors. beforeTurnId is
// accepted by Codex 0.164 although it is not always present in the generated
// schema; capability detection records that.
type ThreadForkParams struct {
	ThreadID     string `json:"threadId"`
	BeforeTurnID string `json:"beforeTurnId,omitempty"`
	LastTurnID   string `json:"lastTurnId,omitempty"`
	ExcludeTurns bool   `json:"excludeTurns,omitempty"`
	Cwd          string `json:"cwd,omitempty"`
	Ephemeral    bool   `json:"ephemeral,omitempty"`
}

type ThreadForkResult struct {
	Thread Thread `json:"thread"`
}

// FirstUserText returns the first user message text in a turn.
func (t Turn) FirstUserText() string {
	for _, it := range t.Items {
		if it.Type != "userMessage" {
			continue
		}
		for _, c := range it.Content {
			if c.Text != "" {
				return c.Text
			}
		}
		if it.Text != "" {
			return it.Text
		}
	}
	return ""
}

// LastAgentText returns the last agent message text in a turn.
func (t Turn) LastAgentText() string {
	for i := len(t.Items) - 1; i >= 0; i-- {
		if t.Items[i].Type == "agentMessage" && t.Items[i].Text != "" {
			return t.Items[i].Text
		}
	}
	return ""
}
