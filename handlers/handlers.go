// Package handlers provides the HTTP handlers for ClawClient.
package handlers

import (
	"log"
	"time"

	"clawclient/internal/ctxengine"
	"clawclient/internal/gateway"
	"clawclient/internal/store"
	"clawclient/templates"
	"github.com/stukennedy/irgo/pkg/render"
	"github.com/stukennedy/irgo/pkg/router"
)

var renderer = render.NewTemplRenderer()

// App holds the shared application state for all handlers.
type App struct {
	Store   *store.Store
	Gateway *gateway.Client
	Engine  *ctxengine.Engine
}

// NewApp creates a new App with initialized dependencies.
func NewApp() *App {
	s, err := store.New()
	if err != nil {
		log.Printf("[app] Warning: could not init store: %v", err)
	}

	return &App{
		Store:   s,
		Gateway: gateway.NewClient(),
		Engine:  ctxengine.NewEngine(),
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
	r.DSGet("/api/thread/{context}", a.handleThread)
	r.DSPost("/api/send", a.handleSend)
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
	return renderer.Render(templates.OnboardPage())
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

	// Attempt connection
	if err := a.Gateway.Connect(signals.GatewayURL, signals.AuthToken); err != nil {
		log.Printf("[handler] Connection failed: %v", err)
		return sse.PatchTempl(templates.ConnectError(err.Error()))
	}

	// Setup event handlers
	a.setupGatewayHandlers()

	// Success - redirect to timeline
	sse.PatchTempl(templates.ConnectSuccess())
	return sse.Redirect("/")
}

// handleTimeline streams timeline blocks via SSE.
func (a *App) handleTimeline(ctx *router.Context) error {
	sse := ctx.SSE()

	// If connected, try to fetch history
	if a.Gateway.IsConnected() {
		res, err := a.Gateway.GetHistory("", 50)
		if err != nil {
			log.Printf("[handler] History fetch error: %v", err)
		} else if res != nil && res.Result != nil {
			messages, _ := gateway.ParseChatMessages(res.Result)
			for _, msg := range messages {
				contextName := msg.Context
				if contextName == "" {
					contextName = "main"
				}
				a.Engine.AddEvent(contextName, ctxengine.Event{
					ID:        msg.ID,
					Type:      "message",
					Role:      msg.Role,
					Content:   msg.Content,
					Timestamp: msg.Timestamp,
					Model:     msg.Model,
				})
			}
		}
	}

	blocks := a.Engine.GetTimeline()
	return sse.PatchTempl(templates.TimelineView(blocks))
}

// handleThread streams a specific context thread.
func (a *App) handleThread(ctx *router.Context) error {
	contextName := ctx.Param("context")
	if contextName == "" {
		contextName = "main"
	}

	threadCtx := a.Engine.GetContext(contextName)

	sse := ctx.SSE()
	return sse.PatchTempl(templates.ThreadView(threadCtx))
}

// handleSend sends a message via the gateway.
func (a *App) handleSend(ctx *router.Context) error {
	var signals struct {
		MessageInput string `json:"messageInput"`
	}
	if err := ctx.ReadSignals(&signals); err != nil || signals.MessageInput == "" {
		return nil
	}

	sse := ctx.SSE()

	if !a.Gateway.IsConnected() {
		return sse.PatchTempl(templates.ConnectError("Not connected to gateway"))
	}

	// Add the user message to the engine immediately
	a.Engine.AddEvent("main", ctxengine.Event{
		ID:        time.Now().Format("20060102150405"),
		Type:      "message",
		Role:      "user",
		Content:   signals.MessageInput,
		Timestamp: time.Now(),
	})

	// Send via gateway
	_, err := a.Gateway.SendChat(signals.MessageInput, "")
	if err != nil {
		log.Printf("[handler] Send error: %v", err)
	}

	// Clear input
	sse.PatchSignals(map[string]interface{}{"messageInput": ""})

	// Update timeline
	blocks := a.Engine.GetTimeline()
	return sse.PatchTempl(templates.TimelineView(blocks))
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

	a.Gateway.OnAgent(func(event gateway.AgentEvent) {
		log.Printf("[agent] %s: %s", event.Action, event.Content)

		switch event.Action {
		case "start":
			if event.Model != "" || event.Task != "" {
				a.Engine.AddAgent("main", ctxengine.AgentSpawn{
					ID:     event.AgentID,
					Model:  event.Model,
					Task:   event.Task,
					Status: "running",
				})
			}
		case "chunk":
			// Streamed content — for future real-time display
		case "done":
			if event.Content != "" {
				a.Engine.AddEvent("main", ctxengine.Event{
					ID:        event.AgentID,
					Type:      "message",
					Role:      "assistant",
					Content:   event.Content,
					Timestamp: time.Now(),
					Model:     event.Model,
				})
			}
		}
	})

	a.Gateway.OnStateChange(func(state gateway.ConnectionState) {
		log.Printf("[gateway] State changed: %s", state)
	})
}
