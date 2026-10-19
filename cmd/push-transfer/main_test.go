package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zapsaang/push-transfer/internal/config"
	"github.com/zapsaang/push-transfer/internal/idempotency"
	"github.com/zapsaang/push-transfer/internal/ingress/webhook"
	"github.com/zapsaang/push-transfer/internal/model"
)

func TestBuildDispatcher_AllChannels(t *testing.T) {
	cfg := &config.Config{
		Bark: &config.BarkConfig{
			DeviceKeys: []string{"dev-key-1"},
			URLs:       []string{"https://bark.example.com"},
			PostURL:    "https://api.day.app/push",
		},
		Crypto: &config.CryptoConfig{
			Key: []byte("1234567890123456"),
		},
		Telegram: &config.TelegramConfig{
			BaseURL:  "https://api.telegram.org",
			BotToken: "123:token",
			ChatID:   123456,
		},
		Email: &config.EmailConfig{
			SenderEmail:   "sender@example.com",
			ReceiverEmail: "recv@example.com",
			Password:      "pass",
			SMTPServer:    "smtp.example.com",
			SMTPPort:      465,
		},
	}

	dispatcher := buildDispatcher(cfg)
	if dispatcher == nil {
		t.Fatal("expected non-nil dispatcher")
	}
}

func TestBuildDispatcher_ConsoleOnly(t *testing.T) {
	cfg := &config.Config{}

	dispatcher := buildDispatcher(cfg)
	if dispatcher == nil {
		t.Fatal("expected non-nil dispatcher")
	}
}

// credentialEnv renders a single-token AUTH_CREDENTIALS value.
func credentialEnv(name, token string) string {
	return fmt.Sprintf(`[{"name":%q,"token":%q}]`, name, token)
}

// basicCredentialEnv renders a single Basic-pair AUTH_CREDENTIALS value.
func basicCredentialEnv(name, user, pass string) string {
	return fmt.Sprintf(`[{"name":%q,"username":%q,"password":%q}]`, name, user, pass)
}

func TestRun_VersionFlag(t *testing.T) {
	t.Run("-version flag", func(t *testing.T) {
		var buf bytes.Buffer
		err := run(context.Background(), []string{"-version"}, &buf, nil)
		if err != nil {
			t.Fatalf("run failed: %v", err)
		}
		expected := fmt.Sprintf("push-transfer version %s\n", Version)
		if buf.String() != expected {
			t.Errorf("expected %q, got %q", expected, buf.String())
		}
	})

	t.Run("-v flag", func(t *testing.T) {
		var buf bytes.Buffer
		err := run(context.Background(), []string{"-v"}, &buf, nil)
		if err != nil {
			t.Fatalf("run failed: %v", err)
		}
		expected := fmt.Sprintf("push-transfer version %s\n", Version)
		if buf.String() != expected {
			t.Errorf("expected %q, got %q", expected, buf.String())
		}
	})

	t.Run("invalid flag", func(t *testing.T) {
		var buf bytes.Buffer
		err := run(context.Background(), []string{"-unknown-flag"}, &buf, nil)
		if err == nil {
			t.Fatal("expected error on unknown flag, got nil")
		}
	})

	t.Run("-h returns flag.ErrHelp so the caller can exit cleanly", func(t *testing.T) {
		var buf bytes.Buffer
		err := run(context.Background(), []string{"-h"}, &buf, nil)
		if !errors.Is(err, flag.ErrHelp) {
			t.Fatalf("expected flag.ErrHelp, got %v", err)
		}
		if !isCleanExit(err) {
			t.Error("expected -h to be classified as a clean exit (exit code 0)")
		}
		if !strings.Contains(buf.String(), "Usage of push-transfer") {
			t.Errorf("expected usage output, got %q", buf.String())
		}
	})

	t.Run("failures are not clean exits", func(t *testing.T) {
		if isCleanExit(nil) {
			t.Error("nil must not be reported as a clean exit through isCleanExit")
		}
		if isCleanExit(errors.New("boom")) {
			t.Error("a real error must not be reported as a clean exit")
		}
		if !isCleanExit(fmt.Errorf("wrapped: %w", flag.ErrHelp)) {
			t.Error("a wrapped flag.ErrHelp must still be a clean exit")
		}
	})
}

