package webhook_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/zapsaang/push-transfer/internal/config"
	"github.com/zapsaang/push-transfer/internal/ingress/webhook"
	"github.com/zapsaang/push-transfer/internal/model"
)

type countingDeliverer struct {
	mu    sync.Mutex
	count int
}

func (d *countingDeliverer) Deliver(_ context.Context, _ *model.Message) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.count++
	return nil
}

func (d *countingDeliverer) Count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.count
}

type spyIdempotencyChecker struct {
	mu           sync.Mutex
	recordedKeys []string
	seen         map[string]bool
}

func newSpyIdempotencyChecker() *spyIdempotencyChecker {
	return &spyIdempotencyChecker{
		seen: make(map[string]bool),
	}
}

func (c *spyIdempotencyChecker) CheckAndRecord(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recordedKeys = append(c.recordedKeys, key)
	if c.seen[key] {
		return false
	}
	c.seen[key] = true
	return true
}

func (c *spyIdempotencyChecker) RecordedKeys() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	ret := make([]string, len(c.recordedKeys))
	copy(ret, c.recordedKeys)
	return ret
}

func TestWebhook_Idempotency(t *testing.T) {
	token := "valid-token-12345"
	creds := []config.Credential{{Token: token}}

	t.Run("1. first request with key succeeds and flows through deliverer", func(t *testing.T) {
		del := &countingDeliverer{}
		checker := newSpyIdempotencyChecker()
		h := webhook.NewHandler(webhook.HandlerOptions{
			Credentials:        creds,
			Processor:          &mockProcessor{pass: true},
			Deliverer:          del,
			IdempotencyChecker: checker,
		})

		body := `{"msg_type":"text","title":"Test Title","content":"Test Content","idempotency_key":"order-001"}`
		req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()

		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d; body: %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"status":"success"`) {
			t.Fatalf("expected status success in response, got %s", rec.Body.String())
		}
		if del.Count() != 1 {
			t.Fatalf("expected deliverer to be called once, got %d", del.Count())
		}
		if len(checker.RecordedKeys()) != 1 || checker.RecordedKeys()[0] != "order-001" {
			t.Fatalf("expected checker to record order-001, got %v", checker.RecordedKeys())
		}
	})

	t.Run("2. duplicate key short-circuits with 200 and logs INFO, deliverer count stays 1", func(t *testing.T) {
		var logBuf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))

		del := &countingDeliverer{}
		checker := newSpyIdempotencyChecker()
		h := webhook.NewHandler(webhook.HandlerOptions{
			Credentials:        creds,
			Processor:          &mockProcessor{pass: true},
			Deliverer:          del,
			IdempotencyChecker: checker,
			Logger:             logger,
		})

		body := `{"msg_type":"text","title":"SMS 1","content":"Hello","idempotency_key":"dup-key-999"}`

		// First call
		req1 := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(body))
		req1.Header.Set("Authorization", "Bearer "+token)
		rec1 := httptest.NewRecorder()
		h.ServeHTTP(rec1, req1)

		if rec1.Code != http.StatusOK {
			t.Fatalf("first request expected 200 OK, got %d", rec1.Code)
		}
		if del.Count() != 1 {
			t.Fatalf("deliverer expected called 1 time, got %d", del.Count())
		}

		// Second duplicate call
		req2 := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(body))
		req2.Header.Set("Authorization", "Bearer "+token)
		rec2 := httptest.NewRecorder()
		h.ServeHTTP(rec2, req2)

		if rec2.Code != http.StatusOK {
			t.Fatalf("second duplicate request expected 200 OK, got %d", rec2.Code)
		}
		if !strings.Contains(rec2.Body.String(), `"status":"success"`) {
			t.Fatalf("expected status success in response, got %s", rec2.Body.String())
		}
		// Deliverer count MUST stay 1
		if del.Count() != 1 {
			t.Fatalf("expected deliverer count to stay 1, got %d", del.Count())
		}
		// Verify audit log contains duplicate ignore message
		logOutput := logBuf.String()
		if !strings.Contains(logOutput, "webhook message ignored due to duplicate idempotency key") {
			t.Fatalf("expected INFO audit log for duplicate idempotency key, got:\n%s", logOutput)
		}
		if !strings.Contains(logOutput, "dup-key-999") {
			t.Fatalf("expected log to contain idempotency key, got:\n%s", logOutput)
		}
	})

	t.Run("3. requests without idempotency_key are always allowed", func(t *testing.T) {
		del := &countingDeliverer{}
		checker := newSpyIdempotencyChecker()
		h := webhook.NewHandler(webhook.HandlerOptions{
			Credentials:        creds,
			Processor:          &mockProcessor{pass: true},
			Deliverer:          del,
			IdempotencyChecker: checker,
		})

		body := `{"msg_type":"text","title":"No Key","content":"Message without key"}`

		for i := 0; i < 3; i++ {
			req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(body))
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("call %d expected 200 OK, got %d", i, rec.Code)
			}
		}

		if del.Count() != 3 {
			t.Fatalf("expected deliverer called 3 times, got %d", del.Count())
		}
		if len(checker.RecordedKeys()) != 0 {
			t.Fatalf("expected checker not to be called for empty key, got %v", checker.RecordedKeys())
		}
	})

	t.Run("4. invalid msg_type returns 400 and does NOT record idempotency key", func(t *testing.T) {
		del := &countingDeliverer{}
		checker := newSpyIdempotencyChecker()
		h := webhook.NewHandler(webhook.HandlerOptions{
			Credentials:        creds,
			Processor:          &mockProcessor{pass: true},
			Deliverer:          del,
			IdempotencyChecker: checker,
		})

		// Missing or invalid msg_type
		body := `{"msg_type":"unsupported_type","title":"Title","idempotency_key":"key-should-not-occupy"}`
		req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request, got %d", rec.Code)
		}
		if len(checker.RecordedKeys()) != 0 {
			t.Fatalf("checker should NOT be called for invalid msg_type, got %v", checker.RecordedKeys())
		}
		if del.Count() != 0 {
			t.Fatalf("deliverer should not be called, got %d", del.Count())
		}
	})

	t.Run("5. unauthorized request returns 401 and does NOT record idempotency key", func(t *testing.T) {
		del := &countingDeliverer{}
		checker := newSpyIdempotencyChecker()
		h := webhook.NewHandler(webhook.HandlerOptions{
			Credentials:        creds,
			Processor:          &mockProcessor{pass: true},
			Deliverer:          del,
			IdempotencyChecker: checker,
		})

		body := `{"msg_type":"text","title":"Title","idempotency_key":"unauth-key"}`
		req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer wrong-token")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 Unauthorized, got %d", rec.Code)
		}
		if len(checker.RecordedKeys()) != 0 {
			t.Fatalf("checker should NOT be called on 401, got %v", checker.RecordedKeys())
		}
		if del.Count() != 0 {
			t.Fatalf("deliverer should not be called, got %d", del.Count())
		}
	})

	t.Run("6. nil IdempotencyChecker safely allows requests with idempotency_key", func(t *testing.T) {
		del := &countingDeliverer{}
		h := webhook.NewHandler(webhook.HandlerOptions{
			Credentials: creds,
			Processor:   &mockProcessor{pass: true},
			Deliverer:   del,
			// IdempotencyChecker is nil
		})

		body := `{"msg_type":"text","title":"Title","content":"Content","idempotency_key":"some-key"}`
		req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", rec.Code)
		}
		if del.Count() != 1 {
			t.Fatalf("expected deliverer called once, got %d", del.Count())
		}
	})
}
