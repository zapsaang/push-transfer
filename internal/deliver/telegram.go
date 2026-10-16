package deliver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zapsaang/push-transfer/internal/model"
)

// MaxTelegramTextRunes is the maximum rune count before truncating for Telegram Bot API.
// Telegram's hard limit is 4096 UTF-8 characters; we truncate safely at 4000 chars with "...".
const MaxTelegramTextRunes = 4000

// SafeTruncateTelegramText truncates a string so that its rune count is at most maxRunes,
// appends "...", and ensures no HTML entity (like &amp; or &#39;) is cut in half.
func SafeTruncateTelegramText(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}

	truncated := string(runes[:maxRunes])
	// If truncated in the middle of an HTML entity like &amp; or &#39;
	lastAmp := strings.LastIndex(truncated, "&")
	if lastAmp != -1 {
		lastSemi := strings.LastIndex(truncated, ";")
		if lastSemi < lastAmp {
			truncated = truncated[:lastAmp]
		}
	}
	return strings.TrimRight(strings.TrimSuffix(truncated, "..."), " ") + "..."
}

// TelegramOptions configures TelegramDeliver.
type TelegramOptions struct {
	HTTPClient *http.Client
	BaseURL    string
	BotToken   string
	ChatID     int64
}

// TelegramDeliver delivers formatted messages to Telegram via Bot API.
type TelegramDeliver struct {
	client   *http.Client
	baseURL  string
	botToken string
	chatID   int64
}

// NewTelegramDeliver creates a TelegramDeliver client.
func NewTelegramDeliver(opts TelegramOptions) *TelegramDeliver {
	client := opts.HTTPClient
	if client == nil {
		client = NewDefaultHTTPClient(5 * time.Second)
	}
	baseURL := opts.BaseURL
	if baseURL == "" {
		baseURL = "https://api.telegram.org"
	}
	return &TelegramDeliver{
		client:   client,
		baseURL:  baseURL,
		botToken: opts.BotToken,
		chatID:   opts.ChatID,
	}
}

// Name returns the identifier for TelegramDeliver.
func (t *TelegramDeliver) Name() string {
	return "telegram"
}

// Send formats msg as HTML and posts it to Telegram Bot API.
func (t *TelegramDeliver) Send(ctx context.Context, msg *model.Message) error {
	if msg == nil {
		return nil
	}

	rawText := msg.GenComplexText()
	if utf8.RuneCountInString(rawText) > MaxTelegramTextRunes {
		rawText = string([]rune(rawText)[:MaxTelegramTextRunes]) + "..."
	}

	text := html.EscapeString(rawText)
	if utf8.RuneCountInString(text) > MaxTelegramTextRunes {
		text = SafeTruncateTelegramText(text, MaxTelegramTextRunes)
	}

	payload := map[string]interface{}{
		"chat_id":    t.chatID,
		"text":       text,
		"parse_mode": "HTML",
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal telegram payload: %w", err)
	}

	endpoint := fmt.Sprintf("%s/bot%s/sendMessage", t.baseURL, t.botToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("failed to create telegram request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("telegram request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseDrainBytes))
	if err != nil {
		return fmt.Errorf("failed to read telegram response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("telegram returned status code %d: %s", resp.StatusCode, telegramErrorDetail(body))
	}

	// The Bot API contract is the JSON `ok` field (https://core.telegram.org/bots/api#making-requests):
	// a request can fail with HTTP 200, so the status code alone cannot confirm delivery.
	var apiResp struct {
		OK          bool   `json:"ok"`
		ErrorCode   int    `json:"error_code"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return fmt.Errorf("telegram returned an unparsable response, delivery unverified: %w", err)
	}
	if !apiResp.OK {
		if apiResp.Description != "" {
			return fmt.Errorf("telegram rejected the message (error_code %d): %s", apiResp.ErrorCode, apiResp.Description)
		}
		return fmt.Errorf("telegram rejected the message (error_code %d)", apiResp.ErrorCode)
	}
	return nil
}

// telegramErrorDetail renders the useful part of a Bot API error body without echoing
// an unbounded response into logs.
func telegramErrorDetail(body []byte) string {
	var apiResp struct {
		ErrorCode   int    `json:"error_code"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(body, &apiResp); err != nil || apiResp.Description == "" {
		return "no description"
	}
	return fmt.Sprintf("error_code %d: %s", apiResp.ErrorCode, apiResp.Description)
}
