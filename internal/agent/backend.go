package agent

import (
        "context"
        "encoding/json"
)

// Backend is the abstraction the Agent HTTP server talks to. Every method
// corresponds 1:1 with a public API endpoint.
//
// Implementations live in the browser package (the real one that drives a
// webview) and in the agent package (an in-memory mock used by tests).
type Backend interface {
        // Navigation
        Navigate(ctx context.Context, url string) error
        // NavigateDirect loads the URL as the webview's top-level page,
        // bypassing the iframe proxy. This is needed for sites (especially
        // SPAs) whose resources and API calls use absolute URLs that the
        // proxy cannot transparently rewrite.
        NavigateDirect(ctx context.Context, url string) error
        Back(ctx context.Context) error
        Forward(ctx context.Context) error
        Reload(ctx context.Context) error
        Stop(ctx context.Context) error

        // Interaction
        Click(ctx context.Context, opts ClickOpts) error
        Scroll(ctx context.Context, opts ScrollOpts) error
        Type(ctx context.Context, opts TypeOpts) error
        PressKey(ctx context.Context, opts KeyOpts) error
        Drag(ctx context.Context, opts DragOpts) error
        // DragTrusted is like Drag but uses CDP Input.dispatchMouseEvent
        // to inject isTrusted=true events. Required for Aliyun baxia /
        // Geetest sliders that reject JS-dispatched events. Returns an
        // error if the backend has no CDP connection (e.g. the mock
        // backend, or a real webview backend started without a CDP port).
        DragTrusted(ctx context.Context, opts TrustedDragOpts) error
        // DragTouch is like DragTrusted but uses CDP Input.dispatchTouchEvent
        // (touchStart/touchMove/touchEnd) instead of mouse events. Some
        // captcha systems listen for touch events only.
        DragTouch(ctx context.Context, opts TrustedDragOpts) error

        // NetworkCapture controls CDP Network domain capturing.
        // EnableNetwork starts capturing all requests; GetCapturedRequests
        // returns them; ClearCapturedRequests resets the buffer.
        EnableNetworkCapture(ctx context.Context) error
        DisableNetworkCapture(ctx context.Context) error
        GetCapturedRequests(ctx context.Context) ([]CapturedRequest, error)
        ClearCapturedRequests(ctx context.Context) error

        // GetAllCookies returns all cookies from the browser's cookie store
        // via CDP Network.getAllCookies.
        GetAllCookies(ctx context.Context) ([]BrowserCookie, error)

        // CDPRawMouse sends a single CDP Input.dispatchMouseEvent.
        CDPRawMouse(ctx context.Context, opts RawMouseOpts) error

        // CDPNavigateTop makes the WebView2 top-level page navigate to
        // url, bypassing the samweb iframe. Used for sites that block
        // iframe embedding (z.ai, Google, etc.). The samweb UI is gone
        // after this — bring it back with CDPNavigateTop("http://wails.localhost/").
        CDPNavigateTop(ctx context.Context, url string) error

        // SpawnTab spawns a new tab worker process for the given profile,
        // loading the given URL in an isolated user-data-dir. Used for
        // multi-profile multi-window support (each profile = one OS window
        // with its own cookie store + localStorage).
        SpawnTab(ctx context.Context, profileID, url string) (TabWorkerInfo, error)

        // ListTabWorkers returns info about all currently-running tab
        // workers spawned by this samweb instance.
        ListTabWorkers(ctx context.Context) ([]TabWorkerInfo, error)

        // BreakthroughSlider automatically detects and bypasses slider
        // captchas on the current page using the breakthrough framework.
        // Returns the challenge name and success status.
        BreakthroughSlider(ctx context.Context) (challenge string, success bool, err error)

        // Inspection
        Eval(ctx context.Context, script string) (json.RawMessage, error)

        // CDPEval runs a JS eval via CDP Runtime.evaluate (not via the
        // dispatch layer). Used by tab workers which don't have the
        // samweb UI bootstrap JS injected.
        CDPEval(ctx context.Context, script string) (json.RawMessage, error)

        // CDPDOMText reads element HTML via CDP DOM domain (bypasses JS
        // thread). Used when z.ai's JS is blocked during task execution.
        CDPDOMText(ctx context.Context, selector string) (string, error)
        // CDPDOMTextLast reads the LAST matching element's HTML via CDP DOM
        // domain. Needed for z.ai chat pages with multiple assistant messages
        // — we want the newest one, not the first.
        	CDPDOMTextLast(ctx context.Context, selector string) (string, error)
	// CDPDOMTextAll reads ALL matching elements' HTML via CDP DOM domain
	CDPDOMTextAll(ctx context.Context, selector string) ([]string, error)

        // SSE capture: z.ai streams Agent output via SSE/WebSocket.
        // These CDP events bypass JS thread entirely.
        EnableSSECapture(ctx context.Context) error
        GetSSEMessages(ctx context.Context) ([]SSEMessage, error)

        // Fetch capture: z.ai Agent mode streams output via fetch streaming.
        // The Fetch domain intercepts these responses at the network layer
        // (CDP WebSocket) — works even when z.ai's JS thread is fully
        // blocked during a long task execution. This is the ONLY reliable
        // way to get incremental output during long Agent tasks.
        //
        // EnableFetchCapture starts intercepting responses whose URL
        // contains urlFilter. Paused requests are NOT auto-continued —
        // the bridge MUST call FinishAllFetchRequests when done.
        EnableFetchCapture(ctx context.Context, urlFilter string) error
        DisableFetchCapture(ctx context.Context) error
        // GetFetchChunks returns new response body chunks since last call.
        GetFetchChunks(ctx context.Context) ([]FetchChunk, error)
        // PollFetchBodies snapshots all in-flight paused requests and
        // records any new bytes. Called by the bridge between
        // GetFetchChunks calls to ensure chunks are up-to-date.
        PollFetchBodies(ctx context.Context) error
        // FinishAllFetchRequests resumes all paused requests so the page
        // receives the responses. Called when the bridge is done capturing.
        FinishAllFetchRequests(ctx context.Context) error
        // GetPausedRequestIDs returns the request IDs of all currently
        // paused Fetch requests (for diagnostics).
        GetPausedRequestIDs(ctx context.Context) ([]string, error)
        Wait(ctx context.Context, selector string, timeoutMs int) error
        Elements(ctx context.Context, selector string) ([]Element, error)
        Element(ctx context.Context, selector string) (*Element, error)
        State(ctx context.Context) (*State, error)

        // Capture
        Screenshot(ctx context.Context, fullPage bool) ([]byte, error)
        // ScreenshotTrusted captures the page via CDP
        // Page.captureScreenshot. Unlike Screenshot (which uses JS SVG
        // foreignObject and often fails on complex pages), this captures
        // the actual rendered pixels from the WebView2 compositor — what
        // the user sees. Requires a CDP connection.
        ScreenshotTrusted(ctx context.Context, fullPage bool) ([]byte, error)

        // Session
        // ResetCookies clears all cookies in the backend's cookie jar. On the
        // real WebviewBackend this clears the proxy's shared jar so the next
        // navigation starts a fresh session (useful for switching accounts or
        // retrying a failed login). On the mock backend it is a no-op.
        ResetCookies(ctx context.Context) error

        // SaveCookies persists the cookie jar to disk so sessions survive
        // process restarts. On the real WebviewBackend this writes to
        // ~/.samweb/cookies.json (or whatever SetCookieFile set). On the mock
        // backend it is a no-op.
        SaveCookies(ctx context.Context) error

        // LoadCookies re-reads the cookie jar from disk, discarding any
        // in-memory cookies. Useful after SaveCookies on another process,
        // or after manually editing the cookie file.
        LoadCookies(ctx context.Context) error

        // ----------------------------- Profiles -----------------------------
        //
        // Profiles are named cookie snapshots that allow the user to switch
        // between multiple accounts on the same site (e.g. z.ai) within a
        // single samweb instance. See internal/browser/profiles.go for the
        // on-disk format and in-memory store.

        // SaveCurrentCookiesToProfile creates or updates a profile with the
        // current browser cookies. If a profile with the given name already
        // exists, its cookies are replaced; otherwise a new profile is created.
        SaveCurrentCookiesToProfile(ctx context.Context, name string) (ProfileInfo, error)

        // ListProfiles returns all saved profiles plus the active profile ID.
        ListProfiles(ctx context.Context) ([]ProfileInfo, string, error)

        // RenameProfile changes the user-visible name of a profile.
        RenameProfile(ctx context.Context, id, newName string) error

        // DeleteProfile removes a profile. If it was the active profile, the
        // active profile is cleared.
        DeleteProfile(ctx context.Context, id string) error

        // SwitchToProfile clears the current browser cookies and loads the
        // cookies from the named profile. Pass an empty id to clear the
        // active profile (cookies are kept as-is).
        SwitchToProfile(ctx context.Context, id string) error

        // Lifecycle
        Close() error
}
