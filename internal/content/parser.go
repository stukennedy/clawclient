// Package content provides message content parsing — extracts media attachments,
// strips system metadata, and returns clean displayable content.
package content

import (
	"regexp"
	"strings"
)

// Media represents an extracted media attachment.
type Media struct {
	Path     string // Filesystem path
	MimeType string // e.g. "image/jpeg"
	URL      string // Served URL for the client (e.g. /media/inbound/abc.jpg)
}

// Parsed holds the result of parsing a message's raw content.
type Parsed struct {
	Text  string  // Clean displayable text
	Media []Media // Extracted media attachments
}

// mediaPattern matches [media attached: /path (mime) | /path]
var mediaPattern = regexp.MustCompile(`\[media attached:\s*([^\s]+)\s+\(([^)]+)\)\s*\|\s*([^\]]+)\]`)

// systemMetaPatterns are lines to strip entirely.
var systemMetaPatterns = []*regexp.Regexp{
	// "To send an image back..." instruction block
	regexp.MustCompile(`(?i)^To send an image back.*$`),
	// Channel metadata like [Telegram Stu Kennedy id:... +1m 2026-...]
	regexp.MustCompile(`^\[(?:Telegram|Signal|WhatsApp|Discord|Slack)\s+.*?\d{4}-\d{2}-\d{2}\s+\d{2}:\d{2}\s+\w+\].*$`),
	// [message_id: ...]
	regexp.MustCompile(`^\[message_id:.*\]$`),
	// System event lines
	regexp.MustCompile(`^System:\s*\[\d{4}-\d{2}-\d{2}.*$`),
}

// mediaBasePath is the prefix to strip to create the served URL.
const mediaBasePath = "/home/stu/.clawdbot/media/"

// Parse extracts media and cleans system metadata from raw message content.
func Parse(raw string) Parsed {
	var result Parsed

	// Extract media attachments
	matches := mediaPattern.FindAllStringSubmatch(raw, -1)
	for _, m := range matches {
		path := strings.TrimSpace(m[1])
		mime := strings.TrimSpace(m[2])

		// Build served URL
		url := ""
		if strings.HasPrefix(path, mediaBasePath) {
			url = "/media/" + strings.TrimPrefix(path, mediaBasePath)
		}

		result.Media = append(result.Media, Media{
			Path:     path,
			MimeType: mime,
			URL:      url,
		})
	}

	// Remove the media tag blocks from text
	text := mediaPattern.ReplaceAllString(raw, "")

	// Process line by line — strip system metadata
	lines := strings.Split(text, "\n")
	var cleaned []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			// Keep blank lines (but we'll trim excess later)
			cleaned = append(cleaned, "")
			continue
		}

		skip := false
		for _, pat := range systemMetaPatterns {
			if pat.MatchString(trimmed) {
				skip = true
				break
			}
		}
		if !skip {
			cleaned = append(cleaned, line)
		}
	}

	// Join and trim excess whitespace
	result.Text = strings.TrimSpace(strings.Join(cleaned, "\n"))

	// Collapse 3+ consecutive newlines to 2
	for strings.Contains(result.Text, "\n\n\n") {
		result.Text = strings.ReplaceAll(result.Text, "\n\n\n", "\n\n")
	}

	return result
}

// IsImage returns true if the media's mime type is an image.
func (m Media) IsImage() bool {
	return strings.HasPrefix(m.MimeType, "image/")
}
