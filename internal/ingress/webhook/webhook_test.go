package webhook_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zapsaang/push-transfer/internal/config"
	"github.com/zapsaang/push-transfer/internal/deliver"
	"github.com/zapsaang/push-transfer/internal/ingress/webhook"
	"github.com/zapsaang/push-transfer/internal/model"
	"github.com/zapsaang/push-transfer/internal/pipeline"
)

func TestAuth_ConstantTimeCompare(t *testing.T) {
	token := "my-secret-token-12345"

	testCases := []struct {
		name         string
		authHeader   string
		expectedCode int
	}{
		{"exact match", "Bearer my-secret-token-12345", http.StatusOK},
		{"same length mismatch", "Bearer my-secret-token-12346", http.StatusUnauthorized},
		{"shorter prefix", "Bearer my-secret-token", http.StatusUnauthorized},
		{"longer suffix", "Bearer my-secret-token-12345-extra", http.StatusUnauthorized},
		{"empty input", "", http.StatusUnauthorized},
		{"single char diff at start", "Bearer xy-secret-token-12345", http.StatusUnauthorized},
	}

	h := webhook.NewHandler(webhook.HandlerOptions{
		Credentials: []config.Credential{{Token: token}},
		Processor:   &mockProcessor{pass: true},
		Deliverer:   &mockDeliverer{},
	})

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"msg_type":"text"}`))
			if tc.authHeader != "" {
				req.Header.Set("Authorization", tc.authHeader)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.expectedCode {
				t.Errorf("request auth %q got status %d; want %d", tc.authHeader, rec.Code, tc.expectedCode)
			}
		})
	}
}

func TestAuth_ExtractBearerToken(t *testing.T) {
	testCases := []struct {
		name     string
		header   string
		expected string
	}{
		{"standard Bearer with single space", "Bearer token123", "token123"},
		{"multiple spaces between Bearer and token", "Bearer    token123", "token123"},
		{"tab separation between Bearer and token", "Bearer\ttoken123", "token123"},
		{"mixed space and tab separation", "Bearer  \t  token123", "token123"},
		{"lowercase bearer", "bearer token123", "token123"},
		{"uppercase BEARER", "BEARER token123", "token123"},
		{"leading and trailing whitespace in header", "   Bearer token123   ", "token123"},
		{"empty header", "", ""},
		{"bearer with no token", "Bearer ", ""},
		{"bearer with only tabs", "Bearer\t\t", ""},
		{"invalid scheme Basic", "Basic dXNlcjpwYXNz", ""},
		{"no whitespace between Bearer and token", "Bearertoken123", ""},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, "/webhook", http.NoBody)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			got := webhook.ExtractBearerToken(req)
			if got != tc.expected {
				t.Errorf("ExtractBearerToken(%q) = %q; want %q", tc.header, got, tc.expected)
			}
		})
	}

	t.Run("nil request returns empty string without panicking", func(t *testing.T) {
		if got := webhook.ExtractBearerToken(nil); got != "" {
			t.Errorf("ExtractBearerToken(nil) = %q; want empty", got)
		}
	})

	// Regression guard (already safe via net/http Header.Get on nil map):
	t.Run("zero http.Request with nil Header returns empty string", func(t *testing.T) {
		if got := webhook.ExtractBearerToken(&http.Request{}); got != "" {
			t.Errorf("ExtractBearerToken(&http.Request{}) = %q; want empty", got)
		}
	})
}

type mockProcessor struct {
	pass bool
	err  error
}

func (m *mockProcessor) Process(_ *model.Message) (bool, error) {
	return m.pass, m.err
}

type mockDeliverer struct {
	err error
}

func (m *mockDeliverer) Deliver(_ context.Context, _ *model.Message) error {
	return m.err
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}

func TestWebhook_HTTPStatusCodes(t *testing.T) {
	const validToken = "secret-token-abc"

	t.Run("no or invalid Bearer token -> 401 Unauthorized", func(t *testing.T) {
		h := webhook.NewHandler(webhook.HandlerOptions{
			Credentials: []config.Credential{{Token: validToken}},
			Processor:   &mockProcessor{pass: true},
			Deliverer:   &mockDeliverer{},
		})

		// 1. No auth header
		req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"msg_type":"text"}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected status 401, got %d", rec.Code)
		}

		// 2. Invalid Bearer token
		req = httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"msg_type":"text"}`))
		req.Header.Set("Authorization", "Bearer wrong-token")
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected status 401, got %d", rec.Code)
		}
	})

	t.Run("malformed JSON -> 400 Bad Request", func(t *testing.T) {
		h := webhook.NewHandler(webhook.HandlerOptions{
			Credentials: []config.Credential{{Token: validToken}},
			Processor:   &mockProcessor{pass: true},
			Deliverer:   &mockDeliverer{},
		})

		req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{invalid-json`))
		req.Header.Set("Authorization", "Bearer "+validToken)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected status 400, got %d", rec.Code)
		}
	})

	t.Run("trailing JSON garbage or multiple JSON objects -> 400 Bad Request", func(t *testing.T) {
		h := webhook.NewHandler(webhook.HandlerOptions{
			Credentials: []config.Credential{{Token: validToken}},
			Processor:   &mockProcessor{pass: true},
			Deliverer:   &mockDeliverer{},
		})

		for _, payload := range []string{
			`{"msg_type":"text","title":"10086"}{"msg_type":"call"}`,
			`{"msg_type":"text","title":"10086"} trailing_garbage`,
		} {
			req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(payload))
			req.Header.Set("Authorization", "Bearer "+validToken)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("payload %q: expected status 400 Bad Request for trailing data, got %d", payload, rec.Code)
			}
		}
	})

	t.Run("filter blocked message -> 200 OK + status blocked", func(t *testing.T) {
		h := webhook.NewHandler(webhook.HandlerOptions{
			Credentials: []config.Credential{{Token: validToken}},
			Processor:   &mockProcessor{pass: false}, // Blocked by filter
			Deliverer:   &mockDeliverer{},
		})

		req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"msg_type":"text","title":"spam"}`))
		req.Header.Set("Authorization", "Bearer "+validToken)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", rec.Code)
		}

		var res map[string]interface{}
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("failed to parse response JSON: %v", err)
		}
		if res["status"] != "blocked" {
			t.Errorf("expected status 'blocked', got %v", res["status"])
		}
	})

	t.Run("successful process and deliver -> 200 OK + status success", func(t *testing.T) {
		h := webhook.NewHandler(webhook.HandlerOptions{
			Credentials: []config.Credential{{Token: validToken}},
			Processor:   &mockProcessor{pass: true},
			Deliverer:   &mockDeliverer{},
		})

		req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"msg_type":"text","title":"10086","content":"test"}`))
		req.Header.Set("Authorization", "Bearer "+validToken)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", rec.Code)
		}

		var res map[string]interface{}
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("failed to parse response JSON: %v", err)
		}
		if res["status"] != "success" {
			t.Errorf("expected status 'success', got %v", res["status"])
		}
	})

	t.Run("all downstream deliveries fail -> 502 Bad Gateway + status deliver_failed", func(t *testing.T) {
		h := webhook.NewHandler(webhook.HandlerOptions{
			Credentials: []config.Credential{{Token: validToken}},
			Processor:   &mockProcessor{pass: true},
			Deliverer:   &mockDeliverer{err: deliver.ErrAllTargetsFailed},
		})

		req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"msg_type":"text","title":"10086"}`))
		req.Header.Set("Authorization", "Bearer "+validToken)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadGateway {
			t.Errorf("expected status 502 Bad Gateway, got %d", rec.Code)
		}

		var res map[string]interface{}
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("failed to parse response JSON: %v", err)
		}
		if res["status"] != "deliver_failed" {
			t.Errorf("expected status 'deliver_failed', got %v", res["status"])
		}
	})

	t.Run("method not allowed -> 405", func(t *testing.T) {
		h := webhook.NewHandler(webhook.HandlerOptions{
			Credentials: []config.Credential{{Token: validToken}},
		})
		req := httptest.NewRequest(http.MethodGet, "/webhook", http.NoBody)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("expected status 405, got %d", rec.Code)
		}
	})

	t.Run("pipeline processor error -> 500 Internal Server Error", func(t *testing.T) {
		h := webhook.NewHandler(webhook.HandlerOptions{
			Credentials: []config.Credential{{Token: validToken}},
			Processor:   &mockProcessor{err: errors.New("pipeline broken")},
		})
		req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"msg_type":"text"}`))
		req.Header.Set("Authorization", "Bearer "+validToken)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Errorf("expected status 500, got %d", rec.Code)
		}
	})

	t.Run("request body exceeding 1MB default limit -> 413 Request Entity Too Large", func(t *testing.T) {
		h := webhook.NewHandler(webhook.HandlerOptions{
			Credentials: []config.Credential{{Token: validToken}},
			Processor:   &mockProcessor{pass: true},
			Deliverer:   &mockDeliverer{},
		})

		largeContent := strings.Repeat("a", (1<<20)+512*1024) // 1.5MB
		payload := `{"msg_type":"text","title":"10086","content":"` + largeContent + `"}`
		req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+validToken)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("expected status 413 Request Entity Too Large, got %d", rec.Code)
		}

		var res map[string]interface{}
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("failed to parse response JSON: %v", err)
		}
		if res["status"] != "failed" || res["message"] != "request body too large" {
			t.Errorf("expected status='failed' and message='request body too large', got %v", res)
		}
	})

	t.Run("custom MaxBodyBytes limit enforced", func(t *testing.T) {
		h := webhook.NewHandler(webhook.HandlerOptions{
			Credentials:  []config.Credential{{Token: validToken}},
			MaxBodyBytes: 256,
			Processor:    &mockProcessor{pass: true},
			Deliverer:    &mockDeliverer{},
		})

		payload := `{"msg_type":"text","title":"10086","content":"` + strings.Repeat("b", 512) + `"}`
		req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+validToken)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("expected status 413 Request Entity Too Large, got %d", rec.Code)
		}
	})

	t.Run("body read error (not MaxBytesError) -> 400 Bad Request", func(t *testing.T) {
		h := webhook.NewHandler(webhook.HandlerOptions{
			Credentials: []config.Credential{{Token: validToken}},
			Processor:   &mockProcessor{pass: true},
			Deliverer:   &mockDeliverer{},
		})

		req := httptest.NewRequest(http.MethodPost, "/webhook", &errReader{})
		req.Header.Set("Authorization", "Bearer "+validToken)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected status 400 Bad Request, got %d", rec.Code)
		}
		var res map[string]interface{}
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("failed to parse response JSON: %v", err)
		}
		if res["status"] != "failed" || res["message"] != "failed to read request body" {
			t.Errorf("expected failed to read request body, got %v", res)
		}
	})

	t.Run("debug logging logs raw request body", func(t *testing.T) {
		var logBuf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

		h := webhook.NewHandler(webhook.HandlerOptions{
			Credentials: []config.Credential{{Token: validToken}},
			Processor:   &mockProcessor{pass: true},
			Deliverer:   &mockDeliverer{},
			Logger:      logger,
		})

		rawJSON := `{"msg_type":"text","title":"10086","content":"test message"}`
		req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(rawJSON))
		req.Header.Set("Authorization", "Bearer "+validToken)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", rec.Code)
		}

		logStr := logBuf.String()
		if !strings.Contains(logStr, "webhook raw request body") || !strings.Contains(logStr, "10086") || !strings.Contains(logStr, "test message") {
			t.Errorf("expected debug log to contain raw JSON, got:\n%s", logStr)
		}
	})

	t.Run("debug logging logs raw malformed JSON body before 400", func(t *testing.T) {
		var logBuf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

		h := webhook.NewHandler(webhook.HandlerOptions{
			Credentials: []config.Credential{{Token: validToken}},
			Processor:   &mockProcessor{pass: true},
			Deliverer:   &mockDeliverer{},
			Logger:      logger,
		})

		rawMalformed := `{malformed: json`
		req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(rawMalformed))
		req.Header.Set("Authorization", "Bearer "+validToken)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d", rec.Code)
		}

		logStr := logBuf.String()
		if !strings.Contains(logStr, "webhook raw request body") || !strings.Contains(logStr, "malformed") {
			t.Errorf("expected debug log to contain raw malformed JSON, got:\n%s", logStr)
		}
	})

	t.Run("debug logging logs message parsed details", func(t *testing.T) {
		var logBuf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

		h := webhook.NewHandler(webhook.HandlerOptions{
			Credentials: []config.Credential{{Token: validToken}},
			Processor:   &mockProcessor{pass: true},
			Deliverer:   &mockDeliverer{},
			Logger:      logger,
		})

		rawJSON := `{"msg_type":"app_notification","app_name":"WeChat","title":"Boss","content":"Meeting now","hostname":"phone-1"}`
		req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(rawJSON))
		req.Header.Set("Authorization", "Bearer "+validToken)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", rec.Code)
		}

		logStr := logBuf.String()
		if !strings.Contains(logStr, "webhook message parsed") ||
			!strings.Contains(logStr, "app_name=WeChat") ||
			!strings.Contains(logStr, "title=Boss") ||
			!strings.Contains(logStr, "hostname=phone-1") {
			t.Errorf("expected parsed message debug log, got:\n%s", logStr)
		}
	})

	t.Run("debug logging logs auth failure reason details without leaking credentials", func(t *testing.T) {
		cases := []struct {
			name       string
			authHeader string
			wantReason string
		}{
			{"missing auth header", "", "missing_authorization_header"},
			{"invalid bearer token", "Bearer wrong-secret", "invalid_bearer_token"},
			{"invalid basic auth", "Basic dXNlcjp3cm9uZw==", "invalid_basic_credentials"},
			{"unsupported scheme", "Digest something", "unsupported_authorization_scheme"},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				var logBuf bytes.Buffer
				logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

				h := webhook.NewHandler(webhook.HandlerOptions{
					Credentials: []config.Credential{{Token: validToken, Username: "admin", Password: "pwd"}},
					Logger:      logger,
				})

				req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(`{}`))
				if tc.authHeader != "" {
					req.Header.Set("Authorization", tc.authHeader)
				}
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)

				if rec.Code != http.StatusUnauthorized {
					t.Fatalf("expected 401, got %d", rec.Code)
				}

				logStr := logBuf.String()
				if !strings.Contains(logStr, "webhook auth rejected details") || !strings.Contains(logStr, "reason="+tc.wantReason) {
					t.Errorf("expected reason %s in debug log, got:\n%s", tc.wantReason, logStr)
				}
				if strings.Contains(logStr, "wrong-secret") || strings.Contains(logStr, "dXNlcjp3cm9uZw") {
					t.Errorf("credential leaked in debug log: %s", logStr)
				}
			})
		}
	})
}

func TestWebhook_BasicAndDualAuth(t *testing.T) {
	t.Run("Basic Auth only", func(t *testing.T) {
		h := webhook.NewHandler(webhook.HandlerOptions{
			Credentials: []config.Credential{{Username: "admin", Password: "secret-pass"}},
			Processor:   &mockProcessor{pass: true},
			Deliverer:   &mockDeliverer{},
		})

		// 1. Valid Basic Auth -> 200 OK
		req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"msg_type":"text"}`))
		req.SetBasicAuth("admin", "secret-pass")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("valid basic auth: expected 200, got %d", rec.Code)
		}

		// 2. Wrong password -> 401 Unauthorized
		req = httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"msg_type":"text"}`))
		req.SetBasicAuth("admin", "wrong-pass")
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("wrong password: expected 401, got %d", rec.Code)
		}

		// 3. Wrong username -> 401 Unauthorized
		req = httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"msg_type":"text"}`))
		req.SetBasicAuth("wrong-user", "secret-pass")
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("wrong username: expected 401, got %d", rec.Code)
		}

		// 4. Missing header -> 401 Unauthorized
		req = httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"msg_type":"text"}`))
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("missing auth header: expected 401, got %d", rec.Code)
		}
	})

	t.Run("Dual Auth (Token and Basic Auth both configured)", func(t *testing.T) {
		h := webhook.NewHandler(webhook.HandlerOptions{
			Credentials: []config.Credential{
				{Token: "bearer-token-xyz"},
				{Username: "admin", Password: "secret-pass"},
			},
			Processor: &mockProcessor{pass: true},
			Deliverer: &mockDeliverer{},
		})

		// 1. Valid Bearer Token -> 200 OK
		req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"msg_type":"text"}`))
		req.Header.Set("Authorization", "Bearer bearer-token-xyz")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("valid bearer token: expected 200, got %d", rec.Code)
		}

		// 2. Valid Basic Auth -> 200 OK
		req = httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"msg_type":"text"}`))
		req.SetBasicAuth("admin", "secret-pass")
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("valid basic auth: expected 200, got %d", rec.Code)
		}

		// 3. Both invalid -> 401 Unauthorized
		req = httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"msg_type":"text"}`))
		req.Header.Set("Authorization", "Bearer wrong-token")
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("invalid bearer token: expected 401, got %d", rec.Code)
		}

		req = httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"msg_type":"text"}`))
		req.SetBasicAuth("admin", "bad-pass")
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("invalid basic auth: expected 401, got %d", rec.Code)
		}
	})
}

func TestWebhook_RealIP(t *testing.T) {
	t.Run("extract from X-Forwarded-For", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/webhook", http.NoBody)
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("X-Forwarded-For", "203.0.113.195, 70.41.3.18, 150.172.238.178")
		ip := webhook.GetRealIP(req)
		// Only the entries appended by a trusted proxy are believed: the walk goes from
		// the nearest hop outwards and stops at the first address that is not itself a
		// trusted proxy. Everything to the left of that could have been forged by client.
		if ip != "150.172.238.178" {
			t.Errorf("expected the outermost untrusted hop '150.172.238.178', got '%s'", ip)
		}
	})

	t.Run("client-injected X-Forwarded-For prefix cannot spoof the logged IP", func(t *testing.T) {
		// A real reverse proxy appends the peer address to any header the client sent,
		// so the trustworthy entry is the one the proxy added (rightmost).
		req := httptest.NewRequest(http.MethodPost, "/webhook", http.NoBody)
		req.RemoteAddr = "10.0.0.1:443" // default-trusted private proxy
		req.Header.Set("X-Forwarded-For", "9.9.9.9, 203.0.113.7")
		if got := webhook.GetRealIP(req); got != "203.0.113.7" {
			t.Errorf("expected '203.0.113.7' (proxy-appended) instead of the forged '9.9.9.9', got '%s'", got)
		}
	})

	t.Run("chain made only of trusted proxies falls back to the leftmost origin", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/webhook", http.NoBody)
		req.RemoteAddr = "10.0.0.1:443"
		req.Header.Set("X-Forwarded-For", "192.168.1.10, 172.16.0.9, 10.0.0.2")
		if got := webhook.GetRealIP(req); got != "192.168.1.10" {
			t.Errorf("expected leftmost trusted origin '192.168.1.10', got '%s'", got)
		}
	})

	t.Run("extract from X-Real-IP", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/webhook", http.NoBody)
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("X-Real-IP", "198.51.100.1")
		ip := webhook.GetRealIP(req)
		if ip != "198.51.100.1" {
			t.Errorf("expected '198.51.100.1', got '%s'", ip)
		}
	})

	t.Run("fallback to RemoteAddr", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/webhook", http.NoBody)
		req.RemoteAddr = "192.0.2.1:54321"
		ip := webhook.GetRealIP(req)
		if ip != "192.0.2.1" {
			t.Errorf("expected '192.0.2.1', got '%s'", ip)
		}
	})

	t.Run("untrusted public RemoteAddr spoofing X-Forwarded-For is ignored", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/webhook", http.NoBody)
		req.RemoteAddr = "8.8.8.8:54321"
		req.Header.Set("X-Forwarded-For", "1.2.3.4")
		req.Header.Set("X-Real-IP", "5.6.7.8")
		ip := webhook.GetRealIP(req)
		if ip != "8.8.8.8" {
			t.Errorf("expected '8.8.8.8' (untrusted proxy ignored), got '%s'", ip)
		}
	})

	t.Run("malformed IP in X-Forwarded-For falls back safely", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/webhook", http.NoBody)
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("X-Forwarded-For", "not-an-ip")
		ip := webhook.GetRealIP(req)
		if ip != "127.0.0.1" {
			t.Errorf("expected '127.0.0.1' fallback on malformed XFF, got '%s'", ip)
		}

		// Also test malformed XFF falling through to valid X-Real-IP
		req.Header.Set("X-Real-IP", "not-an-ip-either")
		ip = webhook.GetRealIP(req)
		if ip != "127.0.0.1" {
			t.Errorf("expected '127.0.0.1' fallback on malformed XRI, got '%s'", ip)
		}

		// Test XFF with ip:port and bracketed IPv6
		req.Header.Set("X-Forwarded-For", "unknown, 203.0.113.195:54321")
		if got := webhook.GetRealIP(req); got != "203.0.113.195" {
			t.Errorf("expected '203.0.113.195' from ip:port in XFF, got %q", got)
		}

		req.Header.Set("X-Forwarded-For", "[2001:db8::abcd]")
		if got := webhook.GetRealIP(req); got != "2001:db8::abcd" {
			t.Errorf("expected '2001:db8::abcd' from bracketed IPv6 in XFF, got %q", got)
		}
	})

	t.Run("custom TrustedProxies CIDR allows configured proxy and rejects others", func(t *testing.T) {
		var logBuf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logBuf, nil))

		h := webhook.NewHandler(webhook.HandlerOptions{
			Logger:         logger,
			TrustedProxies: []string{"", "invalid-ip", "10.0.0.0/99", "198.51.100.0/24", "2001:db8::1", "203.0.113.10"},
			Processor:      &mockProcessor{pass: true},
			Deliverer:      &mockDeliverer{},
		})

		// 1. Request from trusted proxy CIDR 198.51.100.5 -> honors X-Forwarded-For
		req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"msg_type":"text"}`))
		req.RemoteAddr = "198.51.100.5:443"
		req.Header.Set("X-Forwarded-For", "93.184.216.34")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if !strings.Contains(logBuf.String(), "93.184.216.34") {
			t.Errorf("expected log to contain forwarded IP 93.184.216.34 from trusted CIDR, got: %s", logBuf.String())
		}

		// 2. Request from trusted plain IPv6 2001:db8::1 -> honors X-Real-IP
		logBuf.Reset()
		req = httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"msg_type":"text"}`))
		req.RemoteAddr = "[2001:db8::1]:443"
		req.Header.Set("X-Real-IP", "93.184.216.35")
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if !strings.Contains(logBuf.String(), "93.184.216.35") {
			t.Errorf("expected log to contain real IP 93.184.216.35 from trusted IPv6, got: %s", logBuf.String())
		}

		// 3. Request from 127.0.0.1 (not in custom TrustedProxies) -> ignores X-Forwarded-For
		logBuf.Reset()
		req = httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"msg_type":"text"}`))
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("X-Forwarded-For", "1.2.3.4")
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if !strings.Contains(logBuf.String(), "127.0.0.1") || strings.Contains(logBuf.String(), "1.2.3.4") {
			t.Errorf("expected log to contain direct IP 127.0.0.1 and ignore spoofed 1.2.3.4, got: %s", logBuf.String())
		}

		// 4. Request with unparseable RemoteAddr -> returns raw RemoteAddr without trusting headers
		req = httptest.NewRequest(http.MethodPost, "/webhook", http.NoBody)
		req.RemoteAddr = "unix-socket-peer"
		req.Header.Set("X-Forwarded-For", "1.2.3.4")
		if got := webhook.GetRealIP(req); got != "unix-socket-peer" {
			t.Errorf("expected 'unix-socket-peer', got %q", got)
		}
	})

	t.Run("IPv6 with zone identifiers and whitespace segments in headers", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/webhook", http.NoBody)
		req.RemoteAddr = "127.0.0.1:12345"

		// Whitespace only segments in XFF falling through to valid IP
		req.Header.Set("X-Forwarded-For", "   , \t , 198.51.100.22,  ")
		if got := webhook.GetRealIP(req); got != "198.51.100.22" {
			t.Errorf("expected '198.51.100.22' after skipping whitespace segments, got %q", got)
		}

		// IPv6 with zone identifier
		req.Header.Set("X-Forwarded-For", "fe80::1%eth0")
		if got := webhook.GetRealIP(req); got != "fe80::1" {
			t.Errorf("expected 'fe80::1' from scoped IPv6, got %q", got)
		}

		// Bracketed IPv6 with zone identifier and port
		req.Header.Set("X-Forwarded-For", "[fe80::2%eth0]:8080")
		if got := webhook.GetRealIP(req); got != "fe80::2" {
			t.Errorf("expected 'fe80::2' from bracketed scoped IPv6:port, got %q", got)
		}

		// Bracketed IPv6 with zone identifier without port
		req.Header.Set("X-Forwarded-For", "[fe80::3%eth0]")
		if got := webhook.GetRealIP(req); got != "fe80::3" {
			t.Errorf("expected 'fe80::3' from bracketed scoped IPv6, got %q", got)
		}

		// Whitespace in X-Real-IP falls back to RemoteAddr
		req.Header.Del("X-Forwarded-For")
		req.Header.Set("X-Real-IP", "   ")
		if got := webhook.GetRealIP(req); got != "127.0.0.1" {
			t.Errorf("expected '127.0.0.1' fallback for whitespace X-Real-IP, got %q", got)
		}
	})

	t.Run("logs real IP during request", func(t *testing.T) {
		var logBuf bytes.Buffer
		handler := slog.NewTextHandler(&logBuf, nil)
		logger := slog.New(handler)

		h := webhook.NewHandler(webhook.HandlerOptions{
			Logger:    logger,
			Processor: &mockProcessor{pass: true},
			Deliverer: &mockDeliverer{},
		})

		req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"msg_type":"text"}`))
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("X-Real-IP", "203.0.113.50")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		logOutput := logBuf.String()
		if !strings.Contains(logOutput, "203.0.113.50") {
			t.Errorf("expected log to contain real IP 203.0.113.50, got: %s", logOutput)
		}
	})

	t.Run("warns on invalid trusted proxy entries during NewHandler", func(t *testing.T) {
		var logBuf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logBuf, nil))

		_ = webhook.NewHandler(webhook.HandlerOptions{
			Logger:         logger,
			TrustedProxies: []string{"invalid-ip", "10.0.0.0/99"},
		})

		logs := logBuf.String()
		if !strings.Contains(logs, "ignoring invalid trusted proxy IP") {
			t.Errorf("expected warning about invalid IP, got: %s", logs)
		}
		if !strings.Contains(logs, "ignoring invalid trusted proxy CIDR") {
			t.Errorf("expected warning about invalid CIDR, got: %s", logs)
		}
	})

	t.Run("nil request returns empty string without panicking", func(t *testing.T) {
		if got := webhook.GetRealIP(nil); got != "" {
			t.Errorf("GetRealIP(nil) = %q; want empty", got)
		}
	})
}

// TestWebhook_QueryTokenIsNotACredential pins that `?token=` in the URL query string is rejected
// (D-13: credentials must never ride in URLs where proxies/gateways log them).
func TestWebhook_QueryTokenIsNotACredential(t *testing.T) {
	const validToken = "query-token-xyz"
	h := webhook.NewHandler(webhook.HandlerOptions{
		Credentials: []config.Credential{{Name: "dev", Token: validToken}},
		Processor:   &mockProcessor{pass: true},
		Deliverer:   &mockDeliverer{},
	})

	req := httptest.NewRequest(http.MethodPost, "/webhook?token="+validToken, bytes.NewBufferString(`{"msg_type":"text"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 when token is only in query string, got %d (%s)", rec.Code, rec.Body.String())
	}

	// Regression guard: the same token in Authorization: Bearer must still succeed.
	reqOK := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"msg_type":"text"}`))
	reqOK.Header.Set("Authorization", "Bearer "+validToken)
	recOK := httptest.NewRecorder()
	h.ServeHTTP(recOK, reqOK)
	if recOK.Code != http.StatusOK {
		t.Fatalf("expected 200 for Authorization: Bearer, got %d", recOK.Code)
	}
}

// TestWebhook_UnauthorizedChallenge verifies the WWW-Authenticate challenge required
// by RFC 9110 section 15.5.2 for HTTP Basic Auth (the legacy implementation sent it too).
func TestWebhook_UnauthorizedChallenge(t *testing.T) {
	t.Run("Basic Auth configured advertises the Basic challenge", func(t *testing.T) {
		h := webhook.NewHandler(webhook.HandlerOptions{
			Credentials: []config.Credential{{Username: "admin", Password: "secret"}},
			Processor:   &mockProcessor{pass: true},
			Deliverer:   &mockDeliverer{},
		})
		req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"msg_type":"text"}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", rec.Code)
		}
		challenge := rec.Header().Get("WWW-Authenticate")
		if !strings.HasPrefix(challenge, "Basic ") || !strings.Contains(challenge, `realm=`) {
			t.Errorf("expected a Basic WWW-Authenticate challenge, got %q", challenge)
		}
	})

	t.Run("Bearer-only configuration does not advertise a Basic challenge", func(t *testing.T) {
		h := webhook.NewHandler(webhook.HandlerOptions{
			Credentials: []config.Credential{{Token: "token-only"}},
			Processor:   &mockProcessor{pass: true},
			Deliverer:   &mockDeliverer{},
		})
		req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"msg_type":"text"}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", rec.Code)
		}
		if challenge := rec.Header().Get("WWW-Authenticate"); challenge != "" {
			t.Errorf("expected no WWW-Authenticate for token-only auth, got %q", challenge)
		}
	})
}

func TestWebhook_MultiCredentialAuth(t *testing.T) {
	creds := []config.Credential{
		{Name: "phone-a", Token: "tok-a"},
		{Name: "tablet", Username: "tab-user", Password: "tab-pass"},
		{Name: "phone-b", Token: "tok-b", Username: "phone-user", Password: "phone-pass"},
	}

	h := webhook.NewHandler(webhook.HandlerOptions{
		Credentials: creds,
		Processor:   &mockProcessor{pass: true},
		Deliverer:   &mockDeliverer{},
	})

	t.Run("bearer tok-a -> 200", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"msg_type":"text"}`))
		req.Header.Set("Authorization", "Bearer tok-a")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rec.Code)
		}
	})

	t.Run("basic tab-user:tab-pass -> 200", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"msg_type":"text"}`))
		req.SetBasicAuth("tab-user", "tab-pass")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rec.Code)
		}
	})

	t.Run("bearer tok-b -> 200", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"msg_type":"text"}`))
		req.Header.Set("Authorization", "Bearer tok-b")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rec.Code)
		}
	})

	t.Run("basic phone-user:phone-pass -> 200", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"msg_type":"text"}`))
		req.SetBasicAuth("phone-user", "phone-pass")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rec.Code)
		}
	})

	t.Run("cross credential basic mismatch -> 401", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"msg_type":"text"}`))
		req.SetBasicAuth("tab-user", "phone-pass")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", rec.Code)
		}
	})

	t.Run("missing credentials with mixed set advertises basic challenge", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"msg_type":"text"}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", rec.Code)
		}
		challenge := rec.Header().Get("WWW-Authenticate")
		if !strings.HasPrefix(challenge, "Basic ") {
			t.Errorf("expected Basic WWW-Authenticate, got %q", challenge)
		}
	})

	t.Run("multiple tokens only configuration does not advertise basic challenge", func(t *testing.T) {
		hTokens := webhook.NewHandler(webhook.HandlerOptions{
			Credentials: []config.Credential{
				{Name: "p1", Token: "tok-1"},
				{Name: "p2", Token: "tok-2"},
			},
			Processor: &mockProcessor{pass: true},
			Deliverer: &mockDeliverer{},
		})
		req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"msg_type":"text"}`))
		rec := httptest.NewRecorder()
		hTokens.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", rec.Code)
		}
		if challenge := rec.Header().Get("WWW-Authenticate"); challenge != "" {
			t.Errorf("expected no WWW-Authenticate header, got %q", challenge)
		}
	})
}

// TestWebhook_NoCredentialsIsFailClosed pins that there is no open-access mode: a handler built
// without credentials denies every request. Unauthenticated serving is prevented at startup
// (cmd/push-transfer), so reaching this state is a programming error and must fail closed.
func TestWebhook_NoCredentialsIsFailClosed(t *testing.T) {
	cases := []struct {
		name  string
		creds []config.Credential
	}{
		{"nil credentials", nil},
		{"empty slice credentials", []config.Credential{}},
		{"blank-only credentials", []config.Credential{{Token: "  "}, {Username: " ", Password: ""}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := webhook.NewHandler(webhook.HandlerOptions{
				Credentials: tc.creds,
				Processor:   &mockProcessor{pass: true},
				Deliverer:   &mockDeliverer{},
			})

			for _, auth := range []struct {
				name     string
				setupReq func(r *http.Request)
			}{
				{"no auth", func(_ *http.Request) {}},
				{"with bearer", func(r *http.Request) { r.Header.Set("Authorization", "Bearer anything") }},
				{"with basic auth", func(r *http.Request) { r.SetBasicAuth("admin", "admin") }},
			} {
				req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"msg_type":"text"}`))
				auth.setupReq(req)
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)

				if rec.Code != http.StatusUnauthorized {
					t.Errorf("%s: expected fail-closed 401, got %d", auth.name, rec.Code)
				}
			}
		})
	}
}

