package cdp

import "time"

// MouseInput provides CDP Input.dispatchMouseEvent wrappers that inject
// trusted mouse events (isTrusted=true) into the page.

// MouseButton is the CDP representation of a mouse button.
type MouseButton string

const (
        MouseButtonNone   MouseButton = "none"
        MouseButtonLeft   MouseButton = "left"
        MouseButtonMiddle MouseButton = "middle"
        MouseButtonRight  MouseButton = "right"
)

// MouseEventType is the CDP representation of a mouse event type.
type MouseEventType string

const (
        MouseEventMousePressed MouseEventType = "mousePressed"
        MouseEventMouseReleased MouseEventType = "mouseReleased"
        MouseEventMouseMoved    MouseEventType = "mouseMoved"
)

// dispatchMouseParams is the params object for Input.dispatchMouseEvent.
// https://chromedevtools.github.io/devtools-protocol/tot/Input/#method-dispatchMouseEvent
type dispatchMouseParams struct {
        Type       MouseEventType `json:"type"`
        X          float64        `json:"x"`
        Y          float64        `json:"y"`
        Button     MouseButton    `json:"button,omitempty"`
        Buttons    int            `json:"buttons,omitempty"` // bitmask: 1=left,2=right,4=middle
        ClickCount int            `json:"clickCount,omitempty"`
        DeltaX     float64        `json:"deltaX,omitempty"`
        DeltaY     float64        `json:"deltaY,omitempty"`
        Modifiers  int            `json:"modifiers,omitempty"` // bitmask: 1=alt,2=ctrl,4=meta,8=shift
        Timestamp  float64        `json:"timestamp,omitempty"` // in seconds since epoch
}

// DispatchMouse sends a single Input.dispatchMouseEvent command.
// x, y are in CSS pixels relative to the page's viewport (NOT including
// browser chrome). The event is dispatched to the top-level page;
// coordinates inside iframes are handled by the browser's event
// retargeting (the event will reach the iframe's document if the
// coordinates fall inside the iframe).
func (c *Client) DispatchMouse(typ MouseEventType, x, y float64, button MouseButton, buttons int, clickCount int) error {
        _, err := c.send("Input.dispatchMouseEvent", dispatchMouseParams{
                Type:       typ,
                X:          x,
                Y:          y,
                Button:     button,
                Buttons:    buttons,
                ClickCount: clickCount,
        })
        return err
}

// DispatchTouchEmulationMouseMoved is a convenience for a mouseMoved
// event with no button held.
func (c *Client) MouseMove(x, y float64) error {
        return c.DispatchMouse(MouseEventMouseMoved, x, y, MouseButtonNone, 0, 0)
}

// MouseDown presses the left button at (x,y).
func (c *Client) MouseDown(x, y float64) error {
        return c.DispatchMouse(MouseEventMousePressed, x, y, MouseButtonLeft, 1, 1)
}

// MouseUp releases the left button at (x,y).
func (c *Client) MouseUp(x, y float64) error {
        return c.DispatchMouse(MouseEventMouseReleased, x, y, MouseButtonLeft, 1, 1)
}

// DispatchMouseRaw sends a single Input.dispatchMouseEvent with full
// control over all parameters. This is the low-level primitive that
// Drag / MouseDown / MouseUp / MouseMove are built on. Exposed so the
// agent can construct custom event sequences (e.g. mousedown without
// mouseup, then a series of mousemoves, then mouseup — needed for
// baxia slider where the handle snaps back if mouseup fires too early).
type RawMouseOpts struct {
        Type       string  `json:"type"`       // "mousePressed", "mouseReleased", "mouseMoved"
        X          float64 `json:"x"`
        Y          float64 `json:"y"`
        Button     string  `json:"button"`     // "none", "left", "middle", "right"
        Buttons    int     `json:"buttons"`    // bitmask: 1=left, 2=right, 4=middle
        ClickCount int     `json:"clickCount"`
        Timestamp  float64 `json:"timestamp,omitempty"` // monotonic timestamp in seconds
}

// DispatchRaw sends a single raw mouse event via CDP.
func (c *Client) DispatchRaw(opts RawMouseOpts) error {
        params := dispatchMouseParams{
                Type:       MouseEventType(opts.Type),
                X:          opts.X,
                Y:          opts.Y,
                Button:     MouseButton(opts.Button),
                Buttons:    opts.Buttons,
                ClickCount: opts.ClickCount,
        }
        if opts.Timestamp > 0 {
                params.Timestamp = opts.Timestamp
        }
        _, err := c.send("Input.dispatchMouseEvent", params)
        return err
}

// DispatchRawAsync sends a single raw mouse event without waiting for
// response (for high-frequency mousemove sequences).
func (c *Client) DispatchRawAsync(opts RawMouseOpts) error {
        params := dispatchMouseParams{
                Type:       MouseEventType(opts.Type),
                X:          opts.X,
                Y:          opts.Y,
                Button:     MouseButton(opts.Button),
                Buttons:    opts.Buttons,
                ClickCount: opts.ClickCount,
        }
        if opts.Timestamp > 0 {
                params.Timestamp = opts.Timestamp
        }
        return c.sendAsync("Input.dispatchMouseEvent", params)
}

