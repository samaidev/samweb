// Command samweb launches the SamWeb browser (wails version).
//
// SamWeb is a Chrome-style desktop web browser built with Go and WebView2
// (via wails). It embeds a Chrome-lookalike UI (tabs, omnibox, navigation
// buttons, history, bookmarks, cookie profiles) implemented in plain
// HTML/CSS/JS, and renders remote pages through a built-in proxy so that
// sites that would otherwise block iframe embedding can still be viewed.
//
// Usage:
//
//	samweb                    # open the window with default settings
//	samweb --engine Bing      # use Bing as the default search engine
//	samweb --width 1600 --height 900
//	samweb --title "SamWeb"
//	samweb --agent-addr 127.0.0.1:7777 --agent-token my-secret
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
		agentAddr  = flag.String("agent-addr", "127.0.0.1:7777", "address for the agent HTTP API (empty disables)")
		agentToken = flag.String("agent-token", "", "bearer token for the agent API (empty = no auth)")
		cdpPort    = flag.Int("cdp-port", 9222, "CDP remote debugging port for trusted input injection (0 disables)")
	)
	flag.Parse()

	if err := browser.Run(browser.Options{
		Title:      *title,
		Width:      *width,
		Height:     *height,
		EngineName: *engineName,
		AgentAddr:  *agentAddr,
		AgentToken: *agentToken,
		CDPPort:    *cdpPort,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "samweb: %v\n", err)
		os.Exit(1)
	}
}
