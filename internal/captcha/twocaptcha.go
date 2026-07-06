package captcha

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// TwoCaptcha is a Provider that uses the 2captcha.com API.
// API docs: https://2captcha.com/api-docs/aliyun-captcha
type TwoCaptcha struct {
	config Config
}

type tcCreateTaskRequest struct {
	ClientKey string      `json:"clientKey"`
	Task      interface{} `json:"task"`
}

type tcAliyunTask struct {
	Type       string `json:"type"`
	WebsiteURL string `json:"websiteURL"`
	WebsiteKey string `json:"websiteKey"`
}

type tcCreateTaskResponse struct {
	ErrorID          int    `json:"errorId"`
	ErrorCode        string `json:"errorCode"`
	ErrorDescription string `json:"errorDescription"`
	TaskID           int64  `json:"taskId"`
}

type tcGetResultRequest struct {
	ClientKey string `json:"clientKey"`
	TaskID    int64  `json:"taskId"`
}

type tcGetResultResponse struct {
	ErrorID          int                    `json:"errorId"`
	ErrorCode        string                 `json:"errorCode"`
	ErrorDescription string                 `json:"errorDescription"`
	Status           string                 `json:"status"` // "processing" or "ready"
	Solution         map[string]interface{} `json:"solution"`
}

// SolveAliyun solves an Aliyun NoCaptcha via 2captcha.
func (t *TwoCaptcha) SolveAliyun(ctx context.Context, websiteURL, websiteKey string) (string, error) {
	// Step 1: Create task
	createReq := tcCreateTaskRequest{
		ClientKey: t.config.APIKey,
		Task: tcAliyunTask{
			Type:       "AntiAliyunCaptchaTaskProxyless",
			WebsiteURL: websiteURL,
			WebsiteKey: websiteKey,
		},
	}
	createBody, _ := json.Marshal(createReq)
	createResp, err := http.Post("https://api.2captcha.com/createTask", "application/json", bytes.NewReader(createBody))
	if err != nil {
		return "", fmt.Errorf("2captcha createTask: %w", err)
	}
	defer createResp.Body.Close()
	var createResult tcCreateTaskResponse
	if err := json.NewDecoder(createResp.Body).Decode(&createResult); err != nil {
		return "", fmt.Errorf("2captcha decode createTask: %w", err)
	}
	if createResult.ErrorID != 0 {
		return "", fmt.Errorf("2captcha error: %s (%s)", createResult.ErrorDescription, createResult.ErrorCode)
	}
	if createResult.TaskID == 0 {
		return "", fmt.Errorf("2captcha: no task ID returned")
	}

	// Step 2: Poll for result
	deadline := time.Now().Add(t.config.Timeout)
	pollInterval := 3 * time.Second
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(pollInterval):
		}

		getReq := tcGetResultRequest{
			ClientKey: t.config.APIKey,
			TaskID:    createResult.TaskID,
		}
		getBody, _ := json.Marshal(getReq)
		getResp, err := http.Post("https://api.2captcha.com/getTaskResult", "application/json", bytes.NewReader(getBody))
		if err != nil {
			continue
		}
		var getResult tcGetResultResponse
		json.NewDecoder(getResp.Body).Decode(&getResult)
		getResp.Body.Close()

		if getResult.ErrorID != 0 {
			return "", fmt.Errorf("2captcha result error: %s", getResult.ErrorDescription)
		}
		if getResult.Status == "ready" {
			// The token is in solution.token or solution.cookies
			if token, ok := getResult.Solution["token"].(string); ok && token != "" {
				return token, nil
			}
			// Some Aliyun captchas return multiple fields
			if getResult.Solution != nil {
				if encoded, err := json.Marshal(getResult.Solution); err == nil {
					return string(encoded), nil
				}
			}
			return "", fmt.Errorf("2captcha: solution ready but no token found")
		}
		// Still processing — continue polling
	}
	return "", fmt.Errorf("2captcha: timeout after %s", t.config.Timeout)
}

func (t *TwoCaptcha) Name() string { return "2captcha" }
