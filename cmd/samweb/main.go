// Command samweb launches the SamWeb browser.
//
// SamWeb is a Chrome-style desktop web browser built with Go and the
// WebKit-based webview library. It embeds a Chrome-lookalike UI (tabs,
// omnibox, navigation buttons, history, bookmarks) implemented in plain
// HTML/CSS/JS, and renders remote pages through a built-in proxy so that
// sites that would otherwise block iframe embedding can still be viewed.
//
// Usage:
//
//	samweb                    # open the window with default settings
//	samweb --engine Bing      # use Bing as the default search engine
//	samweb --width 1600 --height 900
//	samweb --title "SamWeb"
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/samaidev/samweb/internal/browser"
)

func main() {
	var (
		title      = flag.String("title", "SamWeb", "OS window title")
		width      = flag.Int("width", 1280, "window width in pixels")
		height     = flag.Int("height", 800, "window height in pixels")
		engineName = flag.String("engine", "Google", "default search engine (Google, Bing, DuckDuckGo, Baidu)")
	)
	flag.Parse()

	if err := browser.Run(browser.Options{
		Title:      *title,
		Width:      *width,
		Height:     *height,
		EngineName: *engineName,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "samweb: %v\n", err)
		os.Exit(1)
	}
}
