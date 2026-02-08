package gateway

import (
	"encoding/json"
	"fmt"
	"log"
	"time"
)

// Message represents a WebSocket protocol message.
type Message struct {
	Type    string      `json:"type"`              // "req", "res", "event"
	ID      string      `json:"id,omitempty"`      // Request/response ID
	Method  string      `json:"method,omitempty"`  // Method name (for req)
	Event   string      `json:"event,omitempty"`   // Event name (for events)
	Params  interface{} `json:"params,omitempty"`  // Request params
	Payload interface{} `json:"payload,omitempty"` // Event payload
	Result  interface{} `json:"result,omitempty"`  // Response result
	Error   interface{} `json:"error,omitempty"`   // Response error
	OK      *bool       `json:"ok,omitempty"`      // Response success
	Seq     int         `json:"seq,omitempty"`     // Broadcast sequence number
}

// EventName returns the event name (from Event field) or Method as fallback
func (m *Message) EventName() string {
	if m.Event != "" {
		return m.Event
	}
	return m.Method
}

// EventData returns the payload for event messages (prefers Payload, falls back to Params).
func (m *Message) EventData() interface{} {
	if m.Payload != nil {
		return m.Payload
	}
	return m.Params
}

// Event represents an incoming gateway event.
type Event struct {
	Type string
	Data interface{}
	Raw  *Message
}

// AgentEvent represents a streamed agent lifecycle/content event from the gateway.
// The gateway broadcasts: {type:"event", event:"agent", payload:{runId, stream, seq, data, sessionKey}}
type AgentEvent struct {
	RunID      string      `json:"runId"`
	Stream     string      `json:"stream"`     // "lifecycle", "assistant", "tool", "error"
	Seq        int         `json:"seq"`
	Data       interface{} `json:"data"`
	SessionKey string      `json:"sessionKey"`
	Raw        *Message
}

// AgentLifecycleData holds the data for lifecycle events (stream="lifecycle").
type AgentLifecycleData struct {
	Phase string `json:"phase"` // "start", "end", "error"
	Model string `json:"model"`
}

// AgentAssistantData holds the data for assistant text events (stream="assistant").
type AgentAssistantData struct {
	Text string `json:"text"`
}

// ChatEvent represents a "chat" broadcast event from the gateway.
// {type:"event", event:"chat", payload:{runId, sessionKey, seq, state, message, errorMessage}}
type ChatEvent struct {
	RunID        string       `json:"runId"`
	SessionKey   string       `json:"sessionKey"`
	Seq          int          `json:"seq"`
	State        string       `json:"state"` // "delta", "final", "error"
	Message      *ChatMsgBody `json:"message,omitempty"`
	ErrorMessage string       `json:"errorMessage,omitempty"`
}

// ChatMsgBody is the message body inside a chat event or history entry.
// Content can be a string or [{type:"text", text:"..."}].
type ChatMsgBody struct {
	Role      string      `json:"role"`
	Content   interface{} `json:"content"` // string or []ContentBlock
	Timestamp int64       `json:"timestamp,omitempty"`
	StopReason string     `json:"stopReason,omitempty"`
}

// ContentBlock is a single content block: {type:"text", text:"..."}.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ExtractText pulls the text out of a ChatMsgBody, handling both string and array content.
func (m *ChatMsgBody) ExtractText() string {
	if m == nil {
		return ""
	}
	// Try string first
	if s, ok := m.Content.(string); ok {
		return s
	}
	// Try array of content blocks
	if arr, ok := m.Content.([]interface{}); ok {
		var text string
		for _, item := range arr {
			if block, ok := item.(map[string]interface{}); ok {
				if t, ok := block["text"].(string); ok {
					if text != "" {
						text += "\n"
					}
					text += t
				}
			}
		}
		return text
	}
	return ""
}

// EpochTime is a time.Time that can unmarshal from both ISO strings and epoch milliseconds.
type EpochTime struct {
	time.Time
}

func (t *EpochTime) UnmarshalJSON(data []byte) error {
	// Try as number first (epoch millis)
	var millis float64
	if err := json.Unmarshal(data, &millis); err == nil {
		t.Time = time.UnixMilli(int64(millis))
		return nil
	}
	// Try as string (ISO format)
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		parsed, err := time.Parse(time.RFC3339Nano, s)
		if err == nil {
			t.Time = parsed
			return nil
		}
		// Try other formats
		parsed, err = time.Parse("2006-01-02T15:04:05Z", s)
		if err == nil {
			t.Time = parsed
			return nil
		}
	}
	return nil // fall back to zero time
}

