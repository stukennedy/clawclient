// Package handlers provides the HTTP handlers for ClawClient.
package handlers

import (
	"fmt"
	"log"
	"sync"
	"time"

	"clawclient/internal/ctxengine"
	"clawclient/internal/gateway"
	"clawclient/internal/store"
	"clawclient/templates"
	"github.com/stukennedy/irgo/pkg/render"
	"github.com/stukennedy/irgo/pkg/router"
)

var renderer = render.NewTemplRenderer()

// chatSubscriber is a channel that receives chat events for a specific runId.
type chatSubscriber struct {
	runID string
	ch    chan gateway.ChatEvent
}

// App holds the shared application state for all handlers.
type App struct {
	Store   *store.Store
	Gateway *gateway.Client
	Engine  *ctxengine.Engine

	// Chat response subscribers (handleSend waiting for responses)
	chatSubsMu sync.Mutex
	chatSubs   []chatSubscriber
}

// NewApp creates a new App with initialized dependencies.
func NewApp() *App {
	s, err := store.New()
	if err != nil {
		log.Printf("[app] Warning: could not init store: %v", err)
	}

	a := &App{
		Store:   s,
		Gateway: gateway.NewClient(),
		Engine:  ctxengine.NewEngine(),
	}

	// Point the context engine at the workspace conversation map
	if s != nil {
		cfg := s.Get()
		a.Engine.SetMapPath(cfg.ConversationMapPath())
		log.Printf("[app] Workspace path: %s", cfg.WorkspacePath)
		log.Printf("[app] Conversation map: %s", cfg.ConversationMapPath())
	}

	// Auto-connect to gateway in the background if already configured
	if s != nil && s.IsConfigured() {
		go a.autoConnect()
	}

	return a
}

// autoConnect tries to connect to the gateway using saved config.
func (a *App) autoConnect() {
	cfg := a.Store.Get()
	log.Printf("[app] Auto-connecting to gateway at %s", cfg.GatewayURL)

	if err := a.Gateway.Connect(cfg.GatewayURL, cfg.AuthToken); err != nil {
		log.Printf("[app] Auto-connect failed: %v (will retry on manual connect)", err)
		return
	}

	a.setupGatewayHandlers()
	log.Printf("[app] Auto-connected to gateway successfully")
}

// subscribeChatResponse creates a subscriber for chat events matching a runId.
func (a *App) subscribeChatResponse(runID string) chan gateway.ChatEvent {
	ch := make(chan gateway.ChatEvent, 16)
	a.chatSubsMu.Lock()
	a.chatSubs = append(a.chatSubs, chatSubscriber{runID: runID, ch: ch})
	a.chatSubsMu.Unlock()
	return ch
}

// unsubscribeChatResponse removes and closes a subscriber.
func (a *App) unsubscribeChatResponse(ch chan gateway.ChatEvent) {
	a.chatSubsMu.Lock()
	for i, sub := range a.chatSubs {
		if sub.ch == ch {
			a.chatSubs = append(a.chatSubs[:i], a.chatSubs[i+1:]...)
			break
		}
	}
	a.chatSubsMu.Unlock()
}

// notifyChatSubscribers sends a chat event to any matching subscribers.
func (a *App) notifyChatSubscribers(event gateway.ChatEvent) {
	a.chatSubsMu.Lock()
	defer a.chatSubsMu.Unlock()
	for _, sub := range a.chatSubs {
		// Empty runID means subscribe to all events
		if sub.runID == "" || sub.runID == event.RunID {
			select {
			case sub.ch <- event:
			default:
				// Don't block if subscriber is slow
			}
		}
	}
}

// Mount registers all handlers on the router.
func (a *App) Mount(r *router.Router) {
	// Page routes
	r.GET("/", a.handleHome)
	r.GET("/onboard", a.handleOnboard)

	// API - Datastar SSE endpoints
	r.DSPost("/api/connect", a.handleConnect)
	r.DSGet("/api/timeline", a.handleTimeline)
	r.DSGet("/api/contexts", a.handleContexts)
	r.DSGet("/api/thread/{context}", a.handleThread)
	r.DSPost("/api/send", a.handleSend)
	r.DSGet("/api/loadmore", a.handleLoadMore)
	r.DSGet("/api/status", a.handleStatus)
}