func TestWebhook_DeclaredButBlankFailClosed(t *testing.T) {
	// If credentials slice is declared (len > 0), but all items contain only empty/blank values,
	// NewHandler must fail-closed: reject every request with 401, never allow open access.
	h := webhook.NewHandler(webhook.HandlerOptions{
		Credentials: []config.Credential{
			{Token: ""},
			{Username: "   ", Password: ""},
		},
		Processor: &mockProcessor{pass: true},
		Deliverer: &mockDeliverer{},
	})

	for _, tc := range []struct {
		name     string
		setupReq func(r *http.Request)
	}{
		{"no auth", func(_ *http.Request) {}},
		{"with bearer", func(r *http.Request) { r.Header.Set("Authorization", "Bearer anything") }},
		{"with basic auth", func(r *http.Request) { r.SetBasicAuth("admin", "admin") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"msg_type":"text"}`))
			tc.setupReq(req)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected fail-closed 401, got %d", rec.Code)
			}
		})
	}
}

// TestWebhook_AuthorizedAuditLog pins the audit contract of the multi-credential design:
// a successful authentication records exactly the credential name (never the secret), and a
// credential-less handler is fail-closed: it warns once and never records an authorization line.
func TestWebhook_AuthorizedAuditLog(t *testing.T) {
	newLogger := func(buf *bytes.Buffer) *slog.Logger {
		return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}

	t.Run("successful auth logs the credential name only", func(t *testing.T) {
		var buf bytes.Buffer
		const secret = "tok-super-secret"
		h := webhook.NewHandler(webhook.HandlerOptions{
			Credentials: []config.Credential{{Name: "phone-a", Token: secret}},
			Processor:   &mockProcessor{pass: true},
			Deliverer:   &mockDeliverer{},
			Logger:      newLogger(&buf),
		})

		req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"msg_type":"text"}`))
		req.Header.Set("Authorization", "Bearer "+secret)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		logs := buf.String()
		if !strings.Contains(logs, "credential=phone-a") {
			t.Errorf("expected credential name in audit log, got:\n%s", logs)
		}
		if strings.Contains(logs, secret) {
			t.Errorf("audit log must never contain credential material, got:\n%s", logs)
		}
	})

	t.Run("credential-less handler logs a fail-closed warning and no authorization line", func(t *testing.T) {
		var buf bytes.Buffer
		h := webhook.NewHandler(webhook.HandlerOptions{
			Processor: &mockProcessor{pass: true},
			Deliverer: &mockDeliverer{},
			Logger:    newLogger(&buf),
		})

		req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"msg_type":"text"}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected fail-closed 401, got %d", rec.Code)
		}
		logs := buf.String()
		if strings.Contains(logs, "webhook authorized") {
			t.Errorf("a denied request must not log an authorization line, got:\n%s", logs)
		}
		if !strings.Contains(logs, "fail-closed") {
			t.Errorf("expected a fail-closed warning for a credential-less handler, got:\n%s", logs)
		}
	})

	t.Run("failed auth must not log any credential name", func(t *testing.T) {
		var buf bytes.Buffer
		h := webhook.NewHandler(webhook.HandlerOptions{
			Credentials: []config.Credential{{Name: "phone-a", Token: "tok-a"}},
			Processor:   &mockProcessor{pass: true},
			Deliverer:   &mockDeliverer{},
			Logger:      newLogger(&buf),
		})

		req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"msg_type":"text"}`))
		req.Header.Set("Authorization", "Bearer wrong")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", rec.Code)
		}
		logs := buf.String()
		if strings.Contains(logs, "credential=phone-a") || strings.Contains(logs, "webhook authorized") {
			t.Errorf("failed auth must not log an authorization line, got:\n%s", logs)
		}
	})

	t.Run("whitespace-only credential name is trimmed and suppresses authorization log line", func(t *testing.T) {
		var buf bytes.Buffer
		h := webhook.NewHandler(webhook.HandlerOptions{
			Credentials: []config.Credential{{Name: "   ", Token: "tok-ws"}},
			Processor:   &mockProcessor{pass: true},
			Deliverer:   &mockDeliverer{},
			Logger:      newLogger(&buf),
		})

		req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"msg_type":"text"}`))
		req.Header.Set("Authorization", "Bearer tok-ws")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		logs := buf.String()
		if strings.Contains(logs, `credential="   "`) || strings.Contains(logs, "webhook authorized") {
			t.Errorf("whitespace-only Name must not produce blank credential audit log, got:\n%s", logs)
		}
	})
}

