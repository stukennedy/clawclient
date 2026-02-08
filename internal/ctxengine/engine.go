package ctxengine

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"sync"
	"time"
)

// Engine manages conversation contexts and their events.
type Engine struct {
	mu       sync.RWMutex
	contexts map[string]*Context

	// Path to conversation-map.json on disk (optional).
	mapPath     string
	mapLastMod  time.Time
}

// NewEngine creates a new context engine.
func NewEngine() *Engine {
	return &Engine{
		contexts: make(map[string]*Context),
	}
}

// SetMapPath sets the filesystem path for conversation-map.json.
// The engine will read from this file when refreshing the timeline.
func (e *Engine) SetMapPath(path string) {
	e.mu.Lock()
	e.mapPath = path
	e.mu.Unlock()
}

// GetContext returns a context by name, creating it if it doesn't exist.
func (e *Engine) GetContext(name string) *Context {
	e.mu.Lock()
	defer e.mu.Unlock()

	if ctx, ok := e.contexts[name]; ok {
		return ctx
	}

	ctx := &Context{
		Name:  name,
		Color: ColorFromName(name),
	}
	e.contexts[name] = ctx
	return ctx
}

// GetAllContexts returns all contexts.
func (e *Engine) GetAllContexts() []*Context {
	e.mu.RLock()
	defer e.mu.RUnlock()

	result := make([]*Context, 0, len(e.contexts))
	for _, ctx := range e.contexts {
		result = append(result, ctx)
	}
	return result
}

// ClearMessages removes all message-type events from all contexts.
// Used before re-loading history with a different limit.
func (e *Engine) ClearMessages() {
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, ctx := range e.contexts {
		kept := ctx.Events[:0]
		for _, ev := range ctx.Events {
			if ev.Type != "message" {
				kept = append(kept, ev)
			}
		}
		ctx.Events = kept
	}
}

// AddEvent adds an event to a context.
func (e *Engine) AddEvent(contextName string, event Event) {
	ctx := e.GetContext(contextName)

	e.mu.Lock()
	defer e.mu.Unlock()

	event.Context = contextName
	ctx.Events = append(ctx.Events, event)

	if event.Timestamp.After(ctx.LastEvent) {
		ctx.LastEvent = event.Timestamp
	}

	// Update summary from latest assistant message
	if event.Role == "assistant" && event.Content != "" {
		summary := event.Content
		if len(summary) > 120 {
			summary = summary[:120] + "…"
		}
		ctx.Summary = summary
	}
}

// AddOrUpdateEvent adds an event or updates it if an event with the same ID exists.
// Used for streaming deltas where the same message gets progressively updated.
func (e *Engine) AddOrUpdateEvent(contextName string, event Event) {
	ctx := e.GetContext(contextName)

	e.mu.Lock()
	defer e.mu.Unlock()

	event.Context = contextName

	// Look for existing event with same ID to update in-place
	for i, ev := range ctx.Events {
		if ev.ID == event.ID {
			ctx.Events[i] = event
			if event.Timestamp.After(ctx.LastEvent) {
				ctx.LastEvent = event.Timestamp
			}
			if event.Role == "assistant" && event.Content != "" {
				summary := event.Content
				if len(summary) > 120 {
					summary = summary[:120] + "…"
				}
				ctx.Summary = summary
			}
			return
		}
	}

	// Not found — append new
	ctx.Events = append(ctx.Events, event)
	if event.Timestamp.After(ctx.LastEvent) {
		ctx.LastEvent = event.Timestamp
	}
	if event.Role == "assistant" && event.Content != "" {
		summary := event.Content
		if len(summary) > 120 {
			summary = summary[:120] + "…"
		}
		ctx.Summary = summary
	}
}

// AddAgent records a sub-agent spawn.
func (e *Engine) AddAgent(contextName string, agent AgentSpawn) {
	ctx := e.GetContext(contextName)

	e.mu.Lock()
	defer e.mu.Unlock()

	agent.Context = contextName
	ctx.Agents = append(ctx.Agents, agent)
}

// RefreshFromDisk re-reads conversation-map.json if it has been modified.
// Returns true if data was reloaded.
func (e *Engine) RefreshFromDisk() bool {
	e.mu.RLock()
	path := e.mapPath
	lastMod := e.mapLastMod
	e.mu.RUnlock()

	if path == "" {
		return false
	}

	info, err := os.Stat(path)
	if err != nil {
		// File doesn't exist (yet) — that's normal
		return false
	}

	if !info.ModTime().After(lastMod) {
		return false // unchanged
	}

	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("[ctxengine] Error reading %s: %v", path, err)
		return false
	}

	if err := e.LoadFromJSON(data); err != nil {
		log.Printf("[ctxengine] Error parsing %s: %v", path, err)
		return false
	}

	e.mu.Lock()
	e.mapLastMod = info.ModTime()
	e.mu.Unlock()

	log.Printf("[ctxengine] Reloaded conversation map from %s (%d bytes)", path, len(data))
	return true
}

