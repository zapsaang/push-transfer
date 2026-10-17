// Package deliver provides message delivery implementations for Bark, Telegram, SMTP email, and Console.
package deliver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/zapsaang/push-transfer/internal/crypto"
	"github.com/zapsaang/push-transfer/internal/model"
)

// BarkOptions configures BarkDeliver.
type BarkOptions struct {
	HTTPClient *http.Client
	PostURL    string
	DeviceKeys []string
	URLs       []string
	CryptoKey  []byte
	Logger     *slog.Logger
}

// BarkDeliver delivers notifications to Bark iOS clients.
type BarkDeliver struct {
	client     *http.Client
	postURL    string
	deviceKeys []string
	urls       []string
	cryptoKey  []byte
	logger     *slog.Logger
}

func cleanEndpoints(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	var cleaned []string
	for _, item := range items {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		cleaned = append(cleaned, trimmed)
	}
	return cleaned
}

// NewBarkDeliver creates a BarkDeliver client.
func NewBarkDeliver(opts BarkOptions) *BarkDeliver {
	client := opts.HTTPClient
	if client == nil {
		client = NewDefaultHTTPClient(5 * time.Second)
	}
	postURL := strings.TrimSpace(opts.PostURL)
	if postURL == "" {
		postURL = "https://api.day.app/push"
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &BarkDeliver{
		client:     client,
		postURL:    postURL,
		deviceKeys: cleanEndpoints(opts.DeviceKeys),
		urls:       cleanEndpoints(opts.URLs),
		cryptoKey:  opts.CryptoKey,
		logger:     logger,
	}
}

// Name returns the identifier for BarkDeliver.
func (b *BarkDeliver) Name() string {
	return "bark"
}

type barkPostTask struct {
	label     string
	targetURL string
	body      []byte
	isURL     bool
	logTarget string
}

// barkEncryptedEnvelope is the encrypted Bark request body: the ciphertext and IV
// parameters produced by the crypto layer, plus the plaintext device_key the Bark
// server needs to resolve the target device.
type barkEncryptedEnvelope struct {
	crypto.BarkPayload
	DeviceKey string `json:"device_key,omitempty"`
}

func (b *BarkDeliver) buildEndpointPayload(basePayload map[string]interface{}, deviceKey string) ([]byte, error) {
	payload := basePayload
	if deviceKey != "" {
		payload = make(map[string]interface{}, len(basePayload)+1)
		for k, v := range basePayload {
			payload[k] = v
		}
		payload["device_key"] = deviceKey
	}

	if len(b.cryptoKey) > 0 {
		plainJSON, errMarshal := json.Marshal(payload)
		if errMarshal != nil {
			return nil, fmt.Errorf("bark marshal failed: %w", errMarshal)
		}
		encrypted, errEnc := crypto.EncryptBark(plainJSON, b.cryptoKey)
		if errEnc != nil {
			return nil, fmt.Errorf("bark encrypt failed: %w", errEnc)
		}
		postBody, err := json.Marshal(barkEncryptedEnvelope{
			BarkPayload: encrypted,
			DeviceKey:   deviceKey,
		})
		if err != nil {
			return nil, fmt.Errorf("bark payload marshal failed: %w", err)
		}
		return postBody, nil
	}

	postBody, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("bark payload marshal failed: %w", err)
	}
	return postBody, nil
}