func TestRun_FullE2E_WebhookFlow(t *testing.T) {
	// 1. Setup Mock Bark Server
	var (
		barkMu       sync.Mutex
		barkPayloads []map[string]interface{}
		barkFailing  int32
	)

	mockBark := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.LoadInt32(&barkFailing) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"code":500,"message":"internal error"}`))
			return
		}

		b, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var payload map[string]interface{}
		if err := json.Unmarshal(b, &payload); err == nil {
			barkMu.Lock()
			barkPayloads = append(barkPayloads, payload)
			barkMu.Unlock()
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":200,"message":"success"}`))
	}))
	defer mockBark.Close()

	// 2. Setup Mock Telegram Server
	var (
		tgMu       sync.Mutex
		tgPayloads []map[string]interface{}
		tgFailing  int32
	)

	mockTelegram := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.LoadInt32(&tgFailing) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"ok":false,"description":"telegram unavailable"}`))
			return
		}

		b, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var payload map[string]interface{}
		if err := json.Unmarshal(b, &payload); err == nil {
			tgMu.Lock()
			tgPayloads = append(tgPayloads, payload)
			tgMu.Unlock()
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer mockTelegram.Close()

	const authToken = "secret-token-e2e-2026"
	envMap := map[string]string{
		"PORT":                 "0", // Assign ephemeral OS port
		"RUN_MODE":             "webhook",
		"AUTH_CREDENTIALS":     credentialEnv("e2e", authToken),
		"BARK_DEVICE_KEY_LIST": "bark-test-device-key",
		"BARK_POST_URL":        mockBark.URL + "/push",
		"TELEGRAM_BOT_TOKEN":   "123456:ABC-DEF",
		"TELEGRAM_CHAT_ID":     "987654321",
		"TELEGRAM_API_URL":     mockTelegram.URL,
		"REGEX_FILTER_WHITE":   `["^重要通知", "(?i)whitelist"]`,
		"REGEX_FILTER_BLACK":   `["^垃圾短信", "(?i)spam"]`,
		"REGEX_CAPTCHA":        `["验证码[是为：:]\\s*(\\d{4,6})", "code[: ]+(\\d{6})"]`,
		"REGEX_EMERGENCY":      `["(?i)urgent", "(?i)emergency", "报警"]`,
	}
	lookup := func(k string) string {
		return envMap[k]
	}

	readyCh := make(chan string, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- run(ctx, nil, io.Discard, lookup, readyCh)
	}()

	var serverAddr string
	select {
	case serverAddr = <-readyCh:
	case err := <-runErrCh:
		t.Fatalf("run failed before listening: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for server to listen")
	}

	baseURL := fmt.Sprintf("http://%s", serverAddr)
	client := &http.Client{Timeout: 5 * time.Second}

	// Helper for sending POST /webhook
	postWebhook := func(body string, token string) (*http.Response, map[string]interface{}) {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, baseURL+"/webhook", strings.NewReader(body))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("POST /webhook failed: %v", err)
		}

		respBytes, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		var res map[string]interface{}
		_ = json.Unmarshal(respBytes, &res)
		return resp, res
	}

	// 0. Health check
	{
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, baseURL+"/healthz", http.NoBody)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET /healthz failed: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK from healthz, got %d", resp.StatusCode)
		}
	}

	// 1. Normal message delivery -> assert delivery to Bark & Telegram, HTTP 200 {"status":"success"}
	{
		body := `{"msg_type":"text","title":"Alice","content":"Hello E2E Test"}`
		resp, res := postWebhook(body, authToken)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("normal message: expected 200, got %d", resp.StatusCode)
		}
		if res["status"] != "success" {
			t.Errorf("normal message: expected status success, got %v", res["status"])
		}

		barkMu.Lock()
		if len(barkPayloads) != 1 {
			t.Fatalf("expected 1 Bark payload, got %d", len(barkPayloads))
		}
		lastBark := barkPayloads[0]
		barkMu.Unlock()

		if lastBark["body"] != "Hello E2E Test" {
			t.Errorf("Bark body mismatch: %v", lastBark["body"])
		}
		if lastBark["title"] != "New Message From Alice" {
			t.Errorf("Bark title mismatch: %v", lastBark["title"])
		}

		tgMu.Lock()
		if len(tgPayloads) != 1 {
			t.Fatalf("expected 1 Telegram payload, got %d", len(tgPayloads))
		}
		lastTG := tgPayloads[0]
		tgMu.Unlock()

		if lastTG["chat_id"] != float64(987654321) {
			t.Errorf("Telegram chat_id mismatch: %v", lastTG["chat_id"])
		}
		tgText, _ := lastTG["text"].(string)
		if !strings.Contains(tgText, "Hello E2E Test") {
			t.Errorf("Telegram text does not contain message: %s", tgText)
		}
	}

	// 2. Verification code SMS -> assert captcha extracted and sent with autoCopy to Bark
	{
		body := `{"msg_type":"text","title":"Bank","content":"您的验证码是 654321，5分钟内有效。"}`
		resp, res := postWebhook(body, authToken)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("captcha message: expected 200, got %d", resp.StatusCode)
		}
		if res["status"] != "success" {
			t.Errorf("captcha message: expected status success, got %v", res["status"])
		}

		barkMu.Lock()
		if len(barkPayloads) != 2 {
			t.Fatalf("expected 2 Bark payloads, got %d", len(barkPayloads))
		}
		lastBark := barkPayloads[1]
		barkMu.Unlock()

		if lastBark["autoCopy"] != float64(1) {
			t.Errorf("expected autoCopy 1, got %v", lastBark["autoCopy"])
		}
		if lastBark["copy"] != "654321" {
			t.Errorf("expected copy '654321', got %v", lastBark["copy"])
		}
	}

	// 3. Emergency SMS -> assert critical level sent to Bark
	{
		body := `{"msg_type":"text","title":"Alert","content":"This is an urgent emergency notification!"}`
		resp, res := postWebhook(body, authToken)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("emergency message: expected 200, got %d", resp.StatusCode)
		}
		if res["status"] != "success" {
			t.Errorf("emergency message: expected status success, got %v", res["status"])
		}

		barkMu.Lock()
		if len(barkPayloads) != 3 {
			t.Fatalf("expected 3 Bark payloads, got %d", len(barkPayloads))
		}
		lastBark := barkPayloads[2]
		barkMu.Unlock()

		if lastBark["level"] != "critical" {
			t.Errorf("expected level critical, got %v", lastBark["level"])
		}
		if lastBark["volume"] != "5" {
			t.Errorf("expected the documented string volume \"5\", got %#v", lastBark["volume"])
		}
	}

	// 4. Malicious RCE injection string -> assert treated purely as plain text, no execution, correctly delivered
	{
		rceInjection := "; id; rm -rf /; echo $PATH; `whoami` $(pwd)"
		body := fmt.Sprintf(`{"msg_type":"text","title":%q,"content":%q}`, rceInjection, rceInjection)
		resp, res := postWebhook(body, authToken)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("RCE string message: expected 200, got %d", resp.StatusCode)
		}
		if res["status"] != "success" {
			t.Errorf("RCE string message: expected status success, got %v", res["status"])
		}

		barkMu.Lock()
		if len(barkPayloads) != 4 {
			t.Fatalf("expected 4 Bark payloads, got %d", len(barkPayloads))
		}
		lastBark := barkPayloads[3]
		barkMu.Unlock()

		// Verify content was retained as pure plain text without execution
		if lastBark["body"] != rceInjection {
			t.Errorf("RCE body mismatch: expected %q, got %v", rceInjection, lastBark["body"])
		}

		tgMu.Lock()
		if len(tgPayloads) != 4 {
			t.Fatalf("expected 4 Telegram payloads, got %d", len(tgPayloads))
		}
		lastTG := tgPayloads[3]
		tgMu.Unlock()

		tgText, _ := lastTG["text"].(string)
		// Must not have executed anything, must contain text (HTML escaped)
		if !strings.Contains(tgText, "id; rm -rf") {
			t.Errorf("Telegram payload missing expected text: %s", tgText)
		}
	}

	// 5. Blacklisted SMS -> assert HTTP 200 {"status":"blocked"}, not delivered to downstreams
	{
		barkCountBefore := len(barkPayloads)
		tgCountBefore := len(tgPayloads)

		body := `{"msg_type":"text","title":"AdService","content":"垃圾短信：优惠充值送手机，限时点击"}`
		resp, res := postWebhook(body, authToken)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("blacklisted message: expected 200, got %d", resp.StatusCode)
		}
		if res["status"] != "blocked" {
			t.Errorf("blacklisted message: expected status blocked, got %v", res["status"])
		}

		barkMu.Lock()
		barkCountAfter := len(barkPayloads)
		barkMu.Unlock()

		tgMu.Lock()
		tgCountAfter := len(tgPayloads)
		tgMu.Unlock()

		if barkCountAfter != barkCountBefore {
			t.Errorf("Bark should not receive blocked message: before=%d, after=%d", barkCountBefore, barkCountAfter)
		}
		if tgCountAfter != tgCountBefore {
			t.Errorf("Telegram should not receive blocked message: before=%d, after=%d", tgCountBefore, tgCountAfter)
		}
	}

	// 6. Whitelist overriding blacklist -> assert HTTP 200 {"status":"success"}, delivered to downstreams
	{
		barkMu.Lock()
		barkCountBefore := len(barkPayloads)
		barkMu.Unlock()

		body := `{"msg_type":"text","title":"Alert","content":"重要通知：本消息含有垃圾短信关键字，但由于命中白名单应当放行"}`
		resp, res := postWebhook(body, authToken)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("whitelisted message: expected 200, got %d", resp.StatusCode)
		}
		if res["status"] != "success" {
			t.Errorf("whitelisted message: expected status success, got %v", res["status"])
		}

		barkMu.Lock()
		barkCountAfter := len(barkPayloads)
		barkMu.Unlock()

		if barkCountAfter != barkCountBefore+1 {
			t.Errorf("expected Bark delivery count to increment for whitelisted message")
		}
	}

	// 7. Invalid Token -> assert HTTP 401
	{
		body := `{"msg_type":"text","title":"test","content":"hello"}`
		resp, _ := postWebhook(body, "wrong-token")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401 with wrong token, got %d", resp.StatusCode)
		}

		resp, _ = postWebhook(body, "")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401 with missing token, got %d", resp.StatusCode)
		}
	}

	// 8. Malformed JSON -> assert HTTP 400
	{
		resp, _ := postWebhook(`{malformed-json`, authToken)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400 with malformed JSON, got %d", resp.StatusCode)
		}
	}

	// 8.5 Invalid or missing msg_type -> assert HTTP 400 Bad Request
	{
		body := `{"msg_type":"invalid_type","title":"bad"}`
		resp, res := postWebhook(body, authToken)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400 with invalid msg_type, got %d", resp.StatusCode)
		}
		if res["status"] != "failed" || res["message"] != "invalid or missing msg_type" {
			t.Errorf("expected failed status with 'invalid or missing msg_type', got: %v", res)
		}
	}

	// 8.6 App notification delivery -> assert group = app_name, title = [app_name] title, and Telegram text
	{
		body := `{"msg_type":"app_notification","app_name":"企业微信","title":"项目汇报","content":"代码重构已通过验证"}`
		resp, res := postWebhook(body, authToken)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("app notification message: expected 200, got %d", resp.StatusCode)
		}
		if res["status"] != "success" {
			t.Errorf("app notification message: expected status success, got %v", res["status"])
		}

		barkMu.Lock()
		lastBark := barkPayloads[len(barkPayloads)-1]
		barkMu.Unlock()

		if lastBark["group"] != "企业微信" {
			t.Errorf("Bark group mismatch: expected '企业微信', got %v", lastBark["group"])
		}
		if lastBark["title"] != "[企业微信] 项目汇报" {
			t.Errorf("Bark title mismatch: expected '[企业微信] 项目汇报', got %v", lastBark["title"])
		}

		tgMu.Lock()
		lastTG := tgPayloads[len(tgPayloads)-1]
		tgMu.Unlock()

		tgText, _ := lastTG["text"].(string)
		if !strings.Contains(tgText, "【企业微信】项目汇报") || !strings.Contains(tgText, "代码重构已通过验证") {
			t.Errorf("Telegram app_notification text mismatch: %s", tgText)
		}
	}

	// 9. Downstreams all failing -> assert HTTP 502 {"status":"deliver_failed"}
	{
		atomic.StoreInt32(&barkFailing, 1)
		atomic.StoreInt32(&tgFailing, 1)

		body := `{"msg_type":"text","title":"DeliveryFail","content":"All downstreams will fail"}`
		resp, res := postWebhook(body, authToken)
		if resp.StatusCode != http.StatusBadGateway {
			t.Errorf("expected 502 Bad Gateway when all downstreams fail, got %d", resp.StatusCode)
		}
		if res["status"] != "deliver_failed" {
			t.Errorf("expected status 'deliver_failed', got %v", res["status"])
		}

		atomic.StoreInt32(&barkFailing, 0)
		atomic.StoreInt32(&tgFailing, 0)
	}

	// 10. Graceful shutdown via context cancellation
	cancel()
	select {
	case err := <-runErrCh:
		if err != nil {
			t.Fatalf("expected nil on graceful shutdown, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for graceful shutdown")
	}
}

func TestRun_ConfigErrors(t *testing.T) {
	t.Run("invalid port configuration", func(t *testing.T) {
		env := map[string]string{
			"RUN_MODE": "webhook",
			"PORT":     "invalid-port",
		}
		err := run(context.Background(), nil, io.Discard, func(k string) string { return env[k] })
		if err == nil {
			t.Fatal("expected error with invalid port, got nil")
		}
		if !strings.Contains(err.Error(), "PORT") {
			t.Errorf("expected error to mention PORT, got: %v", err)
		}
	})

	t.Run("invalid regex configuration", func(t *testing.T) {
		env := map[string]string{
			"RUN_MODE":           "webhook",
			"AUTH_CREDENTIALS":   credentialEnv("probe", "token"),
			"REGEX_FILTER_BLACK": `["[unclosed-bracket"]`,
		}
		err := run(context.Background(), nil, io.Discard, func(k string) string { return env[k] })
		if err == nil {
			t.Fatal("expected error with invalid regex, got nil")
		}
		if !strings.Contains(err.Error(), "failed to initialize processing pipeline") {
			t.Errorf("expected pipeline initialization error, got: %v", err)
		}
	})

	t.Run("both mode missing IMAP and credentials", func(t *testing.T) {
		env := map[string]string{
			"RUN_MODE": "both",
			// IMAP not configured and AUTH_CREDENTIALS unset
		}
		err := run(context.Background(), nil, io.Discard, func(k string) string { return env[k] })
		if err == nil {
			t.Fatal("expected error when RUN_MODE=both has no credentials/IMAP, got nil")
		}
		if !strings.Contains(err.Error(), "RUN_MODE=both") {
			t.Errorf("expected error to mention RUN_MODE=both, got: %v", err)
		}
	})

	t.Run("invalid RUN_MODE value", func(t *testing.T) {
		env := map[string]string{
			"RUN_MODE": "yes",
		}
		err := run(context.Background(), nil, io.Discard, func(k string) string { return env[k] })
		if err == nil {
			t.Fatal("expected error for invalid RUN_MODE, got nil")
		}
		if !strings.Contains(err.Error(), "RUN_MODE must be one of webhook, imap, both") {
			t.Errorf("expected RUN_MODE validation error, got: %v", err)
		}
	})

	t.Run("port bind conflict error", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = ln.Close() }()

		_, portStr, _ := net.SplitHostPort(ln.Addr().String())
		env := map[string]string{
			"PORT":             portStr,
			"RUN_MODE":         "webhook",
			"AUTH_CREDENTIALS": credentialEnv("probe", "token"),
		}
		err = run(context.Background(), nil, io.Discard, func(k string) string { return env[k] })
		if err == nil {
			t.Fatal("expected error on port collision, got nil")
		}
		if !strings.Contains(err.Error(), "failed to bind HTTP server") {
			t.Errorf("expected port bind error, got: %v", err)
		}
	})

	t.Run("invalid AUTH_CREDENTIALS configuration", func(t *testing.T) {
		env := map[string]string{
			"RUN_MODE":         "webhook",
			"AUTH_CREDENTIALS": `[{"token":"   "}]`,
		}
		err := run(context.Background(), nil, io.Discard, func(k string) string { return env[k] })
		if err == nil {
			t.Fatal("expected error with invalid AUTH_CREDENTIALS, got nil")
		}
		if !strings.Contains(err.Error(), "AUTH_CREDENTIALS") {
			t.Errorf("expected startup error to name the offending variable, got: %v", err)
		}
	})

	t.Run("malformed AUTH_CREDENTIALS failure never echoes credential material", func(t *testing.T) {
		const secret = "leaked-startup-secret"
		env := map[string]string{
			"RUN_MODE":         "webhook",
			"AUTH_CREDENTIALS": `[{"token":"` + secret + `"}] trailing`,
		}
		err := run(context.Background(), nil, io.Discard, func(k string) string { return env[k] })
		if err == nil {
			t.Fatal("expected error with malformed AUTH_CREDENTIALS, got nil")
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("startup error leaked credential material: %v", err)
		}
		if !strings.Contains(err.Error(), "AUTH_CREDENTIALS") {
			t.Errorf("expected startup error to name the offending variable, got: %v", err)
		}
	})
}

func TestRun_IMAPTriggerFlow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately to test IMAP startup and clean exit
	cancel()

	env := map[string]string{
		"RUN_MODE":                  "imap",
		"EMAIL_TRIGGER_LOGIN_EMAIL": "test@example.com",
		"EMAIL_TRIGGER_PASSWORD":    "secret",
		"EMAIL_TRIGGER_IMAP_SERVER": "127.0.0.1",
		"EMAIL_TRIGGER_IMAP_PORT":   "0",
	}

	err := run(ctx, nil, io.Discard, func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("expected clean exit for cancelled context, got: %v", err)
	}
}

func TestGracefulShutdown(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	server := newHTTPServer(mux, slog.New(slog.NewTextHandler(io.Discard, nil)))

	go func() {
		_ = server.Serve(listener)
	}()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("graceful shutdown failed: %v", err)
	}
}

func TestCallMessageFlow(t *testing.T) {
	cfg := &config.Config{
		Bark: &config.BarkConfig{
			DeviceKeys: []string{"dev-key"},
			PostURL:    "https://api.day.app/push",
		},
	}
	dispatcher := buildDispatcher(cfg)
	if dispatcher == nil {
		t.Fatal("expected non-nil dispatcher")
	}

	callMsg := &model.Message{
		MsgType: model.MsgTypeCall,
		Title:   "13800138000",
		Content: "missed call",
	}

	// Ensure GenComplexText runs on call msg
	text := callMsg.GenComplexText()
	if !strings.Contains(text, "13800138000") {
		t.Errorf("expected call text to contain phone number: %s", text)
	}
}

func TestRun_HTTPServerGoroutineTrackedInWaitGroup(t *testing.T) {
	var buf bytes.Buffer
	readyCh := make(chan string, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	env := map[string]string{
		"PORT":             "0",
		"RUN_MODE":         "webhook",
		"AUTH_CREDENTIALS": credentialEnv("wg", "waitgroup-token"),
	}
	lookup := func(k string) string { return env[k] }

	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, nil, &buf, lookup, readyCh)
	}()

	select {
	case <-readyCh:
	case err := <-errCh:
		t.Fatalf("run exited before ready: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for server ready")
	}

	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("expected nil on shutdown, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for run() to return")
	}

	// Reading buf.String() immediately after run() returns must be race-free
	// and contain the HTTP goroutine completion log.
	output := buf.String()
	if !strings.Contains(output, "HTTP webhook server stopped") {
		t.Errorf("expected log to contain 'HTTP webhook server stopped', got:\n%s", output)
	}
	if !strings.Contains(output, "push-transfer stopped cleanly") {
		t.Errorf("expected log to contain 'push-transfer stopped cleanly', got:\n%s", output)
	}
}

func TestNewHTTPServer_Timeouts(t *testing.T) {
	mux := http.NewServeMux()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := newHTTPServer(mux, logger)
	if srv == nil {
		t.Fatal("expected non-nil *http.Server")
	}
	if srv.Handler != mux {
		t.Errorf("expected Handler to be mux")
	}
	if srv.ReadHeaderTimeout != 5*time.Second {
		t.Errorf("expected ReadHeaderTimeout=5s, got %v", srv.ReadHeaderTimeout)
	}
	if srv.ReadTimeout != 10*time.Second {
		t.Errorf("expected ReadTimeout=10s, got %v", srv.ReadTimeout)
	}
	if srv.WriteTimeout != 10*time.Second {
		t.Errorf("expected WriteTimeout=10s, got %v", srv.WriteTimeout)
	}
	if srv.IdleTimeout != 60*time.Second {
		t.Errorf("expected IdleTimeout=60s, got %v", srv.IdleTimeout)
	}
	// The std logger would bypass the structured logging pipeline.
	if srv.ErrorLog == nil {
		t.Fatal("expected ErrorLog to be wired to the injected slog logger")
	}
	if srv.ErrorLog == log.Default() {
		t.Error("expected ErrorLog not to be the standard library default logger")
	}

	// A nil logger must fall back to the process default instead of panicking.
	if fallback := newHTTPServer(mux, nil); fallback.ErrorLog == nil {
		t.Error("expected ErrorLog to be wired even without an injected logger")
	}
}

func TestBuildDispatcher_PartialConfigWarnings(t *testing.T) {
	t.Run("buildDispatcher logs warnings for deprecated IV, partial Telegram, and partial Email", func(t *testing.T) {
		var buf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

		cfg := &config.Config{
			Crypto: &config.CryptoConfig{
				DeprecatedIVConfigured: true,
			},
			Telegram: &config.TelegramConfig{
				BotToken: "partial-bot-token",
				ChatID:   0,
			},
			Email: &config.EmailConfig{
				SenderEmail:   "sender@example.com",
				ReceiverEmail: "",
			},
		}

		dispatcher := buildDispatcher(cfg, logger)
		if dispatcher == nil {
			t.Fatal("expected non-nil dispatcher")
		}

		logs := buf.String()
		expectedWarnings := []string{
			"AES_128_ECB_IV is deprecated and ignored; random IV is generated per message",
			"partial telegram configuration detected; telegram target disabled",
			"partial email configuration detected; email target disabled",
		}
		for _, want := range expectedWarnings {
			if !strings.Contains(logs, want) {
				t.Errorf("expected logs to contain %q, got:\n%s", want, logs)
			}
		}
	})

	t.Run("run logs all partial config warnings including ignored partial IMAP in webhook mode", func(t *testing.T) {
		var buf bytes.Buffer
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		env := map[string]string{
			"PORT":                       "0",
			"RUN_MODE":                   "webhook",
			"AUTH_CREDENTIALS":           credentialEnv("valid", "valid-token"),
			"EMAIL_TRIGGER_LOGIN_EMAIL":  "only-imap-user@example.com",
			"TELEGRAM_BOT_TOKEN":         "only-tg-token",
			"EMAIL_DELIVER_SENDER_EMAIL": "only-sender@example.com",
			"AES_128_ECB_IV":             "1234567890123456",
		}
		lookup := func(k string) string { return env[k] }

		if err := run(ctx, nil, &buf, lookup); err != nil {
			t.Fatalf("expected clean exit, got: %v", err)
		}

		logs := buf.String()
		expectedWarnings := []string{
			"AES_128_ECB_IV is deprecated and ignored; random IV is generated per message",
			"IMAP configuration is present but ignored: RUN_MODE=webhook does not start the IMAP listener; use RUN_MODE=both",
			"partial telegram configuration detected; telegram target disabled",
			"partial email configuration detected; email target disabled",
		}
		for _, want := range expectedWarnings {
			if count := strings.Count(logs, want); count != 1 {
				t.Errorf("expected warning %q exactly once, got %d times in logs:\n%s", want, count, logs)
			}
		}
	})

	t.Run("webhook and IMAP both start and authenticate with Basic Auth under RUN_MODE=both", func(t *testing.T) {
		var buf bytes.Buffer
		readyCh := make(chan string, 1)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		env := map[string]string{
			"PORT":                      "0",
			"RUN_MODE":                  "both",
			"AUTH_CREDENTIALS":          basicCredentialEnv("webhook-basic", "webhook-admin", "webhook-password"),
			"EMAIL_TRIGGER_LOGIN_EMAIL": "test@example.com",
			"EMAIL_TRIGGER_PASSWORD":    "secret",
			"EMAIL_TRIGGER_IMAP_SERVER": "127.0.0.1",
			"EMAIL_TRIGGER_IMAP_PORT":   "0",
		}
		lookup := func(k string) string { return env[k] }

		errCh := make(chan error, 1)
		go func() {
			errCh <- run(ctx, nil, &buf, lookup, readyCh)
		}()

		var addr string
		select {
		case addr = <-readyCh:
		case err := <-errCh:
			t.Fatalf("expected webhook server to start with Basic Auth, but run returned: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for webhook server to start")
		}

		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+addr+"/webhook", strings.NewReader(`{"msg_type":"text","title":"BasicAuth","content":"Hello"}`))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.SetBasicAuth("webhook-admin", "webhook-password")

		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("POST /webhook failed: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200 OK with valid Basic Auth, got %d", resp.StatusCode)
		}

		cancel()
		if err := <-errCh; err != nil {
			t.Fatalf("expected nil on shutdown, got: %v", err)
		}
	})
}

func TestRun_ConcurrentIsolatedLoggers(t *testing.T) {
	var buf1, buf2 bytes.Buffer
	ready1 := make(chan string, 1)
	ready2 := make(chan string, 1)

	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	env1 := map[string]string{
		"PORT":                 "0",
		"RUN_MODE":             "webhook",
		"AUTH_CREDENTIALS":     credentialEnv("i1", "token-instance-1"),
		"BARK_DEVICE_KEY_LIST": "instance-1-bark-key",
	}
	env2 := map[string]string{
		"PORT":               "0",
		"RUN_MODE":           "webhook",
		"AUTH_CREDENTIALS":   credentialEnv("i2", "token-instance-2"),
		"TELEGRAM_BOT_TOKEN": "instance-2-tg-token",
		"TELEGRAM_CHAT_ID":   "777888999",
	}

	errCh1 := make(chan error, 1)
	errCh2 := make(chan error, 1)

	go func() {
		errCh1 <- run(ctx1, nil, &buf1, func(k string) string { return env1[k] }, ready1)
	}()
	go func() {
		errCh2 <- run(ctx2, nil, &buf2, func(k string) string { return env2[k] }, ready2)
	}()

	var addr1, addr2 string
	select {
	case addr1 = <-ready1:
	case err := <-errCh1:
		t.Fatalf("instance 1 exited early: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for instance 1")
	}

	select {
	case addr2 = <-ready2:
	case err := <-errCh2:
		t.Fatalf("instance 2 exited early: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for instance 2")
	}

	cancel1()
	cancel2()

	if err := <-errCh1; err != nil {
		t.Fatalf("instance 1 returned error: %v", err)
	}
	if err := <-errCh2; err != nil {
		t.Fatalf("instance 2 returned error: %v", err)
	}

	out1 := buf1.String()
	out2 := buf2.String()

	if !strings.Contains(out1, "name=bark") {
		t.Errorf("expected instance 1 log to contain 'name=bark', got:\n%s", out1)
	}
	if strings.Contains(out1, "chat_id=777888999") || strings.Contains(out1, addr2) {
		t.Errorf("instance 1 log was polluted by instance 2:\n%s", out1)
	}

	if !strings.Contains(out2, "chat_id=777888999") {
		t.Errorf("expected instance 2 log to contain 'chat_id=777888999', got:\n%s", out2)
	}
	if strings.Contains(out2, "name=bark") || strings.Contains(out2, addr1) {
		t.Errorf("instance 2 log was polluted by instance 1:\n%s", out2)
	}
}

func TestRun_TrustedProxiesAndPipelineLoggerWired(t *testing.T) {
	var buf bytes.Buffer
	readyCh := make(chan string, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Configure TRUSTED_PROXIES to only trust 198.51.100.0/24 (excluding 127.0.0.1),
	// and configure REGEX_FILTER_BLACK to trigger pipeline filter logging.
	env := map[string]string{
		"PORT":               "0",
		"RUN_MODE":           "webhook",
		"AUTH_CREDENTIALS":   credentialEnv("wired", "wired-test-token"),
		"TRUSTED_PROXIES":    "198.51.100.0/24",
		"REGEX_FILTER_BLACK": `["^blocked_word"]`,
	}
	lookup := func(k string) string { return env[k] }

	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, nil, &buf, lookup, readyCh)
	}()

	var addr string
	select {
	case addr = <-readyCh:
	case err := <-errCh:
		t.Fatalf("run exited early: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for server")
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+addr+"/webhook", strings.NewReader(`{"msg_type":"text","title":"SpamSender","content":"blocked_word in sms"}`))
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer wired-test-token")
	req.Header.Set("X-Forwarded-For", "93.184.216.34")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /webhook failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for blocked message, got %d", resp.StatusCode)
	}

	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("expected nil on shutdown, got: %v", err)
	}

	logs := buf.String()
	// 1. Because TRUSTED_PROXIES="198.51.100.0/24" does not include 127.0.0.1,
	// spoofed X-Forwarded-For "93.184.216.34" from 127.0.0.1 must be ignored.
	if strings.Contains(logs, "93.184.216.34") {
		t.Errorf("expected TRUSTED_PROXIES to reject X-Forwarded-For from 127.0.0.1, but log contained 93.184.216.34:\n%s", logs)
	}
	// 2. Pipeline logger must write to stdout buffer (`buf`), not global stderr.
	if !strings.Contains(logs, "pipeline: message rejected by filter strategy") {
		t.Errorf("expected pipeline log in stdout buffer, got:\n%s", logs)
	}
}

func TestResolveRunModes(t *testing.T) {
	validCreds := config.AuthConfig{Credentials: []config.Credential{{Name: "dev", Token: "tok"}}}
	validIMAP := &config.IMAPConfig{LoginEmail: "u@example.com", Password: "p"}
	partialIMAP := &config.IMAPConfig{LoginEmail: "u@example.com"}

	cases := []struct {
		name        string
		cfg         *config.Config
		wantModes   runModes
		wantErrSubs string
	}{
		{
			name:      "row 1a: webhook + credentials + no IMAP",
			cfg:       &config.Config{RunMode: config.RunModeWebhook, Auth: validCreds},
			wantModes: runModes{Webhook: true, IMAP: false},
		},
		{
			name:      "row 1b: webhook + credentials + full IMAP (ignored)",
			cfg:       &config.Config{RunMode: config.RunModeWebhook, Auth: validCreds, IMAP: validIMAP},
			wantModes: runModes{Webhook: true, IMAP: false},
		},
		{
			name:        "row 2: webhook without credentials -> fail-fast",
			cfg:         &config.Config{RunMode: config.RunModeWebhook, IMAP: validIMAP},
			wantErrSubs: "RUN_MODE=webhook requires AUTH_CREDENTIALS",
		},
		{
			name:      "row 3: imap + full IMAP + no credentials",
			cfg:       &config.Config{RunMode: config.RunModeIMAP, IMAP: validIMAP},
			wantModes: runModes{Webhook: false, IMAP: true},
		},
		{
			name:        "row 4: imap + incomplete IMAP -> fail-fast",
			cfg:         &config.Config{RunMode: config.RunModeIMAP, IMAP: partialIMAP},
			wantErrSubs: "RUN_MODE=imap requires EMAIL_TRIGGER_LOGIN_EMAIL and EMAIL_TRIGGER_PASSWORD",
		},
		{
			name:        "row 5: imap + credentials -> fail-fast",
			cfg:         &config.Config{RunMode: config.RunModeIMAP, Auth: validCreds, IMAP: validIMAP},
			wantErrSubs: "AUTH_CREDENTIALS is not used with RUN_MODE=imap",
		},
		{
			name:      "row 6: both + credentials + full IMAP",
			cfg:       &config.Config{RunMode: config.RunModeBoth, Auth: validCreds, IMAP: validIMAP},
			wantModes: runModes{Webhook: true, IMAP: true},
		},
		{
			name:        "row 7: both without credentials -> fail-fast",
			cfg:         &config.Config{RunMode: config.RunModeBoth, IMAP: validIMAP},
			wantErrSubs: "RUN_MODE=both requires AUTH_CREDENTIALS",
		},
		{
			name:        "row 8: both without complete IMAP -> fail-fast",
			cfg:         &config.Config{RunMode: config.RunModeBoth, Auth: validCreds, IMAP: partialIMAP},
			wantErrSubs: "RUN_MODE=both requires EMAIL_TRIGGER_LOGIN_EMAIL and EMAIL_TRIGGER_PASSWORD",
		},
		{
			name:        "row 9a: unset RUN_MODE -> fail-fast",
			cfg:         &config.Config{Auth: validCreds, IMAP: validIMAP},
			wantErrSubs: "RUN_MODE is required: use one of webhook, imap, both",
		},
		{
			name:        "row 9b: programmatic invalid RUN_MODE -> fail-fast",
			cfg:         &config.Config{RunMode: "invalid", Auth: validCreds, IMAP: validIMAP},
			wantErrSubs: "RUN_MODE is required: use one of webhook, imap, both",
		},
		{
			name:        "row 9c: nil *config.Config -> fail-fast without panic",
			cfg:         nil,
			wantErrSubs: "RUN_MODE is required: use one of webhook, imap, both",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveRunModes(tc.cfg)
			if tc.wantErrSubs != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil (modes=%+v)", tc.wantErrSubs, got)
				}
				if !strings.Contains(err.Error(), tc.wantErrSubs) {
					t.Errorf("expected error containing %q, got %q", tc.wantErrSubs, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.wantModes {
				t.Errorf("got %+v, want %+v", got, tc.wantModes)
			}
		})
	}
}

func TestRun_E2E_MultiCredentialAuth(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var buf bytes.Buffer
	readyCh := make(chan string, 1)

	env := map[string]string{
		"PORT":     "0",
		"RUN_MODE": "webhook",
		"AUTH_CREDENTIALS": `[{"name":"device-1","token":"tok-1"},` +
			`{"name":"tablet","username":"tab-user","password":"tab-pass"},` +
			`{"name":"device-2","token":"tok-2","username":"dual-user","password":"dual-pass"}]`,
	}
	lookup := func(k string) string { return env[k] }

	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, nil, &buf, lookup, readyCh)
	}()

	var addr string
	select {
	case addr = <-readyCh:
	case err := <-errCh:
		t.Fatalf("run exited before ready: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for server ready")
	}

	client := &http.Client{Timeout: 5 * time.Second}

	// 1. Bearer token of a token-only credential.
	req1, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://%s/webhook", addr), bytes.NewBufferString(`{"msg_type":"text"}`))
	req1.Header.Set("Authorization", "Bearer tok-1")
	resp1, err := client.Do(req1)
	if err != nil {
		t.Fatalf("request 1 failed: %v", err)
	}
	_ = resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Errorf("request 1: expected 200 OK, got %d", resp1.StatusCode)
	}

	// 2. Basic pair of a basic-only credential.
	req2, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://%s/webhook", addr), bytes.NewBufferString(`{"msg_type":"text"}`))
	req2.SetBasicAuth("tab-user", "tab-pass")
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatalf("request 2 failed: %v", err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("request 2: expected 200 OK, got %d", resp2.StatusCode)
	}

	// 3. Bearer token of a dual credential.
	req3, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://%s/webhook", addr), bytes.NewBufferString(`{"msg_type":"text"}`))
	req3.Header.Set("Authorization", "Bearer tok-2")
	resp3, err := client.Do(req3)
	if err != nil {
		t.Fatalf("request 3 failed: %v", err)
	}
	_ = resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Errorf("request 3: expected 200 OK, got %d", resp3.StatusCode)
	}

	// 4. Cross-credential assembly (device-1's token name with tablet's password) must fail.
	req4, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://%s/webhook", addr), bytes.NewBufferString(`{"msg_type":"text"}`))
	req4.SetBasicAuth("tab-user", "dual-pass")
	resp4, err := client.Do(req4)
	if err != nil {
		t.Fatalf("request 4 failed: %v", err)
	}
	_ = resp4.Body.Close()
	if resp4.StatusCode != http.StatusUnauthorized {
		t.Errorf("request 4: expected 401 for cross-credential assembly, got %d", resp4.StatusCode)
	}

	// 5. Unknown token.
	req5, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://%s/webhook", addr), bytes.NewBufferString(`{"msg_type":"text"}`))
	req5.Header.Set("Authorization", "Bearer wrong-token")
	resp5, err := client.Do(req5)
	if err != nil {
		t.Fatalf("request 5 failed: %v", err)
	}
	_ = resp5.Body.Close()
	if resp5.StatusCode != http.StatusUnauthorized {
		t.Errorf("request 5: expected 401 Unauthorized, got %d", resp5.StatusCode)
	}

	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("expected nil on shutdown, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for run to exit")
	}

	logs := buf.String()
	if !strings.Contains(logs, "configured webhook credentials") || !strings.Contains(logs, "count=3") {
		t.Errorf("expected configured credentials info with count=3 in logs, got:\n%s", logs)
	}
	// The audit trail must attribute each request to the matching credential name.
	for _, want := range []string{"credential=device-1", "credential=tablet", "credential=device-2"} {
		if !strings.Contains(logs, want) {
			t.Errorf("expected %s in the authorized audit log, got:\n%s", want, logs)
		}
	}
	// The removed legacy/union machinery must leave no trace in the logs.
	for _, forbidden := range []string{"both legacy auth", "duplicate webhook credentials", "conflicting factors removed"} {
		if strings.Contains(logs, forbidden) {
			t.Errorf("log must not mention the removed union logic (%q), got:\n%s", forbidden, logs)
		}
	}
}

// TestRun_FailsFastOnDuplicateAuthenticationFactor pins that the strict duplicate-factor rule is
// enforced on the real startup path, not just inside parseCredentials.
func TestRun_FailsFastOnDuplicateAuthenticationFactor(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	env := map[string]string{
		"PORT":             "0",
		"RUN_MODE":         "webhook",
		"AUTH_CREDENTIALS": `[{"name":"a","token":"same"},{"name":"b","token":"same"}]`,
	}
	err := run(ctx, nil, io.Discard, func(k string) string { return env[k] })
	if err == nil {
		t.Fatal("expected startup to fail on duplicate authentication factors")
	}
	if !strings.Contains(err.Error(), `duplicate "token" already used by entry 1`) {
		t.Errorf("expected a duplicate-factor error, got: %v", err)
	}
}

// TestRun_FailsFastWhenWebhookWouldBeServedWithoutCredentials pins the removal of the implicit
// open-access mode: if the process is going to serve the HTTP webhook, it must have credentials.
// The escape hatch is IMAP-only mode (RUN_MODE=imap), which serves no HTTP at all.
func TestRun_FailsFastWhenWebhookWouldBeServedWithoutCredentials(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
	}{
		{
			name: "no authentication configured at all",
			env:  map[string]string{"PORT": "0", "RUN_MODE": "webhook"},
		},
		{
			name: "legacy variables are no longer honoured, so this is still unauthenticated",
			env:  map[string]string{"PORT": "0", "RUN_MODE": "webhook", "AUTH_TOKEN": "orphan-token"},
		},
		{
			name: "half-configured legacy basic is no longer honoured either",
			env:  map[string]string{"PORT": "0", "RUN_MODE": "webhook", "AUTH_USERNAME": "admin"},
		},
		{
			name: "blank AUTH_CREDENTIALS counts as unconfigured",
			env:  map[string]string{"PORT": "0", "RUN_MODE": "webhook", "AUTH_CREDENTIALS": "   "},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// An already-cancelled context keeps this test hang-proof: if a regression makes the
			// process serve the webhook instead of failing fast, run() returns nil on shutdown
			// and the assertion below fails rather than blocking forever.
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			err := run(ctx, nil, io.Discard, func(k string) string { return tc.env[k] })
			if err == nil {
				t.Fatal("expected startup to fail, got nil error (webhook would be served unauthenticated)")
			}
			if !strings.Contains(err.Error(), "AUTH_CREDENTIALS") {
				t.Errorf("expected the error to name AUTH_CREDENTIALS, got: %v", err)
			}
		})
	}
}

func TestRun_RunModeIMAPStartsWithoutAuth(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Occupy a port and pass it as PORT: if RUN_MODE=imap ever started the HTTP server, the bind
	// would fail and run() would return that error instead of nil. This makes the "no HTTP"
	// guarantee observable instead of relying only on the absence of a log line.
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = occupied.Close() }()
	_, portStr, _ := net.SplitHostPort(occupied.Addr().String())

	var buf bytes.Buffer
	env := map[string]string{
		"RUN_MODE":                  "imap",
		"PORT":                      portStr,
		"EMAIL_TRIGGER_LOGIN_EMAIL": "test@example.com",
		"EMAIL_TRIGGER_PASSWORD":    "secret",
		"EMAIL_TRIGGER_IMAP_SERVER": "127.0.0.1",
		"EMAIL_TRIGGER_IMAP_PORT":   "0",
	}
	if err := run(ctx, nil, &buf, func(k string) string { return env[k] }); err != nil {
		t.Fatalf("expected nil for RUN_MODE=imap without AUTH_CREDENTIALS, got: %v", err)
	}
	logs := buf.String()
	if strings.Contains(logs, "starting HTTP webhook server") {
		t.Errorf("RUN_MODE=imap must not start the HTTP server, got logs:\n%s", logs)
	}
	if !strings.Contains(logs, "starting IMAP email trigger listener") {
		t.Errorf("RUN_MODE=imap must start the IMAP listener, got logs:\n%s", logs)
	}
}

func TestRun_RunModeRequired(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := run(ctx, nil, io.Discard, func(string) string { return "" })
	if err == nil {
		t.Fatal("expected error when RUN_MODE is unset, got nil")
	}
	if !strings.Contains(err.Error(), "RUN_MODE") {
		t.Fatalf("expected error to mention RUN_MODE, got: %v", err)
	}

	t.Setenv("RUN_MODE", "")
	if errNilLookup := run(ctx, nil, io.Discard, nil); errNilLookup == nil || !strings.Contains(errNilLookup.Error(), "RUN_MODE") {
		t.Fatalf("expected error mentioning RUN_MODE with nil lookup, got: %v", errNilLookup)
	}
}

func TestRun_RunModeIMAPRejectsCredentials(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	env := map[string]string{
		"PORT":                      "0",
		"RUN_MODE":                  "imap",
		"EMAIL_TRIGGER_LOGIN_EMAIL": "test@example.com",
		"EMAIL_TRIGGER_PASSWORD":    "secret",
		"AUTH_CREDENTIALS":          credentialEnv("dev", "tok"),
	}
	err := run(ctx, nil, io.Discard, func(k string) string { return env[k] })
	if err == nil {
		t.Fatal("expected error when AUTH_CREDENTIALS is provided under RUN_MODE=imap, got nil")
	}
	if !strings.Contains(err.Error(), "RUN_MODE=imap") {
		t.Fatalf("expected error to mention RUN_MODE=imap, got: %v", err)
	}
}

func TestRun_RunModeWebhookIgnoresIMAP(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var buf bytes.Buffer
	env := map[string]string{
		"PORT":                      "0",
		"RUN_MODE":                  "webhook",
		"AUTH_CREDENTIALS":          credentialEnv("dev", "tok"),
		"EMAIL_TRIGGER_LOGIN_EMAIL": "test@example.com",
		"EMAIL_TRIGGER_PASSWORD":    "secret",
	}
	if err := run(ctx, nil, &buf, func(k string) string { return env[k] }); err != nil {
		t.Fatalf("expected nil exit, got: %v", err)
	}
	const wantWarn = "IMAP configuration is present but ignored: RUN_MODE=webhook does not start the IMAP listener; use RUN_MODE=both"
	if count := strings.Count(buf.String(), wantWarn); count != 1 {
		t.Fatalf("expected warning %q exactly once, got %d times in logs:\n%s", wantWarn, count, buf.String())
	}
	if strings.Contains(buf.String(), "starting IMAP email trigger listener") {
		t.Errorf("RUN_MODE=webhook must not start the IMAP listener, got logs:\n%s", buf.String())
	}
}

// TestRun_RunModeGatePrecedesResourceConstruction pins the startup order: the RUN_MODE gate must
// be evaluated before the pipeline and the delivery targets are built, so a misconfigured process
// fails without side effects that suggest a channel was already brought up.
func TestRun_RunModeGatePrecedesResourceConstruction(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var buf bytes.Buffer
	env := map[string]string{
		"AUTH_CREDENTIALS":   credentialEnv("dev", "tok"),
		"TELEGRAM_BOT_TOKEN": "tg-token",
		"TELEGRAM_CHAT_ID":   "42",
		// RUN_MODE intentionally unset: the gate must stop the startup here.
	}
	err := run(ctx, nil, &buf, func(k string) string { return env[k] })
	if err == nil || !strings.Contains(err.Error(), "RUN_MODE") {
		t.Fatalf("expected the RUN_MODE gate to reject the startup, got: %v", err)
	}
	if strings.Contains(buf.String(), "registered deliver target") {
		t.Errorf("RUN_MODE gate must run before delivery targets are built, got logs:\n%s", buf.String())
	}

	// Log-independent proof of the same ordering: the pipeline would fail to compile the regex,
	// but the RUN_MODE gate must win because it is evaluated first.
	badPipelineEnv := map[string]string{
		"AUTH_CREDENTIALS":   credentialEnv("dev", "tok"),
		"REGEX_FILTER_BLACK": `["[unclosed-bracket"]`,
	}
	err = run(ctx, nil, io.Discard, func(k string) string { return badPipelineEnv[k] })
	if err == nil || !strings.Contains(err.Error(), "RUN_MODE") {
		t.Fatalf("RUN_MODE gate must run before the pipeline is built, got: %v", err)
	}
}

// TestRun_E2E_BasicBytesPreservedThroughConfig exercises the C-7 contract across the full startup
// path: parseCredentials stores Basic fields verbatim, main hands them to the webhook handler
// unchanged, and the request side compares them byte-for-byte. A unit test on either side alone
// would not catch a future trim re-introduced in the wiring.
func TestRun_E2E_BasicBytesPreservedThroughConfig(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var buf bytes.Buffer
	readyCh := make(chan string, 1)

	env := map[string]string{
		"PORT":             "0",
		"RUN_MODE":         "webhook",
		"AUTH_CREDENTIALS": `[{"name":"padded","username":"  user  ","password":"  pass  "}]`,
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, nil, &buf, func(k string) string { return env[k] }, readyCh)
	}()

	var addr string
	select {
	case addr = <-readyCh:
	case err := <-errCh:
		t.Fatalf("run exited before ready: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for server ready")
	}

	post := func(user, pass string) int {
		req, err := http.NewRequest(http.MethodPost, "http://"+addr+"/webhook", bytes.NewBufferString(`{"msg_type":"text"}`))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.SetBasicAuth(user, pass)
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if err != nil {
			t.Fatalf("POST /webhook failed: %v", err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	if code := post("  user  ", "  pass  "); code != http.StatusOK {
		t.Errorf("exact byte-for-byte Basic pair must authenticate, got %d", code)
	}
	if code := post("user", "pass"); code != http.StatusUnauthorized {
		t.Errorf("trimmed Basic pair must not match the configured bytes, got %d", code)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("expected nil on shutdown, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for run to exit")
	}
}

func TestRun_LogLevel_Debug(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var buf bytes.Buffer
	readyCh := make(chan string, 1)

	env := map[string]string{
		"PORT":             "0",
		"RUN_MODE":         "webhook",
		"AUTH_CREDENTIALS": `[{"name":"test","token":"debug-token"}]`,
		"LOG_LEVEL":        "debug",
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, nil, &buf, func(k string) string { return env[k] }, readyCh)
	}()

	var addr string
	select {
	case addr = <-readyCh:
	case err := <-errCh:
		t.Fatalf("run exited before ready: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for server ready")
	}

	// Send a valid POST webhook request
	req, err := http.NewRequest(http.MethodPost, "http://"+addr+"/webhook", bytes.NewBufferString(`{"msg_type":"text","title":"debug-title","content":"hello"}`))
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer debug-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("POST /webhook failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("expected nil on shutdown, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for run to exit")
	}

	logs := buf.String()
	if !strings.Contains(logs, "level=DEBUG") || !strings.Contains(logs, "debug-title") {
		t.Errorf("expected debug log with raw body in logs, got:\n%s", logs)
	}
}

func TestRun_Timezone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var buf bytes.Buffer
	readyCh := make(chan string, 1)

	env := map[string]string{
		"PORT":             "0",
		"RUN_MODE":         "webhook",
		"AUTH_CREDENTIALS": `[{"name":"test","token":"tz-token"}]`,
		"TIMEZONE":         "Asia/Shanghai",
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, nil, &buf, func(k string) string { return env[k] }, readyCh)
	}()

	var addr string
	select {
	case addr = <-readyCh:
	case err := <-errCh:
		t.Fatalf("run exited before ready: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for server ready")
	}

	// 1700000000 is 2023-11-14 22:13:20 UTC -> 2023-11-15 06:13:20 in Asia/Shanghai
	req, err := http.NewRequest(http.MethodPost, "http://"+addr+"/webhook", bytes.NewBufferString(`{"msg_type":"text","title":"10086","content":"test timezone","timestamp":1700000000}`))
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer tz-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("POST /webhook failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("expected nil on shutdown, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for run to exit")
	}

	logs := buf.String()
	if !strings.Contains(logs, "timezone=Asia/Shanghai") {
		t.Errorf("expected timezone=Asia/Shanghai in startup logs, got:\n%s", logs)
	}
	if !strings.Contains(logs, "2023-11-15 06:13:20") {
		t.Errorf("expected 2023-11-15 06:13:20 in console deliver logs, got:\n%s", logs)
	}
}

func TestRun_InvalidTimezone_FailFast(t *testing.T) {
	var buf bytes.Buffer
	env := map[string]string{
		"RUN_MODE":         "webhook",
		"AUTH_CREDENTIALS": `[{"name":"test","token":"tz-token"}]`,
		"TIMEZONE":         "Invalid/Nonexistent_Timezone",
	}

	err := run(context.Background(), nil, &buf, func(k string) string { return env[k] })
	if err == nil {
		t.Fatal("expected run() to fail with invalid TIMEZONE, got nil")
	}
	if !strings.Contains(err.Error(), "TIMEZONE") {
		t.Errorf("expected error to mention TIMEZONE, got: %v", err)
	}
}

type testDelivererCapture struct {
	mu       sync.Mutex
	messages []*model.Message
}

func (d *testDelivererCapture) Deliver(_ context.Context, msg *model.Message) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.messages = append(d.messages, msg.Clone())
	return nil
}

func (d *testDelivererCapture) Count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.messages)
}

type testPassthroughProcessor struct{}

func (p *testPassthroughProcessor) Process(_ *model.Message) (bool, error) {
	return true, nil
}

func TestCrossSource_Idempotency_E2E(t *testing.T) {
	t.Run("1. Webhook first then IMAP suppresses duplicate", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var logBuf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))

		// Shared idempotency cache
		sharedCache := idempotency.NewMemoryCache(ctx, 60*time.Second, 1000)
		defer sharedCache.Close()

		dispatcher := &testDelivererCapture{}
		pipe := &testPassthroughProcessor{}

		token := "cross-channel-token"
		whHandler := webhook.NewHandler(webhook.HandlerOptions{
			Credentials:        []config.Credential{{Token: token}},
			Processor:          pipe,
			Deliverer:          dispatcher,
			IdempotencyChecker: sharedCache,
			Logger:             logger,
		})

		imapH := newIMAPHandler(pipe, dispatcher, sharedCache, logger)

		// 1. Webhook receives message with key "tx-100"
		whBody := `{"msg_type":"text","title":"SMS 1","content":"Webhook first","idempotency_key":"tx-100"}`
		req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(whBody))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		whHandler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("webhook expected 200 OK, got %d; body: %s", rec.Code, rec.Body.String())
		}
		if dispatcher.Count() != 1 {
			t.Fatalf("expected dispatcher called 1 time after webhook, got %d", dispatcher.Count())
		}

		// 2. IMAP receives message with identical key "tx-100"
		imapMsg := &model.Message{
			MsgType:        model.MsgTypeText,
			Title:          "IMAP Duplicate",
			Content:        "IMAP content with same key",
			IdempotencyKey: "tx-100",
		}
		if err := imapH(ctx, imapMsg); err != nil {
			t.Fatalf("expected imapHandler to return nil, got: %v", err)
		}

		// Dispatcher count MUST stay 1
		if dispatcher.Count() != 1 {
			t.Fatalf("expected dispatcher count to stay 1, got %d", dispatcher.Count())
		}

		// Verify audit log has imap duplicate message
		logs := logBuf.String()
		if !strings.Contains(logs, "imap message ignored due to duplicate idempotency key") {
			t.Fatalf("expected IMAP duplicate ignore audit log, got:\n%s", logs)
		}
	})

	t.Run("2. IMAP first then Webhook suppresses duplicate", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var logBuf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))

		sharedCache := idempotency.NewMemoryCache(ctx, 60*time.Second, 1000)
		defer sharedCache.Close()

		dispatcher := &testDelivererCapture{}
		pipe := &testPassthroughProcessor{}

		token := "cross-channel-token-2"
		whHandler := webhook.NewHandler(webhook.HandlerOptions{
			Credentials:        []config.Credential{{Token: token}},
			Processor:          pipe,
			Deliverer:          dispatcher,
			IdempotencyChecker: sharedCache,
			Logger:             logger,
		})

		imapH := newIMAPHandler(pipe, dispatcher, sharedCache, logger)

		// 1. IMAP receives first with key "tx-200"
		imapMsg := &model.Message{
			MsgType:        model.MsgTypeText,
			Title:          "IMAP First",
			Content:        "IMAP content first",
			IdempotencyKey: "tx-200",
		}
		if err := imapH(ctx, imapMsg); err != nil {
			t.Fatalf("expected imapHandler to return nil on first consume, got: %v", err)
		}
		if dispatcher.Count() != 1 {
			t.Fatalf("expected dispatcher called 1 time after IMAP, got %d", dispatcher.Count())
		}

		// 2. Webhook receives duplicate with key "tx-200"
		whBody := `{"msg_type":"text","title":"SMS 2","content":"Webhook duplicate","idempotency_key":"tx-200"}`
		req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(whBody))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		whHandler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("webhook expected 200 OK, got %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"status":"success"`) {
			t.Fatalf("expected status success in response, got %s", rec.Body.String())
		}
		// Dispatcher count MUST stay 1
		if dispatcher.Count() != 1 {
			t.Fatalf("expected dispatcher count to stay 1, got %d", dispatcher.Count())
		}

		// Verify audit log has webhook duplicate message
		logs := logBuf.String()
		if !strings.Contains(logs, "webhook message ignored due to duplicate idempotency key") {
			t.Fatalf("expected Webhook duplicate ignore audit log, got:\n%s", logs)
		}
	})

	t.Run("3. Idempotency configuration takes effect in run", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		// Cancel immediately so run exits after startup
		cancel()

		var buf bytes.Buffer
		env := map[string]string{
			"RUN_MODE":             "webhook",
			"AUTH_CREDENTIALS":     `[{"name":"test","token":"valid-token"}]`,
			"IDEMPOTENCY_TTL":      "120s",
			"IDEMPOTENCY_MAX_KEYS": "5000",
			"PORT":                 "0",
		}

		err := run(ctx, nil, &buf, func(k string) string { return env[k] })
		if err != nil {
			t.Fatalf("run failed: %v", err)
		}

		// Load config directly to verify custom params
		cfg, err := config.LoadFromLookup(func(k string) string { return env[k] })
		if err != nil {
			t.Fatalf("config load failed: %v", err)
		}
		if cfg.Idempotency.TTL != 120*time.Second {
			t.Errorf("expected TTL 120s, got %v", cfg.Idempotency.TTL)
		}
		if cfg.Idempotency.MaxKeys != 5000 {
			t.Errorf("expected MaxKeys 5000, got %d", cfg.Idempotency.MaxKeys)
		}
	})

	t.Run("4. Empty key on both Webhook and IMAP allows both deliveries", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sharedCache := idempotency.NewMemoryCache(ctx, 60*time.Second, 1000)
		defer sharedCache.Close()

		dispatcher := &testDelivererCapture{}
		pipe := &testPassthroughProcessor{}

		token := "empty-key-token"
		whHandler := webhook.NewHandler(webhook.HandlerOptions{
			Credentials:        []config.Credential{{Token: token}},
			Processor:          pipe,
			Deliverer:          dispatcher,
			IdempotencyChecker: sharedCache,
		})

		imapH := newIMAPHandler(pipe, dispatcher, sharedCache, nil)

		// 1. Webhook without idempotency_key
		whBody := `{"msg_type":"text","title":"SMS No Key","content":"Webhook empty key"}`
		req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(whBody))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		whHandler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("webhook expected 200 OK, got %d", rec.Code)
		}
		if dispatcher.Count() != 1 {
			t.Fatalf("expected dispatcher called 1 time, got %d", dispatcher.Count())
		}

		// 2. IMAP without idempotency_key
		imapMsg := &model.Message{
			MsgType: model.MsgTypeText,
			Title:   "IMAP No Key",
			Content: "IMAP empty key",
		}
		if err := imapH(ctx, imapMsg); err != nil {
			t.Fatalf("expected imapHandler to succeed, got: %v", err)
		}

		if dispatcher.Count() != 2 {
			t.Fatalf("expected dispatcher count to be 2 for empty keys, got %d", dispatcher.Count())
		}
	})

	t.Run("5. Concurrent Webhook and IMAP racing same key allows strictly 1 delivery", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sharedCache := idempotency.NewMemoryCache(ctx, 60*time.Second, 1000)
		defer sharedCache.Close()

		dispatcher := &testDelivererCapture{}
		pipe := &testPassthroughProcessor{}

		token := "concurrent-race-token"
		whHandler := webhook.NewHandler(webhook.HandlerOptions{
			Credentials:        []config.Credential{{Token: token}},
			Processor:          pipe,
			Deliverer:          dispatcher,
			IdempotencyChecker: sharedCache,
		})

		imapH := newIMAPHandler(pipe, dispatcher, sharedCache, nil)

		var wg sync.WaitGroup
		startCh := make(chan struct{})

		sharedRaceKey := "race-key-42"

		// 20 Webhook workers
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-startCh
				whBody := fmt.Sprintf(`{"msg_type":"text","title":"WH Race","content":"WH","idempotency_key":%q}`, sharedRaceKey)
				req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(whBody))
				req.Header.Set("Authorization", "Bearer "+token)
				rec := httptest.NewRecorder()
				whHandler.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					t.Errorf("webhook unexpected status %d", rec.Code)
				}
			}()
		}

		// 20 IMAP workers
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-startCh
				imapMsg := &model.Message{
					MsgType:        model.MsgTypeText,
					Title:          "IMAP Race",
					Content:        "IMAP",
					IdempotencyKey: sharedRaceKey,
				}
				if err := imapH(ctx, imapMsg); err != nil {
					t.Errorf("imapHandler unexpected error: %v", err)
				}
			}()
		}

		// All 40 workers start concurrently
		close(startCh)
		wg.Wait()

		// Under race detector, exactly 1 delivery must succeed across both sources
		if dispatcher.Count() != 1 {
			t.Fatalf("expected strictly 1 delivery across 40 concurrent cross-source calls, got %d", dispatcher.Count())
		}
	})
}

