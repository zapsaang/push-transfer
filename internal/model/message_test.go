package model

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestFlexibleTimestamp(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected FlexibleTimestamp
	}{
		{"null", `{"time": null}`, 0},
		{"empty string", `{"time": ""}`, 0},
		{"zero", `{"time": 0}`, 0},
		{"negative", `{"time": -100}`, -100},
		{"10-digit seconds", `{"time": 1700000000}`, 1700000000},
		{"13-digit milliseconds", `{"time": 1700000000000}`, 1700000000},
		{"string 10-digit", `{"time": "1700000000"}`, 1700000000},
		{"string 13-digit", `{"time": "1700000000000"}`, 1700000000},
		{"string null", `{"time": "null"}`, 0},
		{"non-numeric string", `{"time": "invalid_date"}`, 0},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var wrapper struct {
				Time FlexibleTimestamp `json:"time"`
			}
			err := json.Unmarshal([]byte(tc.input), &wrapper)
			if err != nil {
				t.Fatalf("unexpected unmarshal error: %v", err)
			}
			if wrapper.Time != tc.expected {
				t.Fatalf("expected timestamp %d, got %d", tc.expected, wrapper.Time)
			}
		})
	}
}

func TestFlexibleTimestamp_Format(t *testing.T) {
	cst := time.FixedZone("Asia/Shanghai", 8*3600)

	// Zero or negative returns "未知时间"
	var zero FlexibleTimestamp
	if formatted := zero.Format(cst); formatted != "未知时间" {
		t.Fatalf("expected '未知时间' for 0, got %q", formatted)
	}

	var negative FlexibleTimestamp = -123
	if formatted := negative.Format(cst); formatted != "未知时间" {
		t.Fatalf("expected '未知时间' for negative, got %q", formatted)
	}

	// Valid timestamp (1700000000 = 2023-11-14 22:13:20 UTC = 2023-11-15 06:13:20 CST)
	var ts FlexibleTimestamp = 1700000000
	expected := "2023-11-15 06:13:20"
	if formatted := ts.Format(cst); formatted != expected {
		t.Fatalf("expected %q, got %q", expected, formatted)
	}
}

func TestMessage_UnmarshalJSON(t *testing.T) {
	payload := `{
		"msg_type": "text",
		"title": "10086",
		"content": "【中国移动】您的验证码是 952701，请勿泄露。",
		"battery_level": 88,
		"timestamp": 1700000000000,
		"send_time": "1700000000000",
		"receive_time": 1700000010000,
		"transmit_time": 1700000005000,
		"incoming_time": null,
		"emergency": false,
		"captcha_code": "952701"
	}`

	var msg Message
	err := json.Unmarshal([]byte(payload), &msg)
	if err != nil {
		t.Fatalf("UnmarshalJSON failed: %v", err)
	}

	if msg.MsgType != "text" {
		t.Errorf("expected MsgType 'text', got %q", msg.MsgType)
	}
	if msg.Title != "10086" {
		t.Errorf("expected Title '10086', got %q", msg.Title)
	}
	if !strings.Contains(msg.Content, "952701") {
		t.Errorf("expected content to contain '952701', got %q", msg.Content)
	}
	if msg.BatteryLevel == nil || *msg.BatteryLevel != 88 {
		t.Errorf("expected BatteryLevel 88, got %v", msg.BatteryLevel)
	}
	if msg.Timestamp != 1700000000 {
		t.Errorf("expected Timestamp 1700000000, got %d", msg.Timestamp)
	}
	if msg.SendTime != 1700000000 {
		t.Errorf("expected SendTime 1700000000, got %d", msg.SendTime)
	}
	if msg.ReceiveTime != 1700000010 {
		t.Errorf("expected ReceiveTime 1700000010, got %d", msg.ReceiveTime)
	}
	if msg.TransmitTime != 1700000005 {
		t.Errorf("expected TransmitTime 1700000005, got %d", msg.TransmitTime)
	}
	if msg.IncomingTime != 0 {
		t.Errorf("expected IncomingTime 0 for null, got %d", msg.IncomingTime)
	}
	if msg.Emergency != false {
		t.Errorf("expected Emergency false, got %v", msg.Emergency)
	}
	if msg.CaptchaCode != "952701" {
		t.Errorf("expected CaptchaCode '952701', got %q", msg.CaptchaCode)
	}
}