func TestWebhook_StrictMsgTypeValidation(t *testing.T) {
	const validToken = "valid-token-123"
	h := webhook.NewHandler(webhook.HandlerOptions{
		Credentials: []config.Credential{{Token: validToken}},
		Processor:   &mockProcessor{pass: true},
		Deliverer:   &mockDeliverer{},
	})

	testCases := []struct {
		name         string
		payload      string
		expectedCode int
		expectedMsg  string
		checkBody    bool
	}{
		{
			name:         "missing msg_type -> 400 Bad Request",
			payload:      `{"title":"test","content":"hello"}`,
			expectedCode: http.StatusBadRequest,
			expectedMsg:  "invalid or missing msg_type",
			checkBody:    true,
		},
		{
			name:         "empty msg_type -> 400 Bad Request",
			payload:      `{"msg_type":"","title":"test"}`,
			expectedCode: http.StatusBadRequest,
			expectedMsg:  "invalid or missing msg_type",
			checkBody:    true,
		},
		{
			name:         "whitespace msg_type -> 400 Bad Request",
			payload:      `{"msg_type":"   ","title":"test"}`,
			expectedCode: http.StatusBadRequest,
			expectedMsg:  "invalid or missing msg_type",
			checkBody:    true,
		},
		{
			name:         "invalid msg_type -> 400 Bad Request",
			payload:      `{"msg_type":"invalid_type","title":"test"}`,
			expectedCode: http.StatusBadRequest,
			expectedMsg:  "invalid or missing msg_type",
			checkBody:    true,
		},
		{
			name:         "legacy sms type -> 400 Bad Request",
			payload:      `{"msg_type":"sms","title":"test"}`,
			expectedCode: http.StatusBadRequest,
			expectedMsg:  "invalid or missing msg_type",
			checkBody:    true,
		},
		{
			name:         "valid app_notification -> 200 OK",
			payload:      `{"msg_type":"app_notification","app_name":"微信","title":"工作群","content":"开会"}`,
			expectedCode: http.StatusOK,
			checkBody:    false,
		},
		{
			name:         "valid text -> 200 OK",
			payload:      `{"msg_type":"text","title":"10086","content":"话费提醒"}`,
			expectedCode: http.StatusOK,
			checkBody:    false,
		},
		{
			name:         "valid call -> 200 OK",
			payload:      `{"msg_type":"call","title":"13800138000"}`,
			expectedCode: http.StatusOK,
			checkBody:    false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(tc.payload))
			req.Header.Set("Authorization", "Bearer "+validToken)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tc.expectedCode {
				t.Fatalf("expected status %d, got %d: %s", tc.expectedCode, rec.Code, rec.Body.String())
			}

			if tc.checkBody {
				var res map[string]string
				if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
					t.Fatalf("failed to decode response JSON: %v", err)
				}
				if res["status"] != "failed" || res["message"] != tc.expectedMsg {
					t.Fatalf("expected status 'failed' and message %q, got: %+v", tc.expectedMsg, res)
				}
			}
		})
	}
}

