// Package gateway implements the OpenClaw Gateway WebSocket protocol client.
package gateway

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// ConnectionState tracks the gateway connection lifecycle.
type ConnectionState int

const (
	StateDisconnected ConnectionState = iota
	StateConnecting
	StateChallenge
	StateConnected
	StateError
)

func (s ConnectionState) String() string {
	switch s {
	case StateDisconnected:
		return "disconnected"
	case StateConnecting:
		return "connecting"
	case StateChallenge:
		return "challenge"
	case StateConnected:
		return "connected"
	case StateError:
		return "error"
	default:
		return "unknown"
	}
}

// Client manages the WebSocket connection to the OpenClaw Gateway.
type Client struct {
	mu         sync.RWMutex
	conn       *websocket.Conn
	state      ConnectionState
	sessionKey string
	token      string
	gatewayURL string
	reqCounter atomic.Int64

	// Event handlers
	onEvent    func(Event)
	onAgent    func(AgentEvent)
	onStateChange func(ConnectionState)

	// Pending request callbacks
	pending   map[string]chan *Message
	pendingMu sync.Mutex

	// Background read loop
	done chan struct{}
}

// NewClient creates a new gateway client.
func NewClient() *Client {
	return &Client{
		state:   StateDisconnected,
		pending: make(map[string]chan *Message),
		done:    make(chan struct{}),
	}
}

// State returns the current connection state.
func (c *Client) State() ConnectionState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state
}

// SessionKey returns the session key (if connected).
func (c *Client) SessionKey() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sessionKey
}

// IsConnected returns true if the client is fully connected.
func (c *Client) IsConnected() bool {
	return c.State() == StateConnected
}

// OnEvent sets the handler for incoming events.
func (c *Client) OnEvent(fn func(Event)) {
	c.mu.Lock()
	c.onEvent = fn
	c.mu.Unlock()
}

// OnAgent sets the handler for agent streaming events.
func (c *Client) OnAgent(fn func(AgentEvent)) {
	c.mu.Lock()
	c.onAgent = fn
	c.mu.Unlock()
}

// OnStateChange sets the handler for connection state changes.
func (c *Client) OnStateChange(fn func(ConnectionState)) {
	c.mu.Lock()
	c.onStateChange = fn
	c.mu.Unlock()
}

func (c *Client) setState(s ConnectionState) {
	c.mu.Lock()
	c.state = s
	cb := c.onStateChange
	c.mu.Unlock()
	if cb != nil {
		cb(s)
	}
}

// Connect establishes the WebSocket connection and completes the handshake.
func (c *Client) Connect(gatewayURL, token string) error {
	c.mu.Lock()
	c.gatewayURL = gatewayURL
	c.token = token
	c.mu.Unlock()

	c.setState(StateConnecting)

	// Connect to WebSocket
	wsURL := gatewayURL
	dialer := websocket.DefaultDialer
	dialer.HandshakeTimeout = 10 * time.Second

	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		c.setState(StateError)
		return fmt.Errorf("websocket dial: %w", err)
	}

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()

	// Start reading messages
	go c.readLoop()

	// Wait for connect.challenge event
	c.setState(StateChallenge)

	challengeMsg, err := c.waitForMessage("connect.challenge", 10*time.Second)
	if err != nil {
		c.Close()
		return fmt.Errorf("waiting for challenge: %w", err)
	}

	log.Printf("[gateway] Received challenge: %+v", challengeMsg)

	// Send connect request
	reqID := c.nextReqID()
	connectReq := &Message{
		Type:   "req",
		ID:     reqID,
		Method: "connect",
		Params: map[string]interface{}{
			"minProtocol": 3,
			"maxProtocol": 3,
			"client": map[string]interface{}{
				"id":       "clawclient",
				"version":  "0.1.0",
				"platform": "linux",
				"mode":     "operator",
			},
			"role":        "operator",
			"scopes":      []string{"operator.read", "operator.write"},
			"caps":        []string{},
			"commands":    []string{},
			"permissions": map[string]interface{}{},
			"auth": map[string]interface{}{
				"token": token,
			},
		},
	}

	if err := c.sendMessage(connectReq); err != nil {
		c.Close()
		return fmt.Errorf("sending connect request: %w", err)
	}

	// Wait for response
	resMsg, err := c.waitForResponse(reqID, 10*time.Second)
	if err != nil {
		c.Close()
		return fmt.Errorf("waiting for connect response: %w", err)
	}

	// Check for hello-ok
	if result, ok := resMsg.Result.(map[string]interface{}); ok {
		if status, _ := result["status"].(string); status == "hello-ok" {
			if sk, _ := result["sessionKey"].(string); sk != "" {
				c.mu.Lock()
				c.sessionKey = sk
				c.mu.Unlock()
			}
			c.setState(StateConnected)
			log.Printf("[gateway] Connected! Session: %s", c.SessionKey())
			return nil
		}
	}

	// Check for error
	if resMsg.Error != nil {
		c.Close()
		errMsg, _ := json.Marshal(resMsg.Error)
		return fmt.Errorf("connect rejected: %s", string(errMsg))
	}

	c.setState(StateConnected)
	log.Printf("[gateway] Connected (result: %+v)", resMsg.Result)
	return nil
}

