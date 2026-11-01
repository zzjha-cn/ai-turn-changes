package appserver

import "testing"

func TestTurnTextExtraction(t *testing.T) {
	turn := Turn{Items: []TurnItem{
		{Type: "userMessage", Content: []UserMessageContent{{Type: "text", Text: "first"}, {Type: "text", Text: "second"}}},
		{Type: "agentMessage", Text: "answer"},
		{Type: "agentMessage", Text: "last answer"},
	}}
	if got := turn.FirstUserText(); got != "first" {
		t.Fatalf("first=%q", got)
	}
	if got := turn.LastAgentText(); got != "last answer" {
		t.Fatalf("last=%q", got)
	}
	if got := (Turn{Items: []TurnItem{{Type: "agentMessage"}}}).FirstUserText(); got != "" {
		t.Fatalf("empty=%q", got)
	}
}

func TestRPCErrorString(t *testing.T) {
	if got := (&RPCError{Code: -1, Message: "boom"}).Error(); got != "boom" {
		t.Fatalf("got %q", got)
	}
	var e *RPCError
	if got := e.Error(); got == "" {
		t.Fatal("nil RPCError has empty message")
	}
}
