//go:build !desktop

package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	"clawclient/app"
	"clawclient/templates"
	"github.com/stukennedy/irgo/pkg/livereload"
)

func main() {
	addr := flag.String("addr", "0.0.0.0:3000", "Listen address (host:port)")
	dev := flag.Bool("dev", false, "Enable dev mode with live reload")
	flag.Parse()

	if *dev {
		templates.DevMode = true
	}

	r := app.NewRouter()

	// Build the HTTP mux
	mux := http.NewServeMux()

	if *dev {
		lr := livereload.New()
		mux.HandleFunc("/dev/livereload", lr.Handler())
		// Serve static files from disk in dev mode (not embedded)
		mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
		fmt.Printf("Live reload enabled (build time: %d)\n", lr.BuildTime())
	}

	mux.Handle("/", r.Handler())

	fmt.Printf("🦞 ClawClient starting on http://%s\n", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
