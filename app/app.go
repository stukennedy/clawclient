// Package app provides the shared application setup.
// This is imported by both main.go (desktop) and mobile/mobile.go (mobile).
package app

import (
	"io/fs"
	"net/http"

	"clawclient/handlers"
	"clawclient/static"
	"github.com/stukennedy/irgo/pkg/router"
)

// NewRouter creates a new router with all app routes configured.
func NewRouter() *router.Router {
	r := router.New()

	// Serve embedded static files
	staticFS, _ := fs.Sub(static.Files, ".")
	r.Static("/static", http.FS(staticFS))

	// Serve media files from clawdbot media directory
	r.Static("/media", http.Dir("/home/stu/.clawdbot/media"))

	// Create app and mount handlers
	app := handlers.NewApp()
	app.Mount(r)

	return r
}
