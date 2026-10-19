package webhook_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zapsaang/push-transfer/internal/config"
	"github.com/zapsaang/push-transfer/internal/ingress/webhook"
	"github.com/zapsaang/push-transfer/internal/model"
)

type noopProcessor struct{}

func (n *noopProcessor) Process(_ *model.Message) (bool, error) {
	return true, nil
}

type noopDeliverer struct{}

func (n *noopDeliverer) Deliver(_ context.Context, _ *model.Message) error {
	return nil
}

type fixedIdempotencyChecker struct {
	allow bool
}

func (f *fixedIdempotencyChecker) CheckAndRecord(_ string) bool {
	return f.allow
}

func BenchmarkAuth_SingleCredential(b *testing.B) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := webhook.NewHandler(webhook.HandlerOptions{
		Credentials: []config.Credential{
			{Name: "token-auth", Token: "super-secret-token-1234567890"},
		},
		Processor: &noopProcessor{},
		Deliverer: &noopDeliverer{},
		Logger:    logger,
	})

	body := []byte(`{"msg_type":"text","title":"Test","content":"Bench"}`)
	req, _ := http.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer super-secret-token-1234567890")
	req.Header.Set("Content-Type", "application/json")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req.Body = io.NopCloser(bytes.NewReader(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}
}

func BenchmarkAuth_TenCredentials(b *testing.B) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	creds := make([]config.Credential, 10)
	for i := 0; i < 10; i++ {
		creds[i] = config.Credential{
			Name:  fmt.Sprintf("cred-%d", i),
			Token: fmt.Sprintf("token-secret-value-%04d", i),
		}
	}

	h := webhook.NewHandler(webhook.HandlerOptions{
		Credentials: creds,
		Processor:   &noopProcessor{},
		Deliverer:   &noopDeliverer{},
		Logger:      logger,
	})

	// Authenticate with the 10th credential to ensure full constant-time sweep
	body := []byte(`{"msg_type":"text","title":"Test","content":"Bench"}`)
	req, _ := http.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer token-secret-value-0009")
	req.Header.Set("Content-Type", "application/json")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req.Body = io.NopCloser(bytes.NewReader(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}
}

func BenchmarkWebhookHandler_ServeHTTP_DuplicateShortCircuit(b *testing.B) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := webhook.NewHandler(webhook.HandlerOptions{
		Credentials: []config.Credential{
			{Name: "test", Token: "secret-token"},
		},
		Processor:          &noopProcessor{},
		Deliverer:          &noopDeliverer{},
		IdempotencyChecker: &fixedIdempotencyChecker{allow: false}, // duplicate!
		Logger:             logger,
	})

	body := []byte(`{"msg_type":"text","title":"Test","content":"Bench","idempotency_key":"duplicate-key-1"}`)
	req, _ := http.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret-token")
	req.Header.Set("Content-Type", "application/json")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req.Body = io.NopCloser(bytes.NewReader(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}
}

func BenchmarkWebhookHandler_ServeHTTP_Unauthorized(b *testing.B) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := webhook.NewHandler(webhook.HandlerOptions{
		Credentials: []config.Credential{
			{Name: "test", Token: "secret-token"},
		},
		Processor: &noopProcessor{},
		Deliverer: &noopDeliverer{},
		Logger:    logger,
	})

	body := []byte(`{"msg_type":"text","title":"Test","content":"Bench"}`)
	req, _ := http.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer wrong-token")
	req.Header.Set("Content-Type", "application/json")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req.Body = io.NopCloser(bytes.NewReader(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}
}