// Drag performs a human-like drag from (x1,y1) to (x2,y2) using trusted
// CDP mouse events. The trajectory is a cubic bezier with smoothstep
// easing; inter-event delays are randomized to mimic a human. The
// events are dispatched with `isTrusted=true`, so they bypass the
// event.isTrusted check used by Aliyun baxia / Geetest / etc.
//
// duration is the total drag time in ms (0 = default 1000-1500).
// steps is the number of mouseMoved events (0 = default 50-100).
// jitter is the max pixel offset from the bezier curve (0 = default 3).
// holdAtEndMs is the time to hold the button at the end before release
// (0 = default 50-200ms).
//
// mousedown and mouseup are sent synchronously (so we know they were
// received before continuing); mousemove events are sent fire-and-forget
// (sendAsync) because waiting for each response would make a 100-step
// drag take 30+ seconds.
//
// All timing is implemented via time.Sleep on the calling goroutine, so
// Drag blocks for the full duration. Call it from a goroutine if you
// need async behavior.
func (c *Client) Drag(x1, y1, x2, y2 float64, durationMs, steps, jitter, holdAtEndMs int) error {
        // Apply defaults
        if durationMs <= 0 {
                durationMs = 1000 + int(randInt64(500))
        }
        if steps <= 0 {
                steps = 50 + int(randInt64(50))
        }
        if jitter <= 0 {
                jitter = 3
        }
        if holdAtEndMs <= 0 {
                holdAtEndMs = 50 + int(randInt64(150))
        }

        // Cubic bezier control points with human-like arc.
        dx := x2 - x1
        dy := y2 - y1
        cx1 := x1 + dx*0.25 + randFloat(-10, 10)
        cy1 := y1 + dy*0.25 - absFloat(dx)*0.1 + randFloat(-5, 5)
        cx2 := x1 + dx*0.75 + randFloat(-10, 10)
        cy2 := y1 + dy*0.75 - absFloat(dx)*0.05 + randFloat(-5, 5)

        bezierPoint := func(t float64) (float64, float64) {
                u := 1 - t
                x := u*u*u*x1 + 3*u*u*t*cx1 + 3*u*t*t*cx2 + t*t*t*x2
                y := u*u*u*y1 + 3*u*u*t*cy1 + 3*u*t*t*cy2 + t*t*t*y2
                x += randFloat(-float64(jitter), float64(jitter))
                y += randFloat(-float64(jitter), float64(jitter))
                return x, y
        }

        // Enable touch event emulation. Many captcha sliders (including
        // Aliyun baxia's) listen for touchstart/touchmove/touchend rather
        // than mouse events. Input.emulateTouchFromMouseEvent makes
        // Chromium synthesize touch events from our mouse events, so the
        // page sees touchstart/touchmove/touchend with isTrusted=true.
        // We enable it for the duration of the drag and disable it after.
        if _, err := c.send("Input.setEmulateTouchFromMouseEvent", map[string]interface{}{
                "enabled": true,
        }); err != nil {
                // Not fatal — some CDP versions don't support this; fall back
                // to plain mouse events.
        }

        // mousedown (trusted, synchronous so we know it was received)
        if err := c.MouseDown(x1, y1); err != nil {
                return err
        }

        stepDelay := time.Duration(durationMs/steps) * time.Millisecond
        if stepDelay <= 0 {
                stepDelay = time.Millisecond
        }

        for i := 0; i < steps; i++ {
                t := float64(i) / float64(steps)
                // smoothstep easing
                eased := t * t * (3 - 2*t)
                x, y := bezierPoint(eased)
                // fire-and-forget mousemove (no waiting for response).
                // During an active drag (button held), buttons=1 (left button
                // bitmask) but button="none" (no button *change* on move).
                // This is what real Chrome does.
                if err := c.sendAsync("Input.dispatchMouseEvent", dispatchMouseParams{
                        Type:    MouseEventMouseMoved,
                        X:       x,
                        Y:       y,
                        Button:  MouseButtonNone,
                        Buttons: 1, // left button held
                }); err != nil {
                        return err
                }
                // randomized inter-event delay
                jitterDur := time.Duration(randInt64(int64(stepDelay/2))) * time.Millisecond
                // occasional pause (5% chance, 30-80ms)
                if randInt64(100) < 5 {
                        jitterDur += time.Duration(30+randInt64(50)) * time.Millisecond
                }
                time.Sleep(stepDelay + jitterDur)
        }

        // final move to exact target (sync to ensure it lands before mouseup)
        if err := c.MouseMove(x2, y2); err != nil {
                return err
        }
        // hold at end
        time.Sleep(time.Duration(holdAtEndMs) * time.Millisecond)
        // mouseup (sync)
        if err := c.MouseUp(x2, y2); err != nil {
                return err
        }

        // Disable touch emulation now that the drag is done.
        if _, err := c.send("Input.setEmulateTouchFromMouseEvent", map[string]interface{}{
                "enabled": false,
        }); err != nil {
                // non-fatal
        }
        return nil
}
