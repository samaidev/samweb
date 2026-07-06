package captcha

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// CapSolver is a Provider that uses the capsolver.com API.
// CapSolver is often cheaper than 2captcha and has better support for
// Chinese captchas (Aliyun, Geetest, Tencent).
// API docs: https://docs.capsolver.com/
type CapSolver struct {
	config Config
}

type csCreateTaskRequest struct {
	ClientKey string      `json:"clientKey"`
	Task      interface{} `json:"task"`
}

type csAliyunTask struct {
	Type       string `json:"type"`
	WebsiteURL string `json:"websiteURL"`
	WebsiteKey string `json:"websiteKey"`
}

type csCreateTaskResponse struct {
	ErrorID          int    `json:"errorId"`
	ErrorCode        string `json:"errorCode"`
	ErrorDescription string `json:"errorDescription"`
	TaskID           string `json:"taskId"` // CapSolver uses string, not int
}

type csGetResultRequest struct {
	ClientKey string `json:"clientKey"`
	TaskID    string `json:"taskId"`
}

type csGetResultResponse struct {
	ErrorID          int                    `json:"errorId"`
	ErrorCode        string                 `json:"errorCode"`
	ErrorDescription string                 `json:"errorDescription"`
	Status           string                 `json:"status"`
	Solution         map[string]interface{} `json:"solution"`
}

// SolveAliyun solves an Aliyun NoCaptcha via CapSolver.
func (c *CapSolver) SolveAliyun(ctx context.Context, websiteURL, websiteKey string) (string, error) {
	// Step 1: Create task
	createReq := csCreateTaskRequest{
		ClientKey: c.config.APIKey,
		Task: csAliyunTask{
			Type:       "AliyunCaptchaTaskProxyless",
			WebsiteURL: websiteURL,
			WebsiteKey: websiteKey,
		},
	}
	createBody, _ := json.Marshal(createReq)
	createResp, err := http.Post("https://api.capsolver.com/createTask", "application/json", bytes.NewReader(createBody))
	if err != nil {
		return "", fmt.Errorf("capsolver createTask: %w", err)
	}
	defer createResp.Body.Close()
	var createResult csCreateTaskResponse
	if err := json.NewDecoder(createResp.Body).Decode(&createResult); err != nil {
		return "", fmt.Errorf("capsolver decode createTask: %w", err)
	}
	if createResult.ErrorID != 0 {
		return "", fmt.Errorf("capsolver error: %s (%s)", createResult.ErrorDescription, createResult.ErrorCode)
	}
	if createResult.TaskID == "" {
		return "", fmt.Errorf("capsolver: no task ID returned")
	}

	// Step 2: Poll for result
	deadline := time.Now().Add(c.config.Timeout)
	pollInterval := 3 * time.Second
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(pollInterval):
		}

		getReq := csGetResultRequest{
			ClientKey: c.config.APIKey,
			TaskID:    createResult.TaskID,
		}
		getBody, _ := json.Marshal(getReq)
		getResp, err := http.Post("https://api.capsolver.com/getTaskResult", "application/json", bytes.NewReader(getBody))
		if err != nil {
			continue
		}
		var getResult csGetResultResponse
		json.NewDecoder(getResp.Body).Decode(&getResult)
		getResp.Body.Close()

		if getResult.ErrorID != 0 {
			return "", fmt.Errorf("capsolver result error: %s", getResult.ErrorDescription)
		}
		if getResult.Status == "ready" {
			if token, ok := getResult.Solution["token"].(string); ok && token != "" {
				return token, nil
			}
			// Fallback: return whole solution as JSON
			if getResult.Solution != nil {
				if encoded, err := json.Marshal(getResult.Solution); err == nil {
					return string(encoded), nil
				}
			}
			return "", fmt.Errorf("capsolver: solution ready but no token found")
		}
	}
	return "", fmt.Errorf("capsolver: timeout after %s", c.config.Timeout)
}

func (c *CapSolver) Name() string { return "capsolver" }