// handleHome renders the timeline page (or redirects to onboard).
func (a *App) handleHome(ctx *router.Context) (string, error) {
	if a.Store != nil && !a.Store.IsConfigured() {
		ctx.Redirect("/onboard")
		return "", nil
	}
	return renderer.Render(templates.HomePage())
}

// handleOnboard renders the setup page.
func (a *App) handleOnboard(ctx *router.Context) (string, error) {
	// Pre-fill with defaults/saved values
	gatewayURL := store.DefaultGatewayURL
	if a.Store != nil {
		cfg := a.Store.Get()
		if cfg.GatewayURL != "" {
			gatewayURL = cfg.GatewayURL
		}
	}
	return renderer.Render(templates.OnboardPage(gatewayURL))
}

// handleConnect processes gateway connection from onboarding.
func (a *App) handleConnect(ctx *router.Context) error {
	var signals struct {
		GatewayURL string `json:"gatewayUrl"`
		AuthToken  string `json:"authToken"`
	}
	if err := ctx.ReadSignals(&signals); err != nil {
		return ctx.SSE().PatchTempl(templates.ConnectError("Could not read form data"))
	}

	if signals.GatewayURL == "" || signals.AuthToken == "" {
		return ctx.SSE().PatchTempl(templates.ConnectError("Please fill in both fields"))
	}

	sse := ctx.SSE()

	// Show loading
	sse.PatchTempl(templates.ConnectLoading())

	// Save config
	if a.Store != nil {
		if err := a.Store.SetGateway(signals.GatewayURL, signals.AuthToken); err != nil {
			log.Printf("[handler] Error saving config: %v", err)
		}
	}

	// Disconnect any existing connection first
	if a.Gateway.IsConnected() {
		a.Gateway.Close()
	}

	// Attempt connection
	if err := a.Gateway.Connect(signals.GatewayURL, signals.AuthToken); err != nil {
		log.Printf("[handler] Connection failed: %v", err)
		return sse.PatchTempl(templates.ConnectError(err.Error()))
	}

	// Setup event handlers
	a.setupGatewayHandlers()

	// Success — redirect to timeline
	sse.PatchTempl(templates.ConnectSuccess())
	return sse.Redirect("/")
}

// historyLimit tracks how many messages we've loaded from gateway.
var historyLimit = 15
var historyLoaded bool

// loadHistory fetches chat history from gateway and populates the engine.
// Messages are assigned to their inferred context based on context_switch timestamps.
func (a *App) loadHistory() {
	if !a.Gateway.IsConnected() {
		return
	}

	// Ensure conversation-map.json is loaded first (for context switches)
	a.Engine.RefreshFromDisk()

	res, err := a.Gateway.GetHistory("", historyLimit)
	if err != nil {
		log.Printf("[handler] History fetch error: %v", err)
		return
	}
	if res == nil {
		return
	}
	historyData := res.Result
	if historyData == nil {
		historyData = res.Payload
	}
	messages, parseErr := gateway.ParseChatMessages(historyData)
	if parseErr != nil {
		log.Printf("[handler] History parse error: %v", parseErr)
	}
	log.Printf("[handler] Loaded %d messages (limit %d)", len(messages), historyLimit)

	// Clear existing history messages before re-adding (to handle limit increase)
	a.Engine.ClearMessages()

	for _, msg := range messages {
		if msg.Role != "user" && msg.Role != "assistant" {
			continue
		}
		text := msg.TextContent()
		if text == "" {
			continue
		}
		contextName := a.Engine.InferContext(msg.Timestamp.Time)
		a.Engine.AddEvent(contextName, ctxengine.Event{
			ID:        msg.ID,
			Type:      "message",
			Role:      msg.Role,
			Content:   text,
			Timestamp: msg.Timestamp.Time,
			Model:     msg.Model,
		})
	}
	historyLoaded = true
}

// loadMoreHistory increases the limit and reloads.
func (a *App) loadMoreHistory() {
	historyLimit += 20
	historyLoaded = false
	a.loadHistory()
}

// handleTimeline streams the unified feed via SSE.
func (a *App) handleTimeline(ctx *router.Context) error {
	sse := ctx.SSE()
	a.loadHistory()

	// Build the unified feed (all messages, color-coded by context)
	feed := a.Engine.GetFeed()
	log.Printf("[handler] Feed: %d messages", len(feed))
	return sse.PatchTempl(templates.FeedView(feed))
}

