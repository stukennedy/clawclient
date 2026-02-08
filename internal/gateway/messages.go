package gateway

import (
	"encoding/json"
	"time"
)

// Message represents a WebSocket protocol message.
type Message struct {
	Type   string      `json:"type"`              // "req", "res", "event"
	ID     string      `json:"id,omitempty"`      // Request/response ID
	Method string      `json:"method,omitempty"`  // Method name (for req)
	Event  string      `json:"event,omitempty"`   // Event name (for events)
	Params interface{} `json:"params,omitempty"`  // Request params
	Payload interface{} `json:"payload,omitempty"` // Event payload
	Result interface{} `json:"result,omitempty"`  // Response result
	Error  interface{} `json:"error,omitempty"`   // Response error
	OK     *bool       `json:"ok,omitempty"`      // Response success
}

// EventName returns the event name (from Event field) or Method as fallback
func (m *Message) EventName() string {
	if m.Event != "" {
		return m.Event
	}
	return m.Method
}

// Event represents an incoming gateway event.
type Event struct {
	Type string
	Data interface{}
	Raw  *Message
}

// AgentEvent represents a streamed agent response event.
type AgentEvent struct {
	Action  string `json:"action"`  // "start", "chunk", "done", "error"
	Content string `json:"content"` // Text content (for chunk)
	Model   string `json:"model"`   // Model name
	Task    string `json:"task"`    // Task description
	AgentID string `json:"agentId"` // Agent instance ID
	Raw     *Message
}

// ChatMessage represents a message in chat history.
type ChatMessage struct {
	ID        string    `json:"id"`
	Role      string    `json:"role"`      // "user", "assistant", "system"
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
	Context   string    `json:"context"`   // Context/thread name
	Model     string    `json:"model,omitempty"`
	AgentID   string    `json:"agentId,omitempty"`
}

// ChatSendParams are the parameters for a chat.send request.
type ChatSendParams struct {
	Content string `json:"content"`
	Context string `json:"context,omitempty"`
}

// ChatHistoryParams are the parameters for a chat.history request.
type ChatHistoryParams struct {
	Context string `json:"context,omitempty"`
	Limit   int    `json:"limit,omitempty"`
	Before  string `json:"before,omitempty"`
}

// SendChat sends a chat message via the gateway.
func (c *Client) SendChat(content, context string) (string, error) {
	params := ChatSendParams{
		Content: content,
		Context: context,
	}
	return c.SendRequest("chat.send", params)
}

// GetHistory fetches chat history from the gateway.
func (c *Client) GetHistory(context string, limit int) (*Message, error) {
	params := ChatHistoryParams{
		Context: context,
		Limit:   limit,
	}
	return c.SendRequestAndWait("chat.history", params, 30*time.Second)
}

// ParseChatMessages extracts messages from a history response.
func ParseChatMessages(result interface{}) ([]ChatMessage, error) {
	data, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}

	// Try parsing as direct array
	var messages []ChatMessage
	if err := json.Unmarshal(data, &messages); err == nil {
		return messages, nil
	}

	// Try parsing as object with messages field
	var wrapped struct {
		Messages []ChatMessage `json:"messages"`
	}
	if err := json.Unmarshal(data, &wrapped); err == nil {
		return wrapped.Messages, nil
	}

	return nil, nil
}
