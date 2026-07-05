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