// handleContexts returns the context list for the sidebar.
func (a *App) handleContexts(ctx *router.Context) error {
	// Ensure we have history loaded first
	if a.Gateway.IsConnected() && len(a.Engine.GetContextList()) <= 1 {
		// Trigger a history load if we only have "main"
		a.loadHistory()
	}
	contexts := a.Engine.GetContextList()
	log.Printf("[handler] Contexts: %d", len(contexts))
	return ctx.SSE().PatchTempl(templates.ContextList(contexts))
}

// handleLoadMore loads more history messages and re-renders the feed.
func (a *App) handleLoadMore(ctx *router.Context) error {
	a.loadMoreHistory()
	feed := a.Engine.GetFeed()
	log.Printf("[handler] Load more: now %d messages (limit %d)", len(feed), historyLimit)
	return ctx.SSE().PatchTempl(templates.FeedView(feed))
}

// handleThread streams a specific context thread.
func (a *App) handleThread(ctx *router.Context) error {
	contextName := ctx.Param("context")
	if contextName == "" {
		contextName = "main"
	}

	threadCtx := a.Engine.GetContext(contextName)
	log.Printf("[handler] Thread %q: %d events", contextName, len(threadCtx.Events))

	sse := ctx.SSE()
	return sse.PatchTempl(templates.ThreadPage(threadCtx))
}

// handleSend sends a message via the gateway and waits for the response via SSE.
func (a *App) handleSend(ctx *router.Context) error {
	var signals struct {
		MessageInput string `json:"messageInput"`
		ActiveThread string `json:"activeThread"`
	}
	if err := ctx.ReadSignals(&signals); err != nil || signals.MessageInput == "" {
		return nil
	}

	sse := ctx.SSE()

	if !a.Gateway.IsConnected() {
		return sse.PatchTempl(templates.ConnectError("Not connected to gateway"))
	}

	// Determine which context we're posting to
	contextName := signals.ActiveThread
	if contextName == "" {
		contextName = "main"
	}
	inThread := signals.ActiveThread != ""

	// Add the user message to the engine immediately
	userMsgID := fmt.Sprintf("user-%d", time.Now().UnixNano())
	a.Engine.AddEvent(contextName, ctxengine.Event{
		ID:        userMsgID,
		Type:      "message",
		Role:      "user",
		Content:   signals.MessageInput,
		Timestamp: time.Now(),
	})

	// Clear input and show user message immediately
	sse.PatchSignals(map[string]interface{}{"messageInput": ""})
	if inThread {
		threadCtx := a.Engine.GetContext(contextName)
		sse.PatchTempl(templates.ThreadPage(threadCtx))
	} else {
		feed := a.Engine.GetFeed()
		sse.PatchTempl(templates.FeedView(feed))
	}

	// Subscribe to ALL chat events (we don't know the runId yet since SendChat
	// returns the request ID, not the agent run ID assigned by the gateway)
	chatCh := a.subscribeChatResponse("")
	defer a.unsubscribeChatResponse(chatCh)

	// Send via gateway
	_, err := a.Gateway.SendChat(signals.MessageInput, "")
	if err != nil {
		log.Printf("[handler] Send error: %v", err)
		return sse.PatchTempl(templates.ConnectError("Send failed: " + err.Error()))
	}

	// Track whether we've appended the streaming placeholder yet
	streamingStarted := false
	var currentRunID string

	// Wait for the response — keep SSE open
	timeout := time.After(120 * time.Second)
	for {
		select {
		case event := <-chatCh:
			switch event.State {
			case "delta":
				// Stream delta text to the UI
				if event.Message != nil {
					text := event.Message.ExtractText()
					if text != "" {
						currentRunID = event.RunID

						// Update engine state
						a.Engine.AddOrUpdateEvent(contextName, ctxengine.Event{
							ID:        event.RunID,
							Type:      "message",
							Role:      "assistant",
							Content:   text,
							Timestamp: time.Now(),
						})

						// Use targeted streaming update for both thread and feed
						sse.PatchTempl(templates.StreamingMessage(event.RunID, text))
						if !streamingStarted {
							streamingStarted = true
						}
					}
				}

			case "final":
				// Final response — update engine and render final message
				if event.Message != nil {
					text := event.Message.ExtractText()
					if text != "" {
						runID := event.RunID
						if runID == "" {
							runID = currentRunID
						}
						a.Engine.AddOrUpdateEvent(contextName, ctxengine.Event{
							ID:        runID,
							Type:      "message",
							Role:      event.Message.Role,
							Content:   text,
							Timestamp: time.Now(),
						})
						if inThread && runID != "" {
							// Replace streaming message with final rendered message
							finalEvent := ctxengine.Event{
								ID:        runID,
								Type:      "message",
								Role:      event.Message.Role,
								Content:   text,
								Timestamp: time.Now(),
							}
							return sse.PatchTempl(templates.FinalMessage(runID, finalEvent))
						}
					}
				}
				if inThread {
					threadCtx := a.Engine.GetContext(contextName)
					return sse.PatchTempl(templates.ThreadPage(threadCtx))
				}
				// For feed view, replace streaming msg with final
				if currentRunID != "" {
					finalEvent := ctxengine.Event{
						ID:   currentRunID,
						Type: "message",
						Role: "assistant",
						Timestamp: time.Now(),
					}
					if event.Message != nil {
						text := event.Message.ExtractText()
						finalEvent.Content = text
						finalEvent.Role = event.Message.Role
					}
					return sse.PatchTempl(templates.FinalMessage(currentRunID, finalEvent))
				}
				feed := a.Engine.GetFeed()
				return sse.PatchTempl(templates.FeedView(feed))

			case "error":
				log.Printf("[handler] Chat error: %s", event.ErrorMessage)
				return sse.PatchTempl(templates.ConnectError("Agent error: " + event.ErrorMessage))
			}

		case <-timeout:
			log.Printf("[handler] Response timeout after 120s")
			return sse.PatchTempl(templates.ConnectError("Response timed out"))
		}
	}
}