func TestMessage_GenComplexText(t *testing.T) {
	cst := time.FixedZone("Asia/Shanghai", 8*3600)
	batt := 85

	// 1. Text message
	textMsg := Message{
		MsgType:      "text",
		Title:        "10086",
		Content:      "您的验证码是 9527",
		BatteryLevel: &batt,
		TransmitTime: 1700000000, // 2023-11-15 06:13:20 CST
		ReceiveTime:  1700000010, // 2023-11-15 06:13:30 CST
	}

	expectedText := "您的验证码是 9527\n\n短信来自: 10086\n\n发送时间：2023-11-15 06:13:20\n转发时间：2023-11-15 06:13:30\n\n当前电量: 85%\n"
	if got := textMsg.GenComplexText(cst); got != expectedText {
		t.Fatalf("GenComplexText text message mismatch:\nwant:\n%q\ngot:\n%q", expectedText, got)
	}

	// 2. Call message
	callMsg := Message{
		MsgType:      "call",
		Title:        "张三 (13800138000)",
		BatteryLevel: &batt,
		IncomingTime: 1700000000, // 2023-11-15 06:13:20 CST
	}

	expectedCall := "未接来电：张三 (13800138000)\n\n来电时间：2023-11-15 06:13:20\n\n当前电量: 85%\n"
	if got := callMsg.GenComplexText(cst); got != expectedCall {
		t.Fatalf("GenComplexText call message mismatch:\nwant:\n%q\ngot:\n%q", expectedCall, got)
	}

	// 3. Message without battery level
	callNoBatt := Message{
		MsgType:      "call",
		Title:        "10010",
		IncomingTime: 1700000000,
	}
	expectedNoBatt := "未接来电：10010\n\n来电时间：2023-11-15 06:13:20\n"
	if got := callNoBatt.GenComplexText(cst); got != expectedNoBatt {
		t.Fatalf("GenComplexText without battery mismatch:\nwant:\n%q\ngot:\n%q", expectedNoBatt, got)
	}

	// 4. Message with unknown time (<= 0)
	callUnknownTime := Message{
		MsgType: "call",
		Title:   "10010",
	}
	expectedUnknown := "未接来电：10010\n\n来电时间：未知时间\n"
	if got := callUnknownTime.GenComplexText(cst); got != expectedUnknown {
		t.Fatalf("GenComplexText with unknown time mismatch:\nwant:\n%q\ngot:\n%q", expectedUnknown, got)
	}

	// 5. Default message type and nil location
	otherMsg := Message{
		Title:   "Notice",
		Content: "System rebooting",
	}
	otherMsg.SetCaptcha("666888")
	otherMsg.SetEmergency(true)
	if otherMsg.CaptchaCode != "666888" || !otherMsg.Emergency {
		t.Fatalf("SetCaptcha or SetEmergency failed")
	}
	expectedOther := "Notice\n\nSystem rebooting\n"
	if got := otherMsg.GenComplexText(); got != expectedOther {
		t.Fatalf("GenComplexText default mismatch: want %q, got %q", expectedOther, got)
	}
}

