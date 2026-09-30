package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Validate + execute. Model proposes; app disposes.

func ValidateSaveTask(args map[string]any) (title, notes string, err error) {
	t, _ := args["title"].(string)
	t = strings.TrimSpace(t)
	if t == "" {
		return "", "", fmt.Errorf("save_task.title is required")
	}
	if len(t) > 200 {
		return "", "", fmt.Errorf("save_task.title too long (max 200)")
	}
	n, _ := args["notes"].(string)
	if len(n) > 2000 {
		return "", "", fmt.Errorf("save_task.notes too long (max 2000)")
	}
	return t, strings.TrimSpace(n), nil
}

func ValidateSendSummary(args map[string]any) (string, error) {
	t, _ := args["text"].(string)
	t = strings.TrimSpace(t)
	if t == "" {
		return "", fmt.Errorf("send_summary.text is required")
	}
	if len(t) > 1500 {
		t = t[:1500]
	}
	return t, nil
}

// SendSummary posts to Discord webhook if configured; otherwise records as skipped (no crash, no secret leak).
func SendSummary(ctx context.Context, webhookURL, text string) (string, error) {
	if webhookURL == "" {
		return "skipped: no notification channel configured (set DISCORD_WEBHOOK_URL)", nil
	}
	payload, _ := json.Marshal(map[string]string{"content": text})
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", webhookURL, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("webhook error: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("webhook status %d", resp.StatusCode)
	}
	return "sent to notification channel", nil
}