// handleStatus streams connection status updates.
func (a *App) handleStatus(ctx *router.Context) error {
	sse := ctx.SSE()
	connected := a.Gateway.IsConnected()
	return sse.PatchTempl(templates.ConnectionDot(connected))
}

// setupGatewayHandlers wires up event handlers on the gateway client.
func (a *App) setupGatewayHandlers() {
	a.Gateway.OnEvent(func(event gateway.Event) {
		log.Printf("[event] %s: %+v", event.Type, event.Data)
	})

	// Handle chat broadcast events — these carry the actual response messages
	a.Gateway.OnChat(func(event gateway.ChatEvent) {
		log.Printf("[chat] state=%s runId=%s", event.State, event.RunID)

		// Notify any waiting SSE handlers (e.g. handleSend)
		a.notifyChatSubscribers(event)

		switch event.State {
		case "delta":
			if event.Message != nil {
				text := event.Message.ExtractText()
				log.Printf("[chat:delta] %d chars so far", len(text))
			}

		case "final":
			if event.Message != nil {
				text := event.Message.ExtractText()
				if text != "" {
					log.Printf("[chat:final] got %d chars of response", len(text))
					a.Engine.AddOrUpdateEvent("main", ctxengine.Event{
						ID:        event.RunID,
						Type:      "message",
						Role:      event.Message.Role,
						Content:   text,
						Timestamp: time.Now(),
					})
				}
			}

		case "error":
			log.Printf("[chat:error] runId=%s error=%s", event.RunID, event.ErrorMessage)
		}
	})

	// Handle agent lifecycle events (start/end, model info, tool usage)
	a.Gateway.OnAgent(func(event gateway.AgentEvent) {
		log.Printf("[agent] stream=%s runId=%s seq=%d", event.Stream, event.RunID, event.Seq)

		if event.Stream == "lifecycle" {
			// Parse lifecycle data for phase info
			if data, ok := event.Data.(map[string]interface{}); ok {
				phase, _ := data["phase"].(string)
				model, _ := data["model"].(string)
				log.Printf("[agent:lifecycle] phase=%s model=%s", phase, model)

				if phase == "start" && model != "" {
					a.Engine.AddAgent("main", ctxengine.AgentSpawn{
						ID:     event.RunID,
						Model:  model,
						Status: "running",
					})
				}
			}
		}
	})

	a.Gateway.OnStateChange(func(state gateway.ConnectionState) {
		log.Printf("[gateway] State changed: %s", state)
	})
}
