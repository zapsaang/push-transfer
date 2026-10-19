package deliver

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/zapsaang/push-transfer/internal/model"
)

func BenchmarkSafeTruncateTelegramText_Short(b *testing.B) {
	text := "Short notification message from 10086 &amp; bank."
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = SafeTruncateTelegramText(text, MaxTelegramTextRunes)
	}
}

func BenchmarkSafeTruncateTelegramText_Long(b *testing.B) {
	text := strings.Repeat("Long message content with &amp; entities. ", 200)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = SafeTruncateTelegramText(text, MaxTelegramTextRunes)
	}
}

func BenchmarkBarkDeliver_BuildPayload_Plain(b *testing.B) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bark := NewBarkDeliver(BarkOptions{
		DeviceKeys: []string{"device-key-1"},
		Logger:     logger,
	})

	basePayload := map[string]interface{}{
		"title": "Verification",
		"body":  "Code is 123456",
		"group": "SMS",
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := bark.buildEndpointPayload(basePayload, "device-key-1")
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBarkDeliver_BuildPayload_Encrypted(b *testing.B) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bark := NewBarkDeliver(BarkOptions{
		DeviceKeys: []string{"device-key-1"},
		CryptoKey:  []byte("1234567890123456"),
		Logger:     logger,
	})

	basePayload := map[string]interface{}{
		"title": "Verification",
		"body":  "Code is 123456",
		"group": "SMS",
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := bark.buildEndpointPayload(basePayload, "device-key-1")
		if err != nil {
			b.Fatal(err)
		}
	}
}

type benchMockTarget struct {
	name string
}

func (m *benchMockTarget) Name() string { return m.name }
func (m *benchMockTarget) Send(_ context.Context, _ *model.Message) error {
	return nil
}

func BenchmarkDispatcher_Fanout(b *testing.B) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dispatcher := NewDispatcher(DispatcherOptions{
		Targets: []Target{
			&benchMockTarget{name: "bark"},
			&benchMockTarget{name: "telegram"},
			&benchMockTarget{name: "email"},
			&benchMockTarget{name: "console"},
		},
		Timeout: 5 * time.Second,
		Logger:  logger,
	})

	battery := 90
	msg := &model.Message{
		MsgType:      model.MsgTypeText,
		Title:        "Alice",
		Content:      "Hello world from benchmark",
		AppName:      "Messages",
		Hostname:     "Pixel",
		BatteryLevel: &battery,
	}

	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = dispatcher.Deliver(ctx, msg)
	}
}
