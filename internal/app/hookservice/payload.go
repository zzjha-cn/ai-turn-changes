// Package hookservice handles Codex lifecycle hook payloads.
package hookservice

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const maxPayloadBytes = 8 << 20

// Hook event names.
const (
	EventSessionStart     = "SessionStart"
	EventUserPromptSubmit = "UserPromptSubmit"
	EventPreToolUse       = "PreToolUse"
	EventPostToolUse      = "PostToolUse"
	EventStop             = "Stop"
	EventInterrupt        = "Interrupt"
	EventSessionEnd       = "SessionEnd"
)

// Payload is the JSON object Codex writes to a hook's stdin. Unknown fields are
// ignored. Both prompt and user_prompt are accepted.
type Payload struct {
	SessionID            string          `json:"session_id"`
	ThreadID             string          `json:"thread_id"`
	TurnID               string          `json:"turn_id"`
	TranscriptPath       string          `json:"transcript_path"`
	Cwd                  string          `json:"cwd"`
	HookEventName        string          `json:"hook_event_name"`
	Model                string          `json:"model"`
	PermissionMode       string          `json:"permission_mode"`
	Prompt               string          `json:"prompt"`
	UserPrompt           string          `json:"user_prompt"`
	LastAssistantMessage string          `json:"last_assistant_message"`
	ToolName             string          `json:"tool_name"`
	ToolInput            json.RawMessage `json:"tool_input"`
	ToolResponse         json.RawMessage `json:"tool_response"`
	Source               string          `json:"source"`
	StopHookActive       bool            `json:"stop_hook_active"`
}

// DecodePayload reads and validates a hook payload.
func DecodePayload(r io.Reader) (Payload, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxPayloadBytes+1))
	if err != nil {
		return Payload{}, err
	}
	if len(data) > maxPayloadBytes {
		return Payload{}, fmt.Errorf("hook payload exceeds %d bytes", maxPayloadBytes)
	}
	var p Payload
	if err := json.Unmarshal(data, &p); err != nil {
		return Payload{}, fmt.Errorf("decode hook payload: %w", err)
	}
	p.normalize()
	return p, nil
}

func (p *Payload) normalize() {
	p.HookEventName = strings.TrimSpace(p.HookEventName)
	p.SessionID = strings.TrimSpace(p.SessionID)
	p.ThreadID = strings.TrimSpace(p.ThreadID)
	p.TurnID = strings.TrimSpace(p.TurnID)
	p.Source = strings.TrimSpace(p.Source)
	if p.Prompt == "" {
		p.Prompt = p.UserPrompt
	}
	if p.ThreadID == "" {
		p.ThreadID = p.SessionID
	}
}

// Supported reports whether the hook event is part of the processing matrix.
func (p Payload) Supported() bool {
	switch p.HookEventName {
	case EventSessionStart, EventUserPromptSubmit, EventPreToolUse, EventPostToolUse,
		EventStop, EventInterrupt, EventSessionEnd:
		return true
	}
	return false
}

// PromptText returns the prompt from either supported field.
func (p Payload) PromptText() string {
	if p.Prompt != "" {
		return p.Prompt
	}
	return p.UserPrompt
}
