// Package agent exposes a JSON HTTP API that lets an external program
// (an "agent") fully drive the SamWeb browser: navigate, click, scroll,
// type, eval JavaScript, query elements and their coordinates, take
// screenshots, and control tab history.
//
// The package is intentionally free of any webview / GUI dependency.
// All UI interaction is delegated to a Backend implementation, which
// makes it possible to unit-test the server against an in-memory mock
// backend without spinning up a real browser window.
package agent

import (
        "encoding/json"
)

// Element describes a single DOM element returned by Elements / Element.
// Coordinates (X, Y) are relative to the iframe's viewport, in CSS pixels.
type Element struct {
        Tag     string            `json:"tag"`
        ID      string            `json:"id,omitempty"`
        Classes []string          `json:"classes,omitempty"`
        X       float64           `json:"x"`
        Y       float64           `json:"y"`
        Width   float64           `json:"width"`
        Height  float64           `json:"height"`
        Text    string            `json:"text,omitempty"`
        Attrs   map[string]string `json:"attrs,omitempty"`
        HTML    string            `json:"html,omitempty"`
}

// TabInfo is a single tab's summary, as exposed via State.
type TabInfo struct {
        ID    int    `json:"id"`
        Title string `json:"title"`
        URL   string `json:"url"`
}

// State is a snapshot of the browser's current state.
type State struct {
        URL        string    `json:"url"`
        Title      string    `json:"title"`
        Tabs       []TabInfo `json:"tabs"`
        ActiveTab  int       `json:"activeTab"`
        CanBack    bool      `json:"canBack"`
        CanForward bool      `json:"canForward"`
}

// ScrollOpts controls a scroll action. Exactly one of (X/Y absolute), or
// Selector, or Direction+Amount should be set; if multiple are set the
// most specific wins.
type ScrollOpts struct {
        X         *float64 `json:"x,omitempty"`
        Y         *float64 `json:"y,omitempty"`
        Selector  string   `json:"selector,omitempty"`
        Direction string   `json:"direction,omitempty"` // "up" | "down" | "left" | "right"
        Amount    int      `json:"amount,omitempty"`    // pixels
}

// TypeOpts controls a type-text action.
type TypeOpts struct {
        Selector string   `json:"selector,omitempty"`
        X        *float64 `json:"x,omitempty"`
        Y        *float64 `json:"y,omitempty"`
        Text     string   `json:"text"`
        Clear    bool     `json:"clear,omitempty"`
        DelayMs  int      `json:"delayMs,omitempty"`
}

// KeyOpts controls a key-press action.
type KeyOpts struct {
        Key       string   `json:"key"`
        Modifiers []string `json:"modifiers,omitempty"` // "ctrl","shift","alt","meta"
        Selector  string   `json:"selector,omitempty"`
}

// NavigateOpts controls a navigation action.
type NavigateOpts struct {
        URL string `json:"url"`
}

// EvalOpts controls a JS evaluation action.
type EvalOpts struct {
        Script string `json:"script"`
}

// WaitOpts controls a wait-for-element action.
type WaitOpts struct {
        Selector  string `json:"selector"`
        TimeoutMs int    `json:"timeoutMs,omitempty"`
}

// ClickOpts controls a click action.
type ClickOpts struct {
        Selector string   `json:"selector,omitempty"`
        X        *float64 `json:"x,omitempty"`
        Y        *float64 `json:"y,omitempty"`
        Button   string   `json:"button,omitempty"` // "left" (default), "middle", "right"
        Double   bool     `json:"double,omitempty"`
}

// DragOpts controls a human-like drag action. Used for slider captchas
// (Aliyun baxia, Geetest, Tencent) where a straight-line trajectory is
// immediately detected as a bot.
//
// Required: either Selector (start) OR (X1,Y1) for the start point,
// plus either Selector2 (end) OR (X2,Y2) for the end point.
//
// The trajectory is a cubic bezier with random jitter, dispatched as a
// sequence of mousemove events with realistic inter-event delays.
type DragOpts struct {
        // Start point
        Selector string  `json:"selector,omitempty"` // start element selector
        X1       float64 `json:"x1,omitempty"`
        Y1       float64 `json:"y1,omitempty"`
        // End point
        Selector2 string  `json:"selector2,omitempty"` // end element selector
        X2        float64 `json:"x2,omitempty"`
        Y2        float64 `json:"y2,omitempty"`
        // IframeSelector, if set, makes Selector/Selector2 resolve inside
        // this iframe (same-origin only). Used for Aliyun baxia's punish
        // iframe (#baxia-dialog-content).
        IframeSelector string `json:"iframeSelector,omitempty"`
        // Optional tuning (all default to randomized human-like values)
        Duration  int `json:"duration,omitempty"`  // total ms (default 800-1500)
        Steps     int `json:"steps,omitempty"`     // mousemove count (default 50-100)
        Jitter    int `json:"jitter,omitempty"`    // max px offset from curve (default 3)
        HoldAtEnd int `json:"holdAtEnd,omitempty"` // ms to hold before mouseup (default 50-200)
}

// ScreenshotOpts controls a screenshot action.
type ScreenshotOpts struct {
        FullPage bool `json:"fullPage,omitempty"`
}

// OK is the standard "operation succeeded" response.
type OK struct {
        OK bool `json:"ok"`
}

// EvalResult wraps the result of a JS evaluation. Value is the JSON-encoded
// return value of the script.
type EvalResult struct {
        Value json.RawMessage `json:"value"`
}

// ElementsResult wraps a list of matched elements.
type ElementsResult struct {
        Elements []Element `json:"elements"`
        Count    int       `json:"count"`
}
