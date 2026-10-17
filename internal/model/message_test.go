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
		{"string whitespace", `{"time": "   "}`, 0},
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

	if msg.MsgType != MsgTypeText {
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
		MsgType:      MsgTypeText,
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
		MsgType:      MsgTypeCall,
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
		MsgType:      MsgTypeCall,
		Title:        "10010",
		IncomingTime: 1700000000,
	}
	expectedNoBatt := "未接来电：10010\n\n来电时间：2023-11-15 06:13:20\n"
	if got := callNoBatt.GenComplexText(cst); got != expectedNoBatt {
		t.Fatalf("GenComplexText without battery mismatch:\nwant:\n%q\ngot:\n%q", expectedNoBatt, got)
	}

	// 4. Message with unknown time (<= 0)
	callUnknownTime := Message{
		MsgType: MsgTypeCall,
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
			MsgType:   MsgTypeCall,
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
			MsgType:  MsgTypeCall,
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
			MsgType:     MsgTypeCall,
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
			MsgType:   MsgTypeText,
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
			MsgType:   MsgTypeText,
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
			MsgType:   MsgTypeText,
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
			MsgType:      MsgTypeCall,
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

func TestMsgType_Valid(t *testing.T) {
	testCases := []struct {
		msgType  MsgType
		expected bool
	}{
		{MsgTypeText, true},
		{MsgTypeCall, true},
		{MsgTypeAppNotification, true},
		{MsgType(""), false},
		{MsgType("unknown"), false},
		{MsgType("sms"), false},
		{MsgType("App_Notification"), false},
		{MsgType("text "), false},
		{MsgType(" call"), false},
	}

	for _, tc := range testCases {
		t.Run(string(tc.msgType), func(t *testing.T) {
			if got := tc.msgType.Valid(); got != tc.expected {
				t.Fatalf("MsgType(%q).Valid() = %v; want %v", tc.msgType, got, tc.expected)
			}
		})
	}
}

func TestMessage_UnmarshalAppNotification(t *testing.T) {
	payload := `{
		"msg_type": "app_notification",
		"app_name": "微信",
		"title": "工作群",
		"content": "开会",
		"battery_level": 90,
		"timestamp": 1700000000000
	}`

	var msg Message
	err := json.Unmarshal([]byte(payload), &msg)
	if err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}

	if msg.MsgType != MsgTypeAppNotification {
		t.Errorf("expected MsgType %q, got %q", MsgTypeAppNotification, msg.MsgType)
	}
	if msg.AppName != "微信" {
		t.Errorf("expected AppName '微信', got %q", msg.AppName)
	}
	if msg.Title != "工作群" {
		t.Errorf("expected Title '工作群', got %q", msg.Title)
	}
	if msg.Content != "开会" {
		t.Errorf("expected Content '开会', got %q", msg.Content)
	}
	if msg.BatteryLevel == nil || *msg.BatteryLevel != 90 {
		t.Errorf("expected BatteryLevel 90, got %v", msg.BatteryLevel)
	}
	if msg.Timestamp != 1700000000 {
		t.Errorf("expected Timestamp 1700000000, got %d", msg.Timestamp)
	}
}

func TestMessage_GenComplexText_AppNotification_FourStates(t *testing.T) {
	cst := time.FixedZone("Asia/Shanghai", 8*3600)
	batt := 80

	t.Run("state 1: full info with app_name and title and content and battery", func(t *testing.T) {
		msg := Message{
			MsgType:      MsgTypeAppNotification,
			AppName:      " 微信 ",
			Title:        " 张三 ",
			Content:      " 你好 ",
			BatteryLevel: &batt,
			Timestamp:    1700000000, // 2023-11-15 06:13:20 CST
		}
		expected := "【微信】张三\n\n你好\n\n通知时间：2023-11-15 06:13:20\n\n当前电量: 80%\n"
		if got := msg.GenComplexText(cst); got != expected {
			t.Fatalf("State 1 mismatch:\nwant:\n%q\ngot:\n%q", expected, got)
		}
	})

	t.Run("state 2: app_name only (title is empty/whitespace)", func(t *testing.T) {
		msg := Message{
			MsgType:   MsgTypeAppNotification,
			AppName:   "钉钉",
			Title:     "  ",
			Content:   "签到提醒",
			Timestamp: 1700000000,
		}
		expected := "【钉钉】\n\n签到提醒\n\n通知时间：2023-11-15 06:13:20\n"
		if got := msg.GenComplexText(cst); got != expected {
			t.Fatalf("State 2 mismatch:\nwant:\n%q\ngot:\n%q", expected, got)
		}
	})

	t.Run("state 3: title only (app_name is empty)", func(t *testing.T) {
		msg := Message{
			MsgType:   MsgTypeAppNotification,
			AppName:   "",
			Title:     "系统告警",
			Content:   "CPU 负载高",
			Timestamp: 1700000000,
		}
		expected := "【应用通知】系统告警\n\nCPU 负载高\n\n通知时间：2023-11-15 06:13:20\n"
		if got := msg.GenComplexText(cst); got != expected {
			t.Fatalf("State 3 mismatch:\nwant:\n%q\ngot:\n%q", expected, got)
		}
	})

	t.Run("state 4: neither app_name nor title (both empty or whitespace, content empty)", func(t *testing.T) {
		msg := Message{
			MsgType:   MsgTypeAppNotification,
			AppName:   " ",
			Title:     "",
			Content:   "",
			Timestamp: 1700000000,
		}
		expected := "【应用通知】\n\n通知时间：2023-11-15 06:13:20\n"
		if got := msg.GenComplexText(cst); got != expected {
			t.Fatalf("State 4 mismatch:\nwant:\n%q\ngot:\n%q", expected, got)
		}
	})

	t.Run("timestamp fallback to TransmitTime when others are zero", func(t *testing.T) {
		msg := Message{
			MsgType:      MsgTypeAppNotification,
			AppName:      "微信",
			Title:        "通知",
			TransmitTime: 1700000000,
		}
		expected := "【微信】通知\n\n通知时间：2023-11-15 06:13:20\n"
		if got := msg.GenComplexText(cst); got != expected {
			t.Fatalf("TransmitTime fallback mismatch:\nwant:\n%q\ngot:\n%q", expected, got)
		}
	})

	t.Run("timestamp fallback to IncomingTime when others are zero", func(t *testing.T) {
		msg := Message{
			MsgType:      MsgTypeAppNotification,
			AppName:      "微信",
			Title:        "来电提醒",
			IncomingTime: 1700000000,
		}
		expected := "【微信】来电提醒\n\n通知时间：2023-11-15 06:13:20\n"
		if got := msg.GenComplexText(cst); got != expected {
			t.Fatalf("IncomingTime fallback mismatch:\nwant:\n%q\ngot:\n%q", expected, got)
		}
	})
}

