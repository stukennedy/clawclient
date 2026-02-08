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
		var contexts map[string]*Context
		if err := json.Unmarshal(contextsData, &contexts); err == nil {
			e.mu.Lock()
			for name, ctx := range contexts {
				ctx.Name = name
				if ctx.Color == "" {
					ctx.Color = ColorFromName(name)
				}
				e.contexts[name] = ctx
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
