// Package ctxengine provides types and logic for the conversation context engine.
package ctxengine

import "time"

// Context represents a conversation context (thread).
type Context struct {
	Name      string    `json:"name"`
	Color     string    `json:"color"`
	Summary   string    `json:"summary"`
	LastEvent time.Time `json:"lastEvent"`
	Events    []Event   `json:"events"`
	Agents    []AgentSpawn `json:"agents,omitempty"`
	StatusMD  string    `json:"statusMd,omitempty"`
}

// Event represents a single event in the context graph.
type Event struct {
	ID        string      `json:"id"`
	Type      string      `json:"type"`
	Role      string      `json:"role"`
	Content   string      `json:"content"`
	Timestamp time.Time   `json:"timestamp"`
	Context   string      `json:"context"`
	Model     string      `json:"model,omitempty"`
	AgentID   string      `json:"agentId,omitempty"`
	Meta      interface{} `json:"meta,omitempty"`
}

// AgentSpawn represents a sub-agent spawned within a context.
type AgentSpawn struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Task    string `json:"task"`
	Status  string `json:"status"`
	Context string `json:"context"`
}

// Compaction represents a context compaction event.
type Compaction struct {
	Timestamp time.Time `json:"timestamp"`
	Summary   string    `json:"summary"`
	Before    int       `json:"before"`
	After     int       `json:"after"`
}

// TimelineBlock represents a group of events for timeline display.
type TimelineBlock struct {
	Context       *Context
	Date          string
	Events        []Event
	Preview       string
	AgentBadges   []AgentSpawn
	HasCompaction bool
	IsCron        bool
}
