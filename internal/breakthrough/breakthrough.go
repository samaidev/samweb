// Package breakthrough provides a reusable framework for bypassing
// dynamic anti-bot challenges (slider captchas, behavioral analysis,
// device fingerprinting) encountered during automated browsing.
//
// The package is designed around the lessons learned from cracking
// Aliyun baxia NoCaptcha on modelscope.cn. The key insight is that
// most anti-bot systems can be bypassed by combining:
//
//   1. CDP trusted events (isTrusted=true) for mousedown/mouseup
//   2. JS DOM manipulation to "teleport" UI state past detection limits
//   3. Anti-detection JS injection at document_start
//   4. Adaptive probing — detect the captcha's internal tracking
//      mechanism (e.g., style.left vs getBoundingClientRect) and
//      exploit it
//
// The framework is extensible: new captcha types can be added by
// implementing the Challenge interface.
package breakthrough

import (
	"context"
	"fmt"
)

// Challenge represents a type of anti-bot challenge that can be
// detected and bypassed.
type Challenge interface {
	// Name returns the challenge type name (e.g., "aliyun-baxia-slider").
	Name() string

	// Detect checks whether this challenge is present on the current page.
	Detect(ctx context.Context, env *Env) (bool, map[string]interface{}, error)

	// Bypass attempts to bypass the challenge. Returns true if successful.
	Bypass(ctx context.Context, env *Env, meta map[string]interface{}) (bool, error)
}

// Env provides the tools a Challenge needs to interact with the page.
type Env struct {
	CDPMouse     func(ctx context.Context, eventType string, x, y float64, button string, buttons, clickCount int) error
	Eval         func(ctx context.Context, script string) (string, error)
	Screenshot   func(ctx context.Context) ([]byte, error)
	SaveCookies  func(ctx context.Context) error
	LoadCookies  func(ctx context.Context) error
}

// Manager is the registry of known challenge types.
type Manager struct {
	challenges []Challenge
}

// NewManager creates a Manager with all built-in challenge types.
func NewManager() *Manager {
	m := &Manager{}
	m.Register(&AliyunBaxiaSlider{})
	return m
}

// Register adds a new challenge type.
func (m *Manager) Register(c Challenge) {
	m.challenges = append(m.challenges, c)
}

// DetectAndBypass tries each registered challenge in order.
func (m *Manager) DetectAndBypass(ctx context.Context, env *Env) (string, bool, error) {
	for _, c := range m.challenges {
		detected, meta, err := c.Detect(ctx, env)
		if err != nil || !detected {
			continue
		}
		success, err := c.Bypass(ctx, env, meta)
		if err != nil {
			return c.Name(), false, fmt.Errorf("bypass %s: %w", c.Name(), err)
		}
		return c.Name(), success, nil
	}
	return "", false, fmt.Errorf("no known challenge detected")
}