type concurrentMockTarget struct {
	name      string
	callCount int64
	onSend    func(msg *model.Message)
}

func (m *concurrentMockTarget) Name() string { return m.name }
func (m *concurrentMockTarget) Send(_ context.Context, msg *model.Message) error {
	atomic.AddInt64(&m.callCount, 1)
	if m.onSend != nil {
		m.onSend(msg)
	}
	return nil
}

func TestWebhook_AppNotification_EndToEnd_ConcurrentDispatch(t *testing.T) {
	rules := config.RulesConfig{
		FilterWhite: []string{`(?i)重要`},
		FilterBlack: []string{`(?i)垃圾`},
		Captcha:     []string{`(?:验证码[:：为]?|code[:：])\s*(\d{4,6})`},
		Emergency:   []string{`(?i)critical|紧急`},
	}
	p, err := pipeline.NewPipelineFromRules(rules, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("failed to create pipeline: %v", err)
	}

	targetBark := &concurrentMockTarget{
		name: "bark",
		onSend: func(msg *model.Message) {
			if msg.MsgType != model.MsgTypeAppNotification {
				t.Errorf("bark: expected MsgTypeAppNotification, got %q", msg.MsgType)
			}
			if msg.AppName != "招商银行" {
				t.Errorf("bark: expected AppName '招商银行', got %q", msg.AppName)
			}
			if msg.CaptchaCode != "889911" {
				t.Errorf("bark: expected CaptchaCode '889911', got %q", msg.CaptchaCode)
			}
			if !msg.Emergency {
				t.Errorf("bark: expected Emergency true, got %v", msg.Emergency)
			}
			if msg.BatteryLevel == nil || *msg.BatteryLevel != 95 {
				t.Errorf("bark: expected BatteryLevel 95, got %v", msg.BatteryLevel)
			}
			// Attempt mutation to test clone isolation across all channels
			msg.AppName = "MutatedInBark"
			msg.Title = "MutatedTitleInBark"
			if msg.BatteryLevel != nil {
				*msg.BatteryLevel = 1
			}
		},
	}

	targetTG := &concurrentMockTarget{
		name: "telegram",
		onSend: func(msg *model.Message) {
			if msg.AppName != "招商银行" {
				t.Errorf("telegram: expected AppName '招商银行', got %q", msg.AppName)
			}
			text := msg.GenComplexText()
			if !strings.HasPrefix(text, "【招商银行】紧急动账\n\n") || !strings.Contains(text, "当前电量: 95%") {
				t.Errorf("telegram: unexpected GenComplexText: %q", text)
			}
			msg.AppName = "MutatedInTG"
			if msg.BatteryLevel != nil {
				*msg.BatteryLevel = 2
			}
		},
	}

	expectedTimeStr := "通知时间：" + time.Unix(1700000000, 0).Format("2006-01-02 15:04:05")
	targetEmail := &concurrentMockTarget{
		name: "email",
		onSend: func(msg *model.Message) {
			if msg.AppName != "招商银行" || msg.Title != "紧急动账" {
				t.Errorf("email: expected AppName='招商银行' Title='紧急动账', got %q / %q", msg.AppName, msg.Title)
			}
			text := msg.GenComplexText()
			if !strings.HasPrefix(text, "【招商银行】紧急动账\n\n") || !strings.Contains(text, expectedTimeStr) {
				t.Errorf("email: unexpected GenComplexText: %q", text)
			}
			msg.AppName = "MutatedInEmail"
		},
	}

	targetConsole := &concurrentMockTarget{
		name: "console",
		onSend: func(msg *model.Message) {
			if msg.AppName != "招商银行" {
				t.Errorf("console: expected AppName '招商银行', got %q", msg.AppName)
			}
			if msg.BatteryLevel == nil || *msg.BatteryLevel != 95 {
				t.Errorf("console: expected isolated BatteryLevel 95, got %v", msg.BatteryLevel)
			}
			msg.AppName = "MutatedInConsole"
		},
	}

	disp := deliver.NewDispatcher(deliver.DispatcherOptions{
		Targets: []deliver.Target{targetBark, targetTG, targetEmail, targetConsole},
		Timeout: 2 * time.Second,
	})

	const validToken = "concurrent-e2e-token"
	h := webhook.NewHandler(webhook.HandlerOptions{
		Credentials: []config.Credential{{Token: validToken}},
		Processor:   p,
		Deliverer:   disp,
	})

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			payload := fmt.Sprintf(`{
				"msg_type": "app_notification",
				"app_name": "招商银行",
				"title": "紧急动账",
				"content": "【银行】紧急提醒：您的验证码为 889911，请勿泄露。序列号：%d",
				"battery_level": 95,
				"timestamp": 1700000000000
			}`, idx)

			req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(payload))
			req.Header.Set("Authorization", "Bearer "+validToken)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Errorf("routine %d: expected 200 OK, got %d: %s", idx, rec.Code, rec.Body.String())
				return
			}

			var res map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
				t.Errorf("routine %d: json unmarshal failed: %v", idx, err)
				return
			}
			if res["status"] != "success" {
				t.Errorf("routine %d: expected status 'success', got %v", idx, res["status"])
			}
		}(i)
	}

	wg.Wait()

	for _, target := range []*concurrentMockTarget{targetBark, targetTG, targetEmail, targetConsole} {
		if target.callCount != goroutines {
			t.Fatalf("expected %d calls to %s target, got %d", goroutines, target.name, target.callCount)
		}
	}
}