func TestMessage_NilReceiverAndWhitespaceHygiene(t *testing.T) {
	cst := time.FixedZone("Asia/Shanghai", 8*3600)

	t.Run("nil Message receiver does not panic on SetCaptcha, SetEmergency, or GenComplexText", func(t *testing.T) {
		var nilMsg *Message
		nilMsg.SetCaptcha("123456")
		nilMsg.SetEmergency(true)
		if got := nilMsg.GenComplexText(cst); got != "" {
			t.Fatalf("expected empty string from nilMsg.GenComplexText(), got %q", got)
		}
	})

	t.Run("text and call trim surrounding whitespace in Title and Content", func(t *testing.T) {
		callMsg := Message{
			MsgType:      MsgTypeCall,
			Title:        "  13800138000 \n",
			IncomingTime: 1700000000,
		}
		wantCall := "未接来电：13800138000\n\n来电时间：2023-11-15 06:13:20\n"
		if got := callMsg.GenComplexText(cst); got != wantCall {
			t.Fatalf("call whitespace trim mismatch:\nwant: %q\ngot:  %q", wantCall, got)
		}

		textMsg := Message{
			MsgType:      MsgTypeText,
			Title:        " 10086 ",
			Content:      " 余额提醒 \n\n",
			TransmitTime: 1700000000,
			ReceiveTime:  1700000010,
		}
		wantText := "余额提醒\n\n短信来自: 10086\n\n发送时间：2023-11-15 06:13:20\n转发时间：2023-11-15 06:13:30\n"
		if got := textMsg.GenComplexText(cst); got != wantText {
			t.Fatalf("text whitespace trim mismatch:\nwant: %q\ngot:  %q", wantText, got)
		}
	})

	t.Run("default branch trims whitespace and avoids consecutive blank lines", func(t *testing.T) {
		wsOnly := Message{
			Title:   "   ",
			Content: " \t\n ",
		}
		if got := wsOnly.GenComplexText(cst); got != "" {
			t.Fatalf("expected empty string for whitespace-only default message, got %q", got)
		}

		titleOnly := Message{
			Title:   " Notice ",
			Content: "   ",
		}
		if got := titleOnly.GenComplexText(cst); got != "Notice\n" {
			t.Fatalf("expected 'Notice\\n' for title-only default message, got %q", got)
		}

		contentOnly := Message{
			Title:   "  ",
			Content: " Body only ",
		}
		if got := contentOnly.GenComplexText(cst); got != "Body only\n" {
			t.Fatalf("expected 'Body only\\n' for content-only default message, got %q", got)
		}
	})
}

func TestMessage_Clone_AppNotification(t *testing.T) {
	batt := 75
	orig := &Message{
		MsgType:      MsgTypeAppNotification,
		AppName:      "微信",
		Title:        "通知标题",
		Content:      "通知内容",
		BatteryLevel: &batt,
		Timestamp:    1700000000,
		Emergency:    true,
		CaptchaCode:  "456789",
	}

	cloned := orig.Clone()
	if cloned == nil || cloned == orig {
		t.Fatalf("expected distinct non-nil pointer from Clone()")
	}
	if cloned.AppName != "微信" {
		t.Fatalf("expected cloned.AppName == '微信', got %q", cloned.AppName)
	}
	if cloned.MsgType != MsgTypeAppNotification {
		t.Fatalf("expected cloned.MsgType == MsgTypeAppNotification, got %q", cloned.MsgType)
	}

	cloned.AppName = "钉钉"
	if orig.AppName != "微信" {
		t.Fatalf("mutating cloned.AppName affected orig.AppName: got %q", orig.AppName)
	}
}
