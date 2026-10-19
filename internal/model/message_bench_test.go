package model

import (
	"encoding/json"
	"testing"
	"time"
)

func BenchmarkFlexibleTimestamp_Numeric(b *testing.B) {
	raw := []byte(`{"time": 1700000000}`)
	var wrapper struct {
		Time FlexibleTimestamp `json:"time"`
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = json.Unmarshal(raw, &wrapper)
	}
}

func BenchmarkFlexibleTimestamp_ISO8601(b *testing.B) {
	raw := []byte(`{"time": "2026-10-18T00:15:20.123456+08:00"}`)
	var wrapper struct {
		Time FlexibleTimestamp `json:"time"`
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = json.Unmarshal(raw, &wrapper)
	}
}

func BenchmarkFlexibleTimestamp_RFC5322(b *testing.B) {
	raw := []byte(`{"time": "Sun, 18 Oct 2026 00:15:20 +0800"}`)
	var wrapper struct {
		Time FlexibleTimestamp `json:"time"`
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = json.Unmarshal(raw, &wrapper)
	}
}

func BenchmarkMessage_UnmarshalJSON(b *testing.B) {
	raw := []byte(`{
		"msg_type": "text",
		"title": "10086",
		"content": "您的余额为 58.20 元，本次扣费 10 元。",
		"app_name": "短信",
		"hostname": "Xiaomi-13",
		"battery_level": "85%",
		"time_sensitive": "true",
		"idempotency_key": "msg-uid-99887766",
		"send_time": 1700000000
	}`)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var msg Message
		_ = json.Unmarshal(raw, &msg)
	}
}

func BenchmarkMessage_Clone(b *testing.B) {
	battery := 85
	msg := &Message{
		MsgType:        MsgTypeText,
		Title:          "10086",
		Content:        "您的余额为 58.20 元，本次扣费 10 元。",
		AppName:        "短信",
		Hostname:       "Xiaomi-13",
		BatteryLevel:   &battery,
		TimeSensitive:  true,
		IdempotencyKey: "msg-uid-99887766",
		Timestamp:      1700000000,
		SendTime:       1700000000,
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = msg.Clone()
	}
}

func BenchmarkMessage_GenComplexText(b *testing.B) {
	battery := 85
	loc := time.FixedZone("CST", 8*3600)
	msg := &Message{
		MsgType:        MsgTypeText,
		Title:          "10086",
		Content:        "您的余额为 58.20 元，本次扣费 10 元。",
		AppName:        "短信",
		Hostname:       "Xiaomi-13",
		BatteryLevel:   &battery,
		TimeSensitive:  true,
		IdempotencyKey: "msg-uid-99887766",
		Timestamp:      1700000000,
		SendTime:       1700000000,
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = msg.GenComplexText(loc)
	}
}

func BenchmarkSanitizeSingleLine(b *testing.B) {
	input := "Xiaomi-13 Pro\r\n\x00Attack\tHost"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = sanitizeSingleLine(input)
	}
}