func TestMessage_GenComplexText_TimestampFallback(t *testing.T) {
	cst := time.FixedZone("Asia/Shanghai", 8*3600)

	t.Run("call fallback to Timestamp", func(t *testing.T) {
		msg := Message{
			MsgType:   "call",
			Title:     "13800138000",
			Timestamp: 1700000000, // 2023-11-15 06:13:20 CST
		}
		got := msg.GenComplexText(cst)
		want := "未接来电：13800138000\n\n来电时间：2023-11-15 06:13:20\n"
		if got != want {
			t.Fatalf("call Timestamp fallback mismatch:\nwant: %q\ngot:  %q", want, got)
		}
	})

	t.Run("call fallback to SendTime", func(t *testing.T) {
		msg := Message{
			MsgType:  "call",
			Title:    "13800138000",
			SendTime: 1700000000,
		}
		got := msg.GenComplexText(cst)
		want := "未接来电：13800138000\n\n来电时间：2023-11-15 06:13:20\n"
		if got != want {
			t.Fatalf("call SendTime fallback mismatch:\nwant: %q\ngot:  %q", want, got)
		}
	})

	t.Run("call fallback to ReceiveTime", func(t *testing.T) {
		msg := Message{
			MsgType:     "call",
			Title:       "13800138000",
			ReceiveTime: 1700000000,
		}
		got := msg.GenComplexText(cst)
		want := "未接来电：13800138000\n\n来电时间：2023-11-15 06:13:20\n"
		if got != want {
			t.Fatalf("call ReceiveTime fallback mismatch:\nwant: %q\ngot:  %q", want, got)
		}
	})

	t.Run("text send time fallback to SendTime and receive time fallback to Timestamp", func(t *testing.T) {
		msg := Message{
			MsgType:   "text",
			Title:     "10086",
			Content:   "余额不足",
			SendTime:  1700000000, // 2023-11-15 06:13:20 CST
			Timestamp: 1700000010, // 2023-11-15 06:13:30 CST
		}
		got := msg.GenComplexText(cst)
		want := "余额不足\n\n短信来自: 10086\n\n发送时间：2023-11-15 06:13:20\n转发时间：2023-11-15 06:13:30\n"
		if got != want {
			t.Fatalf("text SendTime/Timestamp fallback mismatch:\nwant: %q\ngot:  %q", want, got)
		}
	})

	t.Run("text send and receive time both fallback to Timestamp", func(t *testing.T) {
		msg := Message{
			MsgType:   "text",
			Title:     "10086",
			Content:   "账单提醒",
			Timestamp: 1700000000, // 2023-11-15 06:13:20 CST
		}
		got := msg.GenComplexText(cst)
		want := "账单提醒\n\n短信来自: 10086\n\n发送时间：2023-11-15 06:13:20\n转发时间：2023-11-15 06:13:20\n"
		if got != want {
			t.Fatalf("text Timestamp fallback mismatch:\nwant: %q\ngot:  %q", want, got)
		}
	})
}

func TestMessage_Clone(t *testing.T) {
	t.Run("nil receiver returns nil", func(t *testing.T) {
		var nilMsg *Message
		if cloned := nilMsg.Clone(); cloned != nil {
			t.Fatalf("expected nil from nil receiver Clone(), got %+v", cloned)
		}
	})

	t.Run("nil BatteryLevel deep copy isolation", func(t *testing.T) {
		orig := &Message{
			MsgType:   "text",
			Title:     "10086",
			Content:   "Hello",
			Timestamp: 1700000000,
		}
		cloned := orig.Clone()
		if cloned == nil || cloned == orig {
			t.Fatalf("expected distinct non-nil pointer from Clone()")
		}
		if cloned.BatteryLevel != nil {
			t.Fatalf("expected nil BatteryLevel, got %v", cloned.BatteryLevel)
		}
		cloned.Title = "MutatedTitle"
		if orig.Title != "10086" {
			t.Fatalf("mutating cloned.Title affected orig.Title: got %q", orig.Title)
		}
	})

	t.Run("non-nil BatteryLevel mutation isolation", func(t *testing.T) {
		batt := 90
		orig := &Message{
			MsgType:      "call",
			Title:        "13800138000",
			Content:      "Missed call",
			BatteryLevel: &batt,
			IncomingTime: 1700000000,
			Emergency:    true,
			CaptchaCode:  "123456",
		}
		cloned := orig.Clone()
		if cloned == nil || cloned == orig {
			t.Fatalf("expected distinct non-nil pointer from Clone()")
		}
		if cloned.BatteryLevel == nil || cloned.BatteryLevel == orig.BatteryLevel {
			t.Fatalf("expected distinct BatteryLevel pointer in cloned message")
		}
		if *cloned.BatteryLevel != 90 {
			t.Fatalf("expected *cloned.BatteryLevel == 90, got %d", *cloned.BatteryLevel)
		}

		// Mutate cloned fields and pointer target
		*cloned.BatteryLevel = 15
		cloned.Title = "Mutated"
		cloned.Emergency = false
		cloned.CaptchaCode = "000000"

		if *orig.BatteryLevel != 90 {
			t.Fatalf("mutating *cloned.BatteryLevel affected *orig.BatteryLevel: got %d", *orig.BatteryLevel)
		}
		if orig.Title != "13800138000" || !orig.Emergency || orig.CaptchaCode != "123456" {
			t.Fatalf("mutating cloned fields affected orig: %+v", orig)
		}
	})
}