type captureDeliverer struct {
	mu  sync.Mutex
	msg *model.Message
}

func (c *captureDeliverer) Deliver(_ context.Context, msg *model.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msg = msg.Clone()
	return nil
}

func TestWebhook_ProtocolExtension_EndToEnd(t *testing.T) {
	pipe, err := pipeline.NewPipelineFromRules(config.RulesConfig{
		Captcha: []string{`验证码[是为：:]*\s*(\d{4,6})`},
	}, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("failed to create pipeline: %v", err)
	}

	capture := &captureDeliverer{}
	token := "token-e2e"

	h := webhook.NewHandler(webhook.HandlerOptions{
		Credentials: []config.Credential{{Token: token}},
		Processor:   pipe,
		Deliverer:   capture,
	})

	payload := `{
		"msg_type": "text",
		"title": "招商银行",
		"content": "【招商银行】您的验证码是 654321，5分钟内有效。",
		"hostname": "pixel-gateway",
		"battery_level": "85%",
		"time_sensitive": true,
		"timestamp": "2026-10-18T00:15:20+08:00",
		"send_time": "Sun, 18 Oct 2026 00:15:20 +0800"
	}`

	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(payload))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}

	capture.mu.Lock()
	delivered := capture.msg
	capture.mu.Unlock()

	if delivered == nil {
		t.Fatal("expected delivered message, got nil")
	}

	if delivered.Timestamp != 1792253720 {
		t.Errorf("expected Timestamp 1792253720, got %d", delivered.Timestamp)
	}
	if delivered.SendTime != 1792253720 {
		t.Errorf("expected SendTime 1792253720, got %d", delivered.SendTime)
	}
	if delivered.BatteryLevel == nil || *delivered.BatteryLevel != 85 {
		t.Errorf("expected BatteryLevel 85, got %v", delivered.BatteryLevel)
	}
	if delivered.Hostname != "pixel-gateway" {
		t.Errorf("expected Hostname 'pixel-gateway', got %q", delivered.Hostname)
	}
	if !delivered.TimeSensitive {
		t.Errorf("expected TimeSensitive true, got false")
	}
	if delivered.CaptchaCode != "654321" {
		t.Errorf("expected CaptchaCode '654321', got %q", delivered.CaptchaCode)
	}

	cst := time.FixedZone("Asia/Shanghai", 8*3600)
	complexText := delivered.GenComplexText(cst)
	if !strings.Contains(complexText, "设备主机名: pixel-gateway") {
		t.Errorf("expected complex text to contain '设备主机名: pixel-gateway', got:\n%s", complexText)
	}
	if !strings.Contains(complexText, "当前电量: 85%") {
		t.Errorf("expected complex text to contain '当前电量: 85%%', got:\n%s", complexText)
	}
}

