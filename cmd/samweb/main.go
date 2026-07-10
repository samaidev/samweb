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
//      samweb                    # open the window with default settings
//      samweb --engine Bing      # use Bing as the default search engine
//      samweb --width 1600 --height 900
//      samweb --title "SamWeb"
//      samweb --agent-addr 127.0.0.1:7777 --agent-token my-secret
//
// Tab worker mode (spawned by the main samweb for multi-profile support):
//
//      samweb --tab \
//          --profile qq \
//          --user-data-dir C:\Users\X\.samweb\data\qq \
//          --cdp-port 9223 \
//          --agent-port 7780 \
//          --url https://chat.z.ai
//
// In --tab mode, samweb opens a borderless WebView2 window that loads
// the given URL directly (no samweb UI chrome), using an isolated
// user-data-dir so each tab has its own cookie store + localStorage.
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

                // Tab worker mode flags
                tabMode      = flag.Bool("tab", false, "run as a tab worker (borderless window, no samweb UI, load URL directly)")
                profile      = flag.String("profile", "", "profile ID (tab mode only)")
                userDataDir  = flag.String("user-data-dir", "", "WebView2 user data directory for cookie/localStorage isolation (tab mode only)")
                agentPortTab = flag.Int("agent-port", 0, "agent HTTP API port (tab mode only; 0 = auto)")
                startURL     = flag.String("url", "", "URL to load on startup (tab mode only)")
        )
        flag.Parse()

        opts := browser.Options{
                Title:      *title,
                Width:      *width,
                Height:     *height,
                EngineName: *engineName,
                AgentAddr:  *agentAddr,
                AgentToken: *agentToken,
                CDPPort:    *cdpPort,

                TabMode:     *tabMode,
                Profile:     *profile,
                UserDataDir: *userDataDir,
                AgentPort:   *agentPortTab,
                StartURL:    *startURL,
        }

        if err := browser.Run(opts); err != nil {
                fmt.Fprintf(os.Stderr, "samweb: %v\n", err)
                os.Exit(1)
        }
}
