// Package captcha provides integration with third-party captcha-solving
// services (2captcha, CapSolver) to bypass slider captchas that SamWeb's
// built-in CDP drag cannot pass (e.g. Aliyun baxia's behavioral analysis).
//
// The flow is:
//  1. Agent detects a captcha on the page (e.g. Aliyun NoCaptcha slider)
//  2. Agent extracts the captcha's site key / scene ID from the page
//  3. Agent calls /agent/solve-captcha with the captcha type + parameters
//  4. SamWeb sends the request to 2captcha/CapSolver's API
//  5. The service solves the captcha in their environment (real browser + AI)
//  6. Returns a verification token
//  7. Agent injects the token into the page's captcha callback
//  8. Captcha is "passed" without any dragging
//
// This is the same approach Puppeteer/Playwright automation scripts use
// for production-grade captcha bypassing. Cost: ~$1-3 per 1000 solves.
package captcha

import (
	"context"
	"fmt"
	"time"
)

// Provider is the interface for a captcha-solving service.
type Provider interface {
	// SolveAliyun solves an Aliyun NoCaptcha slider captcha.
	// websiteURL is the full URL of the page with the captcha.
	// websiteKey is the Aliyun captcha app key (e.g. "FFFF0N00000000007596").
	// Returns the verification token to inject into the page.
	SolveAliyun(ctx context.Context, websiteURL, websiteKey string) (token string, err error)

	// Name returns the provider name for logging.
	Name() string
}

// Config holds the captcha-solving service configuration.
type Config struct {
	// Provider is "2captcha" or "capsolver". Default: "2captcha".
	Provider string
	// APIKey is the service's API key. Required.
	APIKey string
	// Timeout is the max time to wait for a solution. Default: 120s.
	Timeout time.Duration
}

// New creates a Provider from the config. Returns nil if no API key is set.
func New(cfg Config) Provider {
	if cfg.APIKey == "" {
		return nil
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 120 * time.Second
	}
	switch cfg.Provider {
	case "capsolver":
		return &CapSolver{config: cfg}
	default:
		return &TwoCaptcha{config: cfg}
	}
}

// SolveAliyunCaptchaRequest is the request body for /agent/solve-captcha.
type SolveAliyunCaptchaRequest struct {
	WebsiteURL string `json:"websiteUrl"`
	WebsiteKey string `json:"websiteKey"`
	// If true, try to auto-extract the websiteKey from the page.
	// If false, WebsiteKey must be provided.
	AutoExtract bool `json:"autoExtract,omitempty"`
}

// SolveAliyunCaptchaResponse is the response from /agent/solve-captcha.
type SolveAliyunCaptchaResponse struct {
	OK    bool   `json:"ok"`
	Token string `json:"token,omitempty"`
	Error string `json:"error,omitempty"`
}

// errNoProvider is returned when no captcha provider is configured.
var errNoProvider = fmt.Errorf("no captcha provider configured — set CAPTCHA_API_KEY env var or --captcha-api-key flag")