type testCustomProcessor struct {
	fn func(*model.Message) (bool, error)
}

func (p *testCustomProcessor) Process(msg *model.Message) (bool, error) {
	return p.fn(msg)
}

func TestNewIMAPHandler_NilLoggerDefensive(t *testing.T) {
	ctx := context.Background()

	t.Run("first deliver and duplicate suppression without panic", func(t *testing.T) {
		pipe := &testPassthroughProcessor{}
		dispatcher := &testDelivererCapture{}
		cache := &idempotency.MemoryCache{}
		defer cache.Close()

		handler := newIMAPHandler(pipe, dispatcher, cache, nil)

		msg := &model.Message{
			MsgType:        model.MsgTypeText,
			Title:          "Nil Logger Test",
			Content:        "content",
			IdempotencyKey: "nil-logger-1",
		}
		if err := handler(ctx, msg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if dispatcher.Count() != 1 {
			t.Fatalf("expected 1 delivered message, got %d", dispatcher.Count())
		}

		// 重复消息触发 logger.Info 审计日志
		if err := handler(ctx, msg); err != nil {
			t.Fatalf("unexpected error on duplicate: %v", err)
		}
		if dispatcher.Count() != 1 {
			t.Fatalf("expected count to stay 1, got %d", dispatcher.Count())
		}
	})

	t.Run("filter rejection logs without panic", func(t *testing.T) {
		pipe := &testCustomProcessor{
			fn: func(_ *model.Message) (bool, error) {
				return false, nil
			},
		}
		dispatcher := &testDelivererCapture{}
		handler := newIMAPHandler(pipe, dispatcher, nil, nil)

		msg := &model.Message{
			MsgType: model.MsgTypeText,
			Title:   "Filtered Title",
			Content: "filtered content",
		}
		if err := handler(ctx, msg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if dispatcher.Count() != 0 {
			t.Fatalf("expected 0 delivered messages, got %d", dispatcher.Count())
		}
	})

	t.Run("pipeline error logs without panic", func(t *testing.T) {
		pipeErr := errors.New("pipeline broken")
		pipe := &testCustomProcessor{
			fn: func(_ *model.Message) (bool, error) {
				return false, pipeErr
			},
		}
		dispatcher := &testDelivererCapture{}
		handler := newIMAPHandler(pipe, dispatcher, nil, nil)

		msg := &model.Message{
			MsgType: model.MsgTypeText,
			Title:   "Error Title",
			Content: "error content",
		}
		err := handler(ctx, msg)
		if !errors.Is(err, pipeErr) {
			t.Fatalf("expected pipeline error %v, got %v", pipeErr, err)
		}
	})
}
