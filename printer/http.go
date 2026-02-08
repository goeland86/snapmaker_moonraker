package printer

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const httpPort = 8080

// executeGCodeHTTP sends a gcode command via the Snapmaker HTTP API.
func executeGCodeHTTP(ip, token, gcode string) (string, error) {
	u := fmt.Sprintf("http://%s:%d/api/v1/execute_code?token=%s",
		ip, httpPort, url.QueryEscape(token))

	body := strings.NewReader(fmt.Sprintf("code=%s", url.QueryEscape(gcode)))
	req, err := http.NewRequest("POST", u, body)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("executing gcode: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gcode execution failed (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	return string(respBody), nil
}

// getStatusHTTP fetches printer status via the Snapmaker HTTP API.
func getStatusHTTP(ip, token string) (map[string]interface{}, error) {
	u := fmt.Sprintf("http://%s:%d/api/v1/status?token=%s&ts=%d",
		ip, httpPort, url.QueryEscape(token), time.Now().Unix())

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(u)
	if err != nil {
		return nil, fmt.Errorf("fetching status: %w", err)
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding status: %w", err)
	}

	return result, nil
}

// connectHTTP performs the HTTP connect/token handshake.
func connectHTTP(ip, token string) (string, error) {
	u := fmt.Sprintf("http://%s:%d/api/v1/connect", ip, httpPort)

	body := strings.NewReader(fmt.Sprintf("token=%s", url.QueryEscape(token)))
	req, err := http.NewRequest("POST", u, body)
	if err != nil {
		return "", fmt.Errorf("creating connect request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("connecting: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decoding connect response: %w", err)
	}

	if result.Token != "" {
		return result.Token, nil
	}
	return token, nil
}