// ContextSwitch represents a context switch event with its timestamp, used for
// assigning messages to contexts based on when context switches occurred.
type ContextSwitch struct {
	Timestamp time.Time
	Context   string
}

// GetContextSwitches returns context switches sorted chronologically.
func (e *Engine) GetContextSwitches() []ContextSwitch {
	e.mu.RLock()
	defer e.mu.RUnlock()

	var switches []ContextSwitch
	for _, ctx := range e.contexts {
		for _, ev := range ctx.Events {
			if ev.Type == "context_switch" {
				switches = append(switches, ContextSwitch{
					Timestamp: ev.Timestamp,
					Context:   ev.Context,
				})
			}
		}
	}
	sort.Slice(switches, func(i, j int) bool {
		return switches[i].Timestamp.Before(switches[j].Timestamp)
	})
	return switches
}

// InferContext returns the context name for a given timestamp based on
// context_switch events. Returns "main" if no context switch precedes it.
func (e *Engine) InferContext(t time.Time) string {
	switches := e.GetContextSwitches()
	ctx := "main"
	for _, sw := range switches {
		if sw.Timestamp.After(t) {
			break
		}
		ctx = sw.Context
	}
	return ctx
}

// ContextMeta returns name and color for a context.
func (e *Engine) ContextMeta(name string) (displayName string, color string) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if ctx, ok := e.contexts[name]; ok {
		displayName = ctx.Name
		if displayName == "" {
			displayName = name
		}
		color = ctx.Color
		if color == "" {
			color = ColorFromName(name)
		}
		return
	}
	return name, ColorFromName(name)
}

// FeedMessage is a message enriched with context info for the main feed.
type FeedMessage struct {
	Event       Event
	ContextKey  string // e.g. "project-clawclient"
	ContextName string // e.g. "ClawClient"
	ContextColor string
}

// GetFeed returns all messages across all contexts in chronological order,
// each tagged with its context. This is the unified main view.
func (e *Engine) GetFeed() []FeedMessage {
	e.RefreshFromDisk()

	e.mu.RLock()
	defer e.mu.RUnlock()

	// Collect context switches for inference
	var switches []ContextSwitch
	for _, ctx := range e.contexts {
		for _, ev := range ctx.Events {
			if ev.Type == "context_switch" {
				switches = append(switches, ContextSwitch{
					Timestamp: ev.Timestamp,
					Context:   ev.Context,
				})
			}
		}
	}
	sort.Slice(switches, func(i, j int) bool {
		return switches[i].Timestamp.Before(switches[j].Timestamp)
	})

	inferCtx := func(t time.Time) string {
		ctx := "main"
		for _, sw := range switches {
			if sw.Timestamp.After(t) {
				break
			}
			ctx = sw.Context
		}
		return ctx
	}

	var feed []FeedMessage
	for _, ctx := range e.contexts {
		for _, ev := range ctx.Events {
			if ev.Type != "message" || ev.Content == "" {
				continue
			}

			// Determine context: if it's already in a non-main context, use that.
			// If it's in "main", try to infer from timestamp.
			ctxKey := ev.Context
			if ctxKey == "" || ctxKey == "main" {
				ctxKey = inferCtx(ev.Timestamp)
			}

			ctxName := ctxKey
			ctxColor := ColorFromName(ctxKey)
			if c, ok := e.contexts[ctxKey]; ok {
				if c.Name != "" {
					ctxName = c.Name
				}
				if c.Color != "" {
					ctxColor = c.Color
				}
			}

			feed = append(feed, FeedMessage{
				Event:        ev,
				ContextKey:   ctxKey,
				ContextName:  ctxName,
				ContextColor: ctxColor,
			})
		}
	}

	// Sort chronologically (oldest first)
	sort.Slice(feed, func(i, j int) bool {
		return feed[i].Event.Timestamp.Before(feed[j].Event.Timestamp)
	})

	return feed
}

// GetContextList returns all known contexts with their metadata for the sidebar/overview.
func (e *Engine) GetContextList() []*Context {
	e.RefreshFromDisk()

	e.mu.RLock()
	defer e.mu.RUnlock()

	var list []*Context
	for _, ctx := range e.contexts {
		list = append(list, ctx)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].LastEvent.After(list[j].LastEvent)
	})
	return list
}