// ChatMessage represents a message in chat history.
// Content can be a string or array of content blocks from the gateway.
type ChatMessage struct {
	ID        string      `json:"id"`
	Role      string      `json:"role"`      // "user", "assistant", "system"
	Content   interface{} `json:"content"`   // string or []ContentBlock
	Timestamp EpochTime   `json:"timestamp"`
	Context   string      `json:"context"`   // Context/thread name
	Model     string      `json:"model,omitempty"`
	AgentID   string      `json:"agentId,omitempty"`
}

// TextContent extracts the text from a ChatMessage's Content field.
func (m *ChatMessage) TextContent() string {
	if m == nil {
		return ""
	}
	// Try string first
	if s, ok := m.Content.(string); ok {
		return s
	}
	// Try array of content blocks
	if arr, ok := m.Content.([]interface{}); ok {
		var text string
		for _, item := range arr {
			if block, ok := item.(map[string]interface{}); ok {
				if t, ok := block["text"].(string); ok {
					if text != "" {
						text += "\n"
					}
					text += t
				}
			}
		}
		return text
	}
	return fmt.Sprintf("%v", m.Content)
}

// ChatSendParams are the parameters for a chat.send request.
type ChatSendParams struct {
	SessionKey     string `json:"sessionKey"`
	Message        string `json:"message"`
	IdempotencyKey string `json:"idempotencyKey"`
}

// ChatHistoryParams are the parameters for a chat.history request.
type ChatHistoryParams struct {
	SessionKey string `json:"sessionKey"`
	Limit      int    `json:"limit,omitempty"`
}

// DefaultSessionKey is the main session key used when none is specified.
const DefaultSessionKey = "agent:main:main"

// SendChat sends a chat message via the gateway.
func (c *Client) SendChat(content, context string) (string, error) {
	sk := c.SessionKey()
	if sk == "" {
		sk = DefaultSessionKey
	}
	params := ChatSendParams{
		SessionKey:     sk,
		Message:        content,
		IdempotencyKey: fmt.Sprintf("%d", time.Now().UnixNano()),
	}
	return c.SendRequest("chat.send", params)
}

// GetHistory fetches chat history from the gateway.
func (c *Client) GetHistory(context string, limit int) (*Message, error) {
	sk := c.SessionKey()
	if sk == "" {
		sk = DefaultSessionKey
	}
	params := ChatHistoryParams{
		SessionKey: sk,
		Limit:      limit,
	}
	return c.SendRequestAndWait("chat.history", params, 30*time.Second)
}

// ParseChatMessages extracts messages from a history response.
func ParseChatMessages(result interface{}) ([]ChatMessage, error) {
	data, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("marshal result: %w", err)
	}

	log.Printf("[parse] Raw data length: %d bytes", len(data))

	// Try parsing as direct array
	var messages []ChatMessage
	if err := json.Unmarshal(data, &messages); err == nil && len(messages) > 0 {
		log.Printf("[parse] Parsed as direct array: %d messages", len(messages))
		return messages, nil
	}

	// Try parsing as object with messages field
	var wrapped struct {
		Messages []ChatMessage `json:"messages"`
	}
	if err := json.Unmarshal(data, &wrapped); err == nil && len(wrapped.Messages) > 0 {
		log.Printf("[parse] Parsed as wrapped: %d messages", len(wrapped.Messages))
		return wrapped.Messages, nil
	} else if err != nil {
		log.Printf("[parse] Wrapped parse error: %v", err)
	} else {
		log.Printf("[parse] Wrapped parse: 0 messages")
	}

	// Try raw map extraction as last resort
	var rawMap map[string]interface{}
	if err := json.Unmarshal(data, &rawMap); err == nil {
		if msgsRaw, ok := rawMap["messages"]; ok {
			msgsData, _ := json.Marshal(msgsRaw)
			var msgs []ChatMessage
			if err := json.Unmarshal(msgsData, &msgs); err == nil {
				log.Printf("[parse] Parsed via raw map: %d messages", len(msgs))
				return msgs, nil
			} else {
				log.Printf("[parse] Raw map parse error: %v", err)
				// Try parsing individual messages
				if arr, ok := msgsRaw.([]interface{}); ok {
					log.Printf("[parse] Messages array has %d entries", len(arr))
					for i, item := range arr {
						itemData, _ := json.Marshal(item)
						var msg ChatMessage
						if err := json.Unmarshal(itemData, &msg); err != nil {
							if i < 3 {
								log.Printf("[parse] Message %d parse error: %v (first 200 bytes: %s)", i, err, string(itemData[:min(200, len(itemData))]))
							}
						} else {
							msgs = append(msgs, msg)
						}
					}
					log.Printf("[parse] Individually parsed %d of %d messages", len(msgs), len(arr))
					return msgs, nil
				}
			}
		}
	}

	return nil, fmt.Errorf("could not parse chat messages from type %T", result)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
