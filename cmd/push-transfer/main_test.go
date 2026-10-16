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
		"AUTH_TOKEN":           authToken,
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
			"PORT": "invalid-port",
		}
		err := run(context.Background(), nil, io.Discard, func(k string) string { return env[k] })
		if err == nil {
			t.Fatal("expected error with invalid port, got nil")
		}
	})

	t.Run("invalid regex configuration", func(t *testing.T) {
		env := map[string]string{
			"REGEX_FILTER_BLACK": `["[unclosed-bracket"]`,
		}
		err := run(context.Background(), nil, io.Discard, func(k string) string { return env[k] })
		if err == nil {
			t.Fatal("expected error with invalid regex, got nil")
		}
	})

	t.Run("no services configured", func(t *testing.T) {
		env := map[string]string{
			"RUN_TRIGGER": "true",
			// IMAP not configured and Token empty
		}
		err := run(context.Background(), nil, io.Discard, func(k string) string { return env[k] })
		if err == nil {
			t.Fatal("expected error when no services configured, got nil")
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
			"PORT":       portStr,
			"AUTH_TOKEN": "token",
		}
		err = run(context.Background(), nil, io.Discard, func(k string) string { return env[k] })
		if err == nil {
			t.Fatal("expected error on port collision, got nil")
		}
	})

	t.Run("trigger enabled without IMAP configured", func(_ *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		env := map[string]string{
			"RUN_TRIGGER": "true",
			"AUTH_TOKEN":  "token",
			"PORT":        "0",
		}
		_ = run(ctx, nil, io.Discard, func(k string) string { return env[k] })
	})
}

func TestRun_IMAPTriggerFlow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately to test IMAP startup and clean exit
	cancel()

	env := map[string]string{
		"RUN_TRIGGER":               "true",
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
		MsgType: "call",
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
		"PORT":       "0",
		"AUTH_TOKEN": "waitgroup-token",
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

	t.Run("run logs all partial config warnings including partial Basic Auth and partial IMAP", func(t *testing.T) {
		var buf bytes.Buffer
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		env := map[string]string{
			"PORT":                       "0",
			"AUTH_TOKEN":                 "valid-token",
			"AUTH_USERNAME":              "only-username",
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
			"partial webhook basic auth configuration detected; both AUTH_USERNAME and AUTH_PASSWORD are required for basic auth",
			"partial IMAP trigger configuration detected; IMAP listener will not start unless all required fields are set",
			"partial telegram configuration detected; telegram target disabled",
			"partial email configuration detected; email target disabled",
		}
		for _, want := range expectedWarnings {
			if count := strings.Count(logs, want); count != 1 {
				t.Errorf("expected warning %q exactly once, got %d times in logs:\n%s", want, count, logs)
			}
		}
	})

	t.Run("webhook starts and authenticates with Basic Auth when RUN_TRIGGER=true and only Basic Auth is configured", func(t *testing.T) {
		var buf bytes.Buffer
		readyCh := make(chan string, 1)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		env := map[string]string{
			"PORT":          "0",
			"RUN_TRIGGER":   "true",
			"AUTH_USERNAME": "webhook-admin",
			"AUTH_PASSWORD": "webhook-password",
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
		"AUTH_TOKEN":           "token-instance-1",
		"BARK_DEVICE_KEY_LIST": "instance-1-bark-key",
	}
	env2 := map[string]string{
		"PORT":               "0",
		"AUTH_TOKEN":         "token-instance-2",
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
		"AUTH_TOKEN":         "wired-test-token",
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
