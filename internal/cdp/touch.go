package cdp

import "time"

// TouchPoint represents a single touch point for Input.dispatchTouchEvent.
// X, Y are in CSS pixels relative to the page viewport (same coordinate
// system as Input.dispatchMouseEvent).
type TouchPoint struct {
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	RadiusX    float64 `json:"radiusX,omitempty"`    // default 1
	RadiusY    float64 `json:"radiusY,omitempty"`    // default 1
	RotationAngle float64 `json:"rotationAngle,omitempty"` // default 0
	Force      float64 `json:"force,omitempty"`      // 0-1, default 1
	ID         float64 `json:"id,omitempty"`         // unique touch point ID
}

// touchEventType is the type of touch event.
type touchEventType string

const (
	TouchStart touchEventType = "touchStart"
	TouchMove  touchEventType = "touchMove"
	TouchEnd   touchEventType = "touchEnd"
)

// dispatchTouchParams is the params for Input.dispatchTouchEvent.
type dispatchTouchParams struct {
	Type        string       `json:"type"`
	TouchPoints []TouchPoint `json:"touchPoints"`
	Modifiers   int          `json:"modifiers,omitempty"` // bitmask: 1=alt,2=ctrl,4=meta,8=shift
	Timestamp   float64      `json:"timestamp,omitempty"`
}

// DispatchTouch sends a single Input.dispatchTouchEvent command.
func (c *Client) DispatchTouch(typ touchEventType, points []TouchPoint) error {
	_, err := c.send("Input.dispatchTouchEvent", dispatchTouchParams{
		Type:        string(typ),
		TouchPoints: points,
	})
	return err
}

// dispatchTouchAsync sends a touch event without waiting for response
// (for high-frequency touchMove during a drag).
func (c *Client) dispatchTouchAsync(typ touchEventType, points []TouchPoint) error {
	return c.sendAsync("Input.dispatchTouchEvent", dispatchTouchParams{
		Type:        string(typ),
		TouchPoints: points,
	})
}

// DragTouch performs a human-like drag using CDP touch events
// (touchStart → touchMove × N → touchEnd). Some captcha systems
// (Aliyun baxia, Geetest) listen for touch events rather than mouse
// events, especially on mobile-emulated pages. Touch events use a
// different event routing path in Chromium and may reach listeners
// that mouse events don't.
//
// Coordinates are page-viewport CSS pixels (same as Drag).
func (c *Client) DragTouch(x1, y1, x2, y2 float64, durationMs, steps, jitter, holdAtEndMs int) error {
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

	// Cubic bezier control points
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

	// touchStart (sync so we know it landed)
	if err := c.DispatchTouch(TouchStart, []TouchPoint{
		{X: x1, Y: y1, ID: 1, Force: 1, RadiusX: 1, RadiusY: 1},
	}); err != nil {
		return err
	}

	stepDelay := time.Duration(durationMs/steps) * time.Millisecond
	if stepDelay <= 0 {
		stepDelay = time.Millisecond
	}

	for i := 0; i < steps; i++ {
		t := float64(i) / float64(steps)
		eased := t * t * (3 - 2*t)
		x, y := bezierPoint(eased)
		if err := c.dispatchTouchAsync(TouchMove, []TouchPoint{
			{X: x, Y: y, ID: 1, Force: 1, RadiusX: 1, RadiusY: 1},
		}); err != nil {
			return err
		}
		jitterDur := time.Duration(randInt64(int64(stepDelay/2))) * time.Millisecond
		if randInt64(100) < 5 {
			jitterDur += time.Duration(30+randInt64(50)) * time.Millisecond
		}
		time.Sleep(stepDelay + jitterDur)
	}

	// final move to exact target
	if err := c.DispatchTouch(TouchMove, []TouchPoint{
		{X: x2, Y: y2, ID: 1, Force: 1, RadiusX: 1, RadiusY: 1},
	}); err != nil {
		return err
	}
	time.Sleep(time.Duration(holdAtEndMs) * time.Millisecond)
	// touchEnd (empty touchPoints = release)
	if err := c.DispatchTouch(TouchEnd, []TouchPoint{}); err != nil {
		return err
	}
	return nil
}