// GetTimeline returns timeline blocks sorted reverse chronologically.
// It automatically refreshes from disk first if a map path is set.
func (e *Engine) GetTimeline() []TimelineBlock {
	// Try to refresh from disk before building the timeline
	e.RefreshFromDisk()

	e.mu.RLock()
	defer e.mu.RUnlock()

	var blocks []TimelineBlock

	for _, ctx := range e.contexts {
		if len(ctx.Events) == 0 {
			continue
		}

		block := TimelineBlock{
			Context: ctx,
			Date:    formatDate(ctx.LastEvent),
			Events:  ctx.Events,
		}

		// Generate preview from the last message
		for i := len(ctx.Events) - 1; i >= 0; i-- {
			ev := ctx.Events[i]
			if ev.Content != "" {
				preview := ev.Content
				if len(preview) > 100 {
					preview = preview[:100] + "…"
				}
				block.Preview = preview
				break
			}
		}

		// Collect agent badges
		for _, a := range ctx.Agents {
			block.AgentBadges = append(block.AgentBadges, a)
		}

		// Check for compactions/cron
		for _, ev := range ctx.Events {
			if ev.Type == "compaction" {
				block.HasCompaction = true
			}
			if ev.Type == "cron" {
				block.IsCron = true
			}
		}

		blocks = append(blocks, block)
	}

	// Sort by most recent first
	sort.Slice(blocks, func(i, j int) bool {
		return blocks[i].Context.LastEvent.After(blocks[j].Context.LastEvent)
	})

	return blocks
}

// LoadFromJSON loads context data from a conversation-map.json blob.
func (e *Engine) LoadFromJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parse conversation map: %w", err)
	}

	if contextsData, ok := raw["contexts"]; ok {
		var contexts map[string]struct {
			Name       string `json:"name"`
			Color      string `json:"color"`
			Created    string `json:"created"`
			LastActive string `json:"lastActive"`
			Status     string `json:"status"`
		}
		if err := json.Unmarshal(contextsData, &contexts); err == nil {
			e.mu.Lock()
			for key, raw := range contexts {
				// Don't overwrite a context that already has events
				if existing, ok := e.contexts[key]; ok && len(existing.Events) > 0 {
					// Just update metadata
					if raw.Color != "" {
						existing.Color = raw.Color
					}
					continue
				}
				ctx := &Context{
					Name:  key,
					Color: raw.Color,
				}
				if ctx.Color == "" {
					ctx.Color = ColorFromName(key)
				}
				// Parse timestamps so contexts show on the timeline
				if raw.LastActive != "" {
					if t, err := time.Parse(time.RFC3339, raw.LastActive); err == nil {
						ctx.LastEvent = t
					}
				}
				if raw.Name != "" {
					ctx.Summary = raw.Name
				}
				e.contexts[key] = ctx
			}
			e.mu.Unlock()
		}
	}

	// Also load events from the map to populate contexts
	if eventsData, ok := raw["events"]; ok {
		var events []struct {
			ID        string `json:"id"`
			Type      string `json:"type"`
			Context   string `json:"context"`
			Summary   string `json:"summary"`
			Timestamp string `json:"timestamp"`
		}
		if err := json.Unmarshal(eventsData, &events); err == nil {
			e.mu.Lock()
			for _, ev := range events {
				if ev.Context == "" || ev.Type == "compaction" {
					continue
				}
				ctx, ok := e.contexts[ev.Context]
				if !ok {
					continue
				}
				// Add a summary event so the context has content to display
				if ev.Summary != "" && ev.Type == "context_switch" {
					// Check if this event already exists
					found := false
					for _, existing := range ctx.Events {
						if existing.ID == ev.ID {
							found = true
							break
						}
					}
					if !found {
						ts := time.Time{}
						if ev.Timestamp != "" {
							if t, err := time.Parse(time.RFC3339, ev.Timestamp); err == nil {
								ts = t
							}
						}
						ctx.Events = append(ctx.Events, Event{
							ID:        ev.ID,
							Type:      "context_switch",
							Content:   ev.Summary,
							Timestamp: ts,
							Context:   ev.Context,
						})
					}
				}
			}
			e.mu.Unlock()
		}
	}

	return nil
}

// ColorFromName generates a deterministic color from a context name.
func ColorFromName(name string) string {
	hash := md5.Sum([]byte(name))

	hue := int(hash[0]) + int(hash[1])<<1
	sat := 55 + int(hash[2])%30
	light := 55 + int(hash[3])%20

	hue = hue % 360

	return fmt.Sprintf("hsl(%d, %d%%, %d%%)", hue, sat, light)
}

func formatDate(t time.Time) string {
	if t.IsZero() {
		return "Unknown"
	}

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	yesterday := today.AddDate(0, 0, -1)
	eventDay := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())

	switch {
	case eventDay.Equal(today):
		return "Today"
	case eventDay.Equal(yesterday):
		return "Yesterday"
	case eventDay.After(today.AddDate(0, 0, -7)):
		return t.Weekday().String()
	default:
		return t.Format("Jan 2")
	}
}