// Send formats and delivers msg to Bark endpoints.
func (b *BarkDeliver) Send(ctx context.Context, msg *model.Message) error {
	if msg == nil {
		return nil
	}

	basePayload := map[string]interface{}{
		"isArchive": 1,
		"body":      msg.Content,
	}

	// 推导 Bark 分组逻辑（严格防空值，支持 AppName 特化）
	group := strings.TrimSpace(string(msg.MsgType))
	if msg.MsgType == model.MsgTypeAppNotification {
		if appName := strings.TrimSpace(msg.AppName); appName != "" {
			group = appName
		} else {
			group = string(model.MsgTypeAppNotification)
		}
	}
	if group != "" {
		basePayload["group"] = group
	}

	var copyContent string
	switch msg.MsgType {
	case model.MsgTypeCall:
		basePayload["title"] = fmt.Sprintf("New Call From %s", strings.TrimSpace(msg.Title))
		copyContent = strings.TrimSpace(msg.Title)
	case model.MsgTypeText:
		basePayload["title"] = fmt.Sprintf("New Message From %s", strings.TrimSpace(msg.Title))
		copyContent = strings.TrimSpace(msg.CaptchaCode)
	case model.MsgTypeAppNotification:
		appName := strings.TrimSpace(msg.AppName)
		title := strings.TrimSpace(msg.Title)
		switch {
		case appName != "" && title != "":
			basePayload["title"] = fmt.Sprintf("[%s] %s", appName, title)
		case appName != "":
			basePayload["title"] = fmt.Sprintf("[%s]", appName)
		case title != "":
			basePayload["title"] = fmt.Sprintf("New Notification From %s", title)
		default:
			basePayload["title"] = "New Notification"
		}
		copyContent = strings.TrimSpace(msg.CaptchaCode)
	default:
		if title := strings.TrimSpace(msg.Title); title != "" {
			basePayload["title"] = title
		}
		copyContent = strings.TrimSpace(msg.CaptchaCode)
	}

	if copyContent != "" {
		basePayload["autoCopy"] = 1
		basePayload["copy"] = copyContent
	}
	if msg.Emergency {
		basePayload["level"] = "critical"
		// Bark reads `volume` as a *string* holding 0-10 (default 5, i.e. 0.5 amplitude);
		// a JSON number is ignored by the iOS client, silently falling back to 5.
		// See LevelProcessor.swift setCriticalSound / bark.day.app params#volume.
		basePayload["volume"] = "5"
	}

	var errs []error
	tasks := make([]barkPostTask, 0, len(b.deviceKeys)+len(b.urls))

	for _, devKey := range b.deviceKeys {
		postBody, err := b.buildEndpointPayload(basePayload, devKey)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		tasks = append(tasks, barkPostTask{
			label:     fmt.Sprintf("device_key %s", devKey),
			targetURL: b.postURL,
			body:      postBody,
			isURL:     false,
			logTarget: devKey,
		})
	}

	for _, targetURL := range b.urls {
		postBody, err := b.buildEndpointPayload(basePayload, "")
		if err != nil {
			errs = append(errs, err)
			continue
		}
		tasks = append(tasks, barkPostTask{
			label:     fmt.Sprintf("url %s", targetURL),
			targetURL: targetURL,
			body:      postBody,
			isURL:     true,
			logTarget: targetURL,
		})
	}

	results := make([]error, len(tasks))
	if len(tasks) == 1 {
		results[0] = b.postJSON(ctx, tasks[0].targetURL, tasks[0].body)
	} else if len(tasks) > 1 {
		var wg sync.WaitGroup
		wg.Add(len(tasks))
		for i, task := range tasks {
			go func(idx int, t barkPostTask) {
				defer wg.Done()
				results[idx] = b.postJSON(ctx, t.targetURL, t.body)
			}(i, task)
		}
		wg.Wait()
	}

	var successCount int
	for i, task := range tasks {
		if err := results[i]; err != nil {
			if task.isURL {
				b.logger.Warn("bark post to url failed", "url", task.logTarget, "error", err)
			} else {
				b.logger.Warn("bark post to device_key failed", "device_key", task.logTarget, "error", err)
			}
			errs = append(errs, fmt.Errorf("%s: %w", task.label, err))
		} else {
			successCount++
		}
	}

	totalEndpoints := len(b.deviceKeys) + len(b.urls)
	if totalEndpoints > 0 && successCount == 0 {
		return fmt.Errorf("all bark endpoints failed: %w", errors.Join(errs...))
	}

	return nil
}

func (b *BarkDeliver) postJSON(ctx context.Context, targetURL string, data []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseDrainBytes))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}
	return nil
}