func TestWebhook_TimeSensitive_FlexibleVariants(t *testing.T) {
	testCases := []struct {
		name          string
		timeSensitive string
		expected      bool
	}{
		{"string true", `"true"`, true},
		{"string false", `"false"`, false},
		{"integer 1", `1`, true},
		{"integer 0", `0`, false},
		{"string 1", `"1"`, true},
		{"string 0", `"0"`, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			pipe, err := pipeline.NewPipelineFromRules(config.RulesConfig{}, 500*time.Millisecond)
			if err != nil {
				t.Fatalf("failed to create pipeline: %v", err)
			}
			capture := &captureDeliverer{}
			token := "sec-token-12345678"

			h := webhook.NewHandler(webhook.HandlerOptions{
				Credentials: []config.Credential{{Token: token}},
				Processor:   pipe,
				Deliverer:   capture,
			})

			payload := fmt.Sprintf(`{
				"msg_type": "text",
				"title": "测试",
				"content": "无验证码的普通消息",
				"time_sensitive": %s
			}`, tc.timeSensitive)

			req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(payload))
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()

			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
			}

			capture.mu.Lock()
			delivered := capture.msg
			capture.mu.Unlock()

			if delivered == nil {
				t.Fatal("expected delivered message, got nil")
			}
			if delivered.TimeSensitive != tc.expected {
				t.Errorf("expected TimeSensitive %v for %s, got %v", tc.expected, tc.timeSensitive, delivered.TimeSensitive)
			}
		})
	}
}
