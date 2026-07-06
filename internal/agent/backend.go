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

        // Inspection
        Eval(ctx context.Context, script string) (json.RawMessage, error)
        Wait(ctx context.Context, selector string, timeoutMs int) error
        Elements(ctx context.Context, selector string) ([]Element, error)
        Element(ctx context.Context, selector string) (*Element, error)
        State(ctx context.Context) (*State, error)

        // Capture
        Screenshot(ctx context.Context, fullPage bool) ([]byte, error)

        // Session
        // ResetCookies clears all cookies in the backend's cookie jar. On the
        // real WebviewBackend this clears the proxy's shared jar so the next
        // navigation starts a fresh session (useful for switching accounts or
        // retrying a failed login). On the mock backend it is a no-op.
        ResetCookies(ctx context.Context) error

        // Lifecycle
        Close() error
}
