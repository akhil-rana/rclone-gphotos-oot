package gphotosmobile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rclone/rclone/fs"
)

// automatedBrowserLogin attempts to launch Chrome via CDP, waits for the user to log in,
// and extracts the oauth_token cookie.
func automatedBrowserLogin(ctx context.Context) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("automated login is currently only supported on Linux")
	}

	// Find Chrome/Firefox binary
	var chromeBin string
	paths := []string{"google-chrome", "google-chrome-beta", "chrome", "chromium", "chromium-browser", "brave", "brave-browser"}
	for _, p := range paths {
		if path, err := exec.LookPath(p); err == nil {
			chromeBin = path
			break
		}
	}

	if chromeBin == "" {
		return "", fmt.Errorf("could not find a supported Chromium-based browser (Chrome/Chromium/Brave) installation")
	}

	// Create a temporary user data dir to ensure a fresh session
	tmpProfile, err := os.MkdirTemp("", "rclone-cdp-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp profile: %v", err)
	}
	defer os.RemoveAll(tmpProfile)

	port := 9222
	url := "https://accounts.google.com/EmbeddedSetup"

	cmd := exec.Command(chromeBin,
		fmt.Sprintf("--remote-debugging-port=%d", port),
		fmt.Sprintf("--user-data-dir=%s", tmpProfile),
		"--no-first-run",
		"--no-default-browser-check",
		url,
	)

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("failed to start Chrome: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
	}()

	fs.Logf(nil, "Browser launched. Please sign in...")
	fs.Logf(nil, "Waiting for authentication to complete (timeout in 5 minutes)...")

	// Wait for the CDP port to open
	time.Sleep(2 * time.Second)

	// We need to find the websocket URL for the page
	var wsURL string
	for i := 0; i < 10; i++ {
		resp, err := http.Get(fmt.Sprintf("http://localhost:%d/json", port))
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			var targets []map[string]interface{}
			if err := json.Unmarshal(body, &targets); err == nil {
				for _, t := range targets {
					if t["type"] == "page" {
						if ws, ok := t["webSocketDebuggerUrl"].(string); ok {
							wsURL = ws
							break
						}
					}
				}
			}
		}
		if wsURL != "" {
			break
		}
		time.Sleep(1 * time.Second)
	}

	if wsURL == "" {
		return "", fmt.Errorf("could not connect to browser DevTools protocol")
	}

	// Poll the cookie from memory directly via the CDP WebSocket bridge
	timeout := time.Now().Add(5 * time.Minute)
	for time.Now().Before(timeout) {
		token, err := getCookieViaWebSocket(wsURL, "oauth_token")
		if err == nil && token != "" {
			return token, nil
		}
		time.Sleep(2 * time.Second)
	}

	return "", fmt.Errorf("timeout (5 minutes) waiting for login via CDP bridge")

}

// WebSocket implementation for sending CDP JSON-RPC commands.

func getCookieViaWebSocket(wsURL string, cookieName string) (string, error) {
	c, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return "", err
	}
	defer c.Close()

	// 1. Enable the Network domain (for Chrome/Brave via CDP)
	_ = c.WriteMessage(websocket.TextMessage, []byte("{\"id\": 1, \"method\": \"Network.enable\"}"))

	// 2. CDP Command: Network.getAllCookies
	cdpCookieMsg := []byte("{\"id\": 2, \"method\": \"Network.getAllCookies\"}")
	_ = c.WriteMessage(websocket.TextMessage, cdpCookieMsg)

	// Read frames in a loop
	timeout := time.Now().Add(5 * time.Second)
	for time.Now().Before(timeout) {
		_ = c.SetReadDeadline(time.Now().Add(1 * time.Second))
		_, payload, err := c.ReadMessage()
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				_ = c.WriteMessage(websocket.TextMessage, cdpCookieMsg)
				continue
			}
			return "", err
		}

		// Fast universal string extraction:
		strPayload := string(payload)
		idx := strings.Index(strPayload, "oauth2_4/")
		if idx != -1 {
			endIdx := idx
			for endIdx < len(strPayload) {
				if strPayload[endIdx] == '"' {
					break
				}
				endIdx++
			}
			token := strPayload[idx:endIdx]
			if len(token) > 20 {
				return token, nil
			}
		}

		// Properly poll by asking again if we got an empty response array for either protocol
		var response map[string]interface{}
		if err := json.Unmarshal(payload, &response); err == nil {
			if idFloat, ok := response["id"].(float64); ok {
				id := int(idFloat)
				if id == 2 {
					time.Sleep(500 * time.Millisecond)
					_ = c.WriteMessage(websocket.TextMessage, cdpCookieMsg)
				}
			}
		}
	}

	return "", fmt.Errorf("cookie not found in memory")
}