// Close disconnects from the gateway.
func (c *Client) Close() {
	c.mu.Lock()
	conn := c.conn
	c.conn = nil
	c.mu.Unlock()

	if conn != nil {
		conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		conn.Close()
	}

	// Signal done
	select {
	case c.done <- struct{}{}:
	default:
	}

	c.setState(StateDisconnected)
}

func (c *Client) nextReqID() string {
	return fmt.Sprintf("%d", c.reqCounter.Add(1))
}

func (c *Client) sendMessage(msg *Message) error {
	c.mu.RLock()
	conn := c.conn
	c.mu.RUnlock()

	if conn == nil {
		return fmt.Errorf("not connected")
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}

	log.Printf("[gateway] >>> %s", string(data))
	return conn.WriteMessage(websocket.TextMessage, data)
}

// SendRequest sends a request and returns the request ID.
func (c *Client) SendRequest(method string, params interface{}) (string, error) {
	reqID := c.nextReqID()
	msg := &Message{
		Type:   "req",
		ID:     reqID,
		Method: method,
		Params: params,
	}
	return reqID, c.sendMessage(msg)
}

// SendRequestAndWait sends a request and waits for the response.
func (c *Client) SendRequestAndWait(method string, params interface{}, timeout time.Duration) (*Message, error) {
	reqID := c.nextReqID()

	// Register pending
	ch := make(chan *Message, 1)
	c.pendingMu.Lock()
	c.pending[reqID] = ch
	c.pendingMu.Unlock()

	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, reqID)
		c.pendingMu.Unlock()
	}()

	msg := &Message{
		Type:   "req",
		ID:     reqID,
		Method: method,
		Params: params,
	}

	if err := c.sendMessage(msg); err != nil {
		return nil, err
	}

	select {
	case res := <-ch:
		return res, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("timeout waiting for response to %s", method)
	}
}

func (c *Client) waitForMessage(eventType string, timeout time.Duration) (*Message, error) {
	// Register a temporary event watcher
	ch := make(chan *Message, 1)

	c.pendingMu.Lock()
	key := "event:" + eventType
	c.pending[key] = ch
	c.pendingMu.Unlock()

	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, key)
		c.pendingMu.Unlock()
	}()

	select {
	case msg := <-ch:
		return msg, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("timeout waiting for %s", eventType)
	}
}

func (c *Client) waitForResponse(reqID string, timeout time.Duration) (*Message, error) {
	ch := make(chan *Message, 1)
	c.pendingMu.Lock()
	c.pending[reqID] = ch
	c.pendingMu.Unlock()

	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, reqID)
		c.pendingMu.Unlock()
	}()

	select {
	case msg := <-ch:
		return msg, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("timeout waiting for response %s", reqID)
	}
}

func (c *Client) readLoop() {
	defer func() {
		c.setState(StateDisconnected)
	}()

	for {
		c.mu.RLock()
		conn := c.conn
		c.mu.RUnlock()

		if conn == nil {
			return
		}

		_, data, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("[gateway] Read error: %v", err)
			}
			return
		}

		log.Printf("[gateway] <<< %s", string(data))

		var msg Message
		if err := json.Unmarshal(data, &msg); err != nil {
			log.Printf("[gateway] Parse error: %v", err)
			continue
		}

		c.handleMessage(&msg)
	}
}

func (c *Client) handleMessage(msg *Message) {
	switch msg.Type {
	case "event":
		// Check pending event waiters
		c.pendingMu.Lock()
		key := "event:" + msg.Method
		if ch, ok := c.pending[key]; ok {
			select {
			case ch <- msg:
			default:
			}
			delete(c.pending, key)
		}
		c.pendingMu.Unlock()

		// Handle agent events specially
		if msg.Method == "agent" {
			c.mu.RLock()
			cb := c.onAgent
			c.mu.RUnlock()
			if cb != nil {
				var evt AgentEvent
				if data, err := json.Marshal(msg.Params); err == nil {
					json.Unmarshal(data, &evt)
				}
				evt.Raw = msg
				cb(evt)
			}
			return
		}

		// Generic event handler
		c.mu.RLock()
		cb := c.onEvent
		c.mu.RUnlock()
		if cb != nil {
			cb(Event{
				Type: msg.Method,
				Data: msg.Params,
				Raw:  msg,
			})
		}

	case "res":
		// Route to pending request
		c.pendingMu.Lock()
		if ch, ok := c.pending[msg.ID]; ok {
			select {
			case ch <- msg:
			default:
			}
			delete(c.pending, msg.ID)
		}
		c.pendingMu.Unlock()
	}
}
