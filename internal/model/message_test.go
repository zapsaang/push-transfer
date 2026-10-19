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
		{"float seconds unquoted", `{"time": 1700000000.5}`, 1700000000},
		{"float seconds quoted", `{"time": "1700000000.5"}`, 1700000000},
		{"float milliseconds quoted", `{"time": "1700000000123.4"}`, 1700000000},
		{"extreme float overflow unquoted", `{"time": 1e25}`, 0},
		{"extreme float overflow quoted", `{"time": "1e25"}`, 0},
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

func TestFlexibleTimestamp_ISO8601(t *testing.T) {
	// Expected Unix timestamp: 2026-10-18T00:15:20+08:00 = 1792253720
	const expected FlexibleTimestamp = 1792253720

	testCases := []struct {
		name  string
		input string
	}{
		{"RFC3339 standard", `{"time": "2026-10-18T00:15:20+08:00"}`},
		{"UTC Z", `{"time": "2026-10-17T16:15:20Z"}`},
		{"dot fractional seconds", `{"time": "2026-10-18T00:15:20.123456+08:00"}`},
		{"comma fractional seconds", `{"time": "2026-10-18T00:15:20,123+08:00"}`},
		{"compact offset", `{"time": "2026-10-18T00:15:20+0800"}`},
		{"compact hour only offset", `{"time": "2026-10-18T00:15:20+08"}`},
		{"space separator with colon zone", `{"time": "2026-10-18 00:15:20+08:00"}`},
		{"space separator with compact zone", `{"time": "2026-10-18 00:15:20+0800"}`},
		{"space separator with hour only zone", `{"time": "2026-10-18 00:15:20+08"}`},
		{"space before zone", `{"time": "2026-10-18 00:15:20 +08:00"}`},
		{"space before compact zone", `{"time": "2026-10-18 00:15:20 +0800"}`},
		{"space before hour only zone", `{"time": "2026-10-18 00:15:20 +08"}`},
		{"JSON unicode escape", `{"time": "2026-10-18T00:15:20\u002b08:00"}`},
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
			if wrapper.Time != expected {
				t.Fatalf("expected timestamp %d, got %d for input %s", expected, wrapper.Time, tc.input)
			}
		})
	}
}

func TestFlexibleTimestamp_ISO8601_LocalTimezone(t *testing.T) {
	cst := time.FixedZone("Asia/Shanghai", 8*3600)
	utc := time.UTC

	testCases := []struct {
		name     string
		input    string
		loc      *time.Location
		expected int64
	}{
		{
			name:     "CST space separated",
			input:    `"2026-10-18 00:15:20"`,
			loc:      cst,
			expected: time.Date(2026, 10, 18, 0, 15, 20, 0, cst).Unix(),
		},
		{
			name:     "UTC space separated",
			input:    `"2026-10-18 00:15:20"`,
			loc:      utc,
			expected: time.Date(2026, 10, 18, 0, 15, 20, 0, utc).Unix(),
		},
		{
			name:     "CST T separated",
			input:    `"2026-10-18T00:15:20"`,
			loc:      cst,
			expected: time.Date(2026, 10, 18, 0, 15, 20, 0, cst).Unix(),
		},
		{
			name:     "CST comma fractional",
			input:    `"2026-10-18 00:15:20,500"`,
			loc:      cst,
			expected: time.Date(2026, 10, 18, 0, 15, 20, 0, cst).Unix(),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ts, err := parseFlexibleTimestamp([]byte(tc.input), tc.loc)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if int64(ts) != tc.expected {
				t.Fatalf("expected %d, got %d", tc.expected, ts)
			}
		})
	}
}

func TestFlexibleTimestamp_RFC5322(t *testing.T) {
	cst := time.FixedZone("Asia/Shanghai", 8*3600)
	expectedCST := time.Date(2026, 10, 18, 0, 15, 20, 0, cst).Unix()

	testCases := []struct {
		name     string
		input    string
		expected int64
	}{
		{
			name:     "standard RFC5322",
			input:    `{"time": "Sun, 18 Oct 2026 00:15:20 +0800"}`,
			expected: expectedCST,
		},
		{
			name:     "single digit day",
			input:    `{"time": "Sun, 4 Oct 2026 00:15:20 +0800"}`,
			expected: time.Date(2026, 10, 4, 0, 15, 20, 0, cst).Unix(),
		},
		{
			name:     "without weekday",
			input:    `{"time": "18 Oct 2026 00:15:20 +0800"}`,
			expected: expectedCST,
		},
		{
			name:     "without seconds",
			input:    `{"time": "18 Oct 2026 00:15 +0800"}`,
			expected: time.Date(2026, 10, 18, 0, 15, 0, 0, cst).Unix(),
		},
		{
			name:     "timezone comment",
			input:    `{"time": "Sun, 18 Oct 2026 00:15:20 +0800 (CST)"}`,
			expected: expectedCST,
		},
		{
			name:     "2-digit year (obs-year)",
			input:    `{"time": "18 Oct 26 00:15:20 +0800"}`,
			expected: expectedCST,
		},
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
			if int64(wrapper.Time) != tc.expected {
				t.Fatalf("expected timestamp %d, got %d for input %s", tc.expected, wrapper.Time, tc.input)
			}
		})
	}
}

func TestFlexibleTimestamp_InvalidFallbacks(t *testing.T) {
	testCases := []struct {
		name  string
		input string
	}{
		{"invalid month and day", `{"time": "2023-13-45T99:99:99Z"}`},
		{"garbage string", `{"time": "not-a-date"}`},
		{"date only", `{"time": "2023-11-15"}`},
		{"empty string", `{"time": ""}`},
		{"string null", `{"time": "null"}`},
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
			if wrapper.Time != 0 {
				t.Fatalf("expected timestamp 0 for invalid input %s, got %d", tc.input, wrapper.Time)
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

func TestMessage_BatteryLevel_FlexibleUnmarshal(t *testing.T) {
	intPtr := func(v int) *int { return &v }

	testCases := []struct {
		name     string
		input    string
		expected *int
	}{
		{"omitted", `{"msg_type":"text","title":"test","content":"hi"}`, nil},
		{"explicit null", `{"msg_type":"text","title":"test","content":"hi","battery_level":null}`, nil},
		{"integer 85", `{"msg_type":"text","title":"test","content":"hi","battery_level":85}`, intPtr(85)},
		{"integer 0", `{"msg_type":"text","title":"test","content":"hi","battery_level":0}`, intPtr(0)},
		{"string 85", `{"msg_type":"text","title":"test","content":"hi","battery_level":"85"}`, intPtr(85)},
		{"string 85 percent", `{"msg_type":"text","title":"test","content":"hi","battery_level":"85%"}`, intPtr(85)},
		{"string with spaces and percent", `{"msg_type":"text","title":"test","content":"hi","battery_level":" 85 % "}`, intPtr(85)},
		{"float string 85.0", `{"msg_type":"text","title":"test","content":"hi","battery_level":"85.0"}`, intPtr(85)},
		{"float string with percent 85.0%", `{"msg_type":"text","title":"test","content":"hi","battery_level":"85.0%"}`, intPtr(85)},
		{"empty string", `{"msg_type":"text","title":"test","content":"hi","battery_level":""}`, nil},
		{"whitespace string", `{"msg_type":"text","title":"test","content":"hi","battery_level":"   "}`, nil},
		{"string null", `{"msg_type":"text","title":"test","content":"hi","battery_level":"null"}`, nil},
		{"invalid string", `{"msg_type":"text","title":"test","content":"hi","battery_level":"unknown"}`, nil},
		{"string NaN", `{"msg_type":"text","title":"test","content":"hi","battery_level":"NaN"}`, nil},
		{"string Infinity", `{"msg_type":"text","title":"test","content":"hi","battery_level":"Infinity"}`, nil},
		{"string +Inf", `{"msg_type":"text","title":"test","content":"hi","battery_level":"+Inf"}`, nil},
		{"string -Inf", `{"msg_type":"text","title":"test","content":"hi","battery_level":"-Inf"}`, nil},
		{"negative integer -1", `{"msg_type":"text","title":"test","content":"hi","battery_level":-1}`, nil},
		{"negative string -1", `{"msg_type":"text","title":"test","content":"hi","battery_level":"-1"}`, nil},
		{"integer over 100", `{"msg_type":"text","title":"test","content":"hi","battery_level":105}`, nil},
		{"string over 100", `{"msg_type":"text","title":"test","content":"hi","battery_level":"120%"}`, nil},
		{"boundary 100", `{"msg_type":"text","title":"test","content":"hi","battery_level":100}`, intPtr(100)},
		{"float rounded 85.6%", `{"msg_type":"text","title":"test","content":"hi","battery_level":"85.6%"}`, intPtr(86)},
		{"float rounded 85.4%", `{"msg_type":"text","title":"test","content":"hi","battery_level":"85.4%"}`, intPtr(85)},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var msg Message
			err := json.Unmarshal([]byte(tc.input), &msg)
			if err != nil {
				t.Fatalf("unexpected unmarshal error: %v", err)
			}
			if tc.expected == nil {
				if msg.BatteryLevel != nil {
					t.Fatalf("expected nil BatteryLevel for %s, got %d", tc.input, *msg.BatteryLevel)
				}
			} else {
				if msg.BatteryLevel == nil {
					t.Fatalf("expected BatteryLevel %d for %s, got nil", *tc.expected, tc.input)
				}
				if *msg.BatteryLevel != *tc.expected {
					t.Fatalf("expected BatteryLevel %d for %s, got %d", *tc.expected, tc.input, *msg.BatteryLevel)
				}
			}
		})
	}
}

func TestMessage_TimeSensitive_FlexibleUnmarshal(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected bool
	}{
		{"omitted", `{"msg_type":"text","title":"test","content":"hi"}`, false},
		{"explicit null", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":null}`, false},
		{"boolean true", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":true}`, true},
		{"boolean false", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":false}`, false},
		{"string true", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":"true"}`, true},
		{"string false", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":"false"}`, false},
		{"string True mixed case", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":"True"}`, true},
		{"string FALSE mixed case", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":"FALSE"}`, false},
		{"string with spaces true", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":" true "}`, true},
		{"string with spaces false", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":" false "}`, false},
		{"integer 1", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":1}`, true},
		{"integer 0", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":0}`, false},
		{"string 1", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":"1"}`, true},
		{"string 0", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":"0"}`, false},
		{"string with spaces 1", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":" 1 "}`, true},
		{"string with spaces 0", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":" 0 "}`, false},
		{"float 1.0", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":1.0}`, true},
		{"float 0.0", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":0.0}`, false},
		{"float string 1.0", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":"1.0"}`, true},
		{"float string 0.0", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":"0.0"}`, false},
		{"empty string", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":""}`, false},
		{"whitespace string", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":"   "}`, false},
		{"string null", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":"null"}`, false},
		{"invalid string", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":"invalid"}`, false},
		{"other integer 2", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":2}`, false},
		{"negative integer -1", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":-1}`, false},
		{"other string 2", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":"2"}`, false},
		{"scientific notation 1e0", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":1e0}`, true},
		{"scientific notation 0e0", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":0e0}`, false},
		{"scientific string 1e0", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":"1e0"}`, true},
		{"scientific string 0e0", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":"0e0"}`, false},
		{"json array fallback", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":[1,2]}`, false},
		{"json object fallback", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":{"a":1}}`, false},
		{"string null with spaces", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":" null "}`, false},
		{"raw token with spaces 1", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":  1  }`, true},
		{"raw token with spaces 0", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":  0  }`, false},
		{"raw token with spaces true", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":  true  }`, true},
		{"raw token with spaces false", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":  false  }`, false},
		{"chinese shi 是", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":"是"}`, true},
		{"chinese fou 否", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":"否"}`, false},
		{"chinese zhen 真", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":"真"}`, true},
		{"chinese jia 假", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":"假"}`, false},
		{"chinese dui 对", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":"对"}`, true},
		{"chinese cuo 错", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":"错"}`, false},
		{"english yes", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":"yes"}`, true},
		{"english no", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":"no"}`, false},
		{"english YES uppercase", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":"YES"}`, true},
		{"english NO uppercase", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":"NO"}`, false},
		{"english y", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":"y"}`, true},
		{"english n", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":"n"}`, false},
		{"english on", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":"on"}`, true},
		{"english off", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":"off"}`, false},
		{"chinese with spaces 是", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":" 是 "}`, true},
		{"chinese with spaces 否", `{"msg_type":"text","title":"test","content":"hi","time_sensitive":" 否 "}`, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var msg Message
			err := json.Unmarshal([]byte(tc.input), &msg)
			if err != nil {
				t.Fatalf("unexpected unmarshal error: %v", err)
			}
			if msg.TimeSensitive != tc.expected {
				t.Fatalf("expected TimeSensitive %v for input %s, got %v", tc.expected, tc.input, msg.TimeSensitive)
			}
		})
	}
}

func TestMessage_TimeSensitive_MarshalRoundTrip(t *testing.T) {
	// 1. True round-trip
	msg1 := &Message{
		MsgType:       MsgTypeText,
		Title:         "Notification",
		Content:       "Hello",
		TimeSensitive: true,
	}
	data1, err := json.Marshal(msg1)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	if !strings.Contains(string(data1), `"time_sensitive":true`) {
		t.Errorf("expected marshaled json to contain '\"time_sensitive\":true', got: %s", string(data1))
	}
	var round1 Message
	if err := json.Unmarshal(data1, &round1); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if !round1.TimeSensitive {
		t.Errorf("expected round1.TimeSensitive to be true, got false")
	}

	// 2. False round-trip (omitempty should omit the field)
	msg2 := &Message{
		MsgType:       MsgTypeText,
		Title:         "Notification",
		Content:       "Hello",
		TimeSensitive: false,
	}
	data2, err := json.Marshal(msg2)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	if strings.Contains(string(data2), "time_sensitive") {
		t.Errorf("expected marshaled json to omit 'time_sensitive' when false, got: %s", string(data2))
	}
	var round2 Message
	if err := json.Unmarshal(data2, &round2); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if round2.TimeSensitive {
		t.Errorf("expected round2.TimeSensitive to be false, got true")
	}
}

func TestMessage_UnmarshalJSON_AllFieldsPreservedWhenOmitted(t *testing.T) {
	intPtr := func(v int) *int { return &v }
	original := &Message{
		MsgType:       MsgTypeAppNotification,
		AppName:       "OldApp",
		Title:         "OldTitle",
		Content:       "OldContent",
		Hostname:      "OldHost",
		BatteryLevel:  intPtr(77),
		Timestamp:     1700000001,
		SendTime:      1700000002,
		ReceiveTime:   1700000003,
		TransmitTime:  1700000004,
		IncomingTime:  1700000005,
		TimeSensitive: true,
		Emergency:     true,
		CaptchaCode:   "888999",
	}

	// Only update content
	err := json.Unmarshal([]byte(`{"content":"UpdatedContentOnly"}`), original)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 1. Updated field
	if original.Content != "UpdatedContentOnly" {
		t.Errorf("expected Content to be updated to 'UpdatedContentOnly', got %q", original.Content)
	}

	// 2. All other 13 fields MUST be strictly preserved
	if original.MsgType != MsgTypeAppNotification {
		t.Errorf("expected MsgType preserved as 'app_notification', got %q", original.MsgType)
	}
	if original.AppName != "OldApp" {
		t.Errorf("expected AppName preserved as 'OldApp', got %q", original.AppName)
	}
	if original.Title != "OldTitle" {
		t.Errorf("expected Title preserved as 'OldTitle', got %q", original.Title)
	}
	if original.Hostname != "OldHost" {
		t.Errorf("expected Hostname preserved as 'OldHost', got %q", original.Hostname)
	}
	if original.BatteryLevel == nil || *original.BatteryLevel != 77 {
		t.Errorf("expected BatteryLevel preserved as 77, got %v", original.BatteryLevel)
	}
	if original.Timestamp != 1700000001 {
		t.Errorf("expected Timestamp preserved as 1700000001, got %d", original.Timestamp)
	}
	if original.SendTime != 1700000002 {
		t.Errorf("expected SendTime preserved as 1700000002, got %d", original.SendTime)
	}
	if original.ReceiveTime != 1700000003 {
		t.Errorf("expected ReceiveTime preserved as 1700000003, got %d", original.ReceiveTime)
	}
	if original.TransmitTime != 1700000004 {
		t.Errorf("expected TransmitTime preserved as 1700000004, got %d", original.TransmitTime)
	}
	if original.IncomingTime != 1700000005 {
		t.Errorf("expected IncomingTime preserved as 1700000005, got %d", original.IncomingTime)
	}
	if !original.TimeSensitive {
		t.Errorf("expected TimeSensitive preserved as true, got false")
	}
	if !original.Emergency {
		t.Errorf("expected Emergency preserved as true, got false")
	}
	if original.CaptchaCode != "888999" {
		t.Errorf("expected CaptchaCode preserved as '888999', got %q", original.CaptchaCode)
	}
}

func TestMessage_UnmarshalJSON_ExplicitOverwrites(t *testing.T) {
	intPtr := func(v int) *int { return &v }

	// 1. TimeSensitive overwrite from true to false with all falsy representations
	falsyVariants := []string{
		`{"time_sensitive": false}`,
		`{"time_sensitive": 0}`,
		`{"time_sensitive": "false"}`,
		`{"time_sensitive": "0"}`,
		`{"time_sensitive": null}`,
	}
	for _, payload := range falsyVariants {
		msg := &Message{TimeSensitive: true}
		if err := json.Unmarshal([]byte(payload), msg); err != nil {
			t.Fatalf("unexpected unmarshal error for %s: %v", payload, err)
		}
		if msg.TimeSensitive {
			t.Errorf("expected TimeSensitive to be updated to false for payload %s, got true", payload)
		}
	}

	// 2. TimeSensitive overwrite from false to true with truthy representations
	truthyVariants := []string{
		`{"time_sensitive": true}`,
		`{"time_sensitive": 1}`,
		`{"time_sensitive": "true"}`,
		`{"time_sensitive": "1"}`,
	}
	for _, payload := range truthyVariants {
		msg := &Message{TimeSensitive: false}
		if err := json.Unmarshal([]byte(payload), msg); err != nil {
			t.Fatalf("unexpected unmarshal error for %s: %v", payload, err)
		}
		if !msg.TimeSensitive {
			t.Errorf("expected TimeSensitive to be updated to true for payload %s, got false", payload)
		}
	}

	// 3. BatteryLevel explicit overwrite to null / 0 / percentage
	msgBatt := &Message{BatteryLevel: intPtr(50)}
	if err := json.Unmarshal([]byte(`{"battery_level": null}`), msgBatt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msgBatt.BatteryLevel != nil {
		t.Errorf("expected BatteryLevel to be nil after explicit null, got %v", *msgBatt.BatteryLevel)
	}

	msgBatt2 := &Message{BatteryLevel: intPtr(50)}
	if err := json.Unmarshal([]byte(`{"battery_level": 0}`), msgBatt2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msgBatt2.BatteryLevel == nil || *msgBatt2.BatteryLevel != 0 {
		t.Errorf("expected BatteryLevel to be 0, got %v", msgBatt2.BatteryLevel)
	}

	msgBatt3 := &Message{BatteryLevel: intPtr(50)}
	if err := json.Unmarshal([]byte(`{"battery_level": "85%"}`), msgBatt3); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msgBatt3.BatteryLevel == nil || *msgBatt3.BatteryLevel != 85 {
		t.Errorf("expected BatteryLevel to be 85, got %v", msgBatt3.BatteryLevel)
	}

	// 4. Pointer deep copy isolation for preserved BatteryLevel
	sourceLevel := 60
	msgOriginal := &Message{BatteryLevel: &sourceLevel}
	if err := json.Unmarshal([]byte(`{"title": "isolated"}`), msgOriginal); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Mutating sourceLevel should not mutate msgOriginal.BatteryLevel if deeply isolated
	sourceLevel = 99
	if *msgOriginal.BatteryLevel != 60 {
		t.Errorf("expected preserved BatteryLevel to be isolated from external pointer mutation, got %d", *msgOriginal.BatteryLevel)
	}
}

func TestMessage_TimeSensitiveAndHostname_UnmarshalAndClone(t *testing.T) {
	// 1. UnmarshalJSON with hostname and time_sensitive
	payload := `{
		"msg_type": "text",
		"title": "Bank",
		"content": "Code 1234",
		"hostname": "Pixel-8",
		"time_sensitive": true,
		"battery_level": 90
	}`

	var msg Message
	if err := json.Unmarshal([]byte(payload), &msg); err != nil {
		t.Fatalf("UnmarshalJSON failed: %v", err)
	}
	if msg.Hostname != "Pixel-8" {
		t.Errorf("expected Hostname 'Pixel-8', got %q", msg.Hostname)
	}
	if !msg.TimeSensitive {
		t.Errorf("expected TimeSensitive true, got false")
	}

	// 2. SetTimeSensitive method including nil receiver protection
	var nilMsg *Message
	nilMsg.SetTimeSensitive(true) // should not panic

	msg.SetTimeSensitive(false)
	if msg.TimeSensitive {
		t.Errorf("expected TimeSensitive false after SetTimeSensitive(false)")
	}
	msg.SetTimeSensitive(true)
	if !msg.TimeSensitive {
		t.Errorf("expected TimeSensitive true after SetTimeSensitive(true)")
	}

	// 3. Clone deep copy isolation
	if nilMsg.Clone() != nil {
		t.Errorf("expected nil for nilMsg.Clone()")
	}

	cloned := msg.Clone()
	if cloned == nil {
		t.Fatalf("expected cloned to be non-nil")
	}
	if cloned.Hostname != msg.Hostname || cloned.TimeSensitive != msg.TimeSensitive {
		t.Errorf("cloned fields do not match original")
	}

	// Mutate clone and ensure original is not affected
	cloned.Hostname = "iPhone-16"
	cloned.TimeSensitive = false
	newBatt := 50
	cloned.BatteryLevel = &newBatt

	if msg.Hostname != "Pixel-8" {
		t.Errorf("original Hostname mutated: %q", msg.Hostname)
	}
	if !msg.TimeSensitive {
		t.Errorf("original TimeSensitive mutated to false")
	}
	if *msg.BatteryLevel != 90 {
		t.Errorf("original BatteryLevel mutated: %d", *msg.BatteryLevel)
	}
}

func TestMessage_GenComplexText_HostnameAndBatteryMatrix(t *testing.T) {
	cst := time.FixedZone("Asia/Shanghai", 8*3600)
	batt := 85

	// Four states for text message
	// State 1: Neither hostname nor battery_level
	msg1 := Message{
		MsgType:      MsgTypeText,
		Title:        "10086",
		Content:      "Notice",
		TransmitTime: 1700000000,
		ReceiveTime:  1700000010,
	}
	expected1 := "Notice\n\n短信来自: 10086\n\n发送时间：2023-11-15 06:13:20\n转发时间：2023-11-15 06:13:30\n"
	if got := msg1.GenComplexText(cst); got != expected1 {
		t.Fatalf("State 1 mismatch:\nwant:\n%q\ngot:\n%q", expected1, got)
	}

	// State 2: battery_level only (100% byte-for-byte backward compatible)
	msg2 := Message{
		MsgType:      MsgTypeText,
		Title:        "10086",
		Content:      "Notice",
		BatteryLevel: &batt,
		TransmitTime: 1700000000,
		ReceiveTime:  1700000010,
	}
	expected2 := "Notice\n\n短信来自: 10086\n\n发送时间：2023-11-15 06:13:20\n转发时间：2023-11-15 06:13:30\n\n当前电量: 85%\n"
	if got := msg2.GenComplexText(cst); got != expected2 {
		t.Fatalf("State 2 mismatch:\nwant:\n%q\ngot:\n%q", expected2, got)
	}

	// State 3: hostname only
	msg3 := Message{
		MsgType:      MsgTypeText,
		Title:        "10086",
		Content:      "Notice",
		Hostname:     "Pixel-8",
		TransmitTime: 1700000000,
		ReceiveTime:  1700000010,
	}
	expected3 := "Notice\n\n短信来自: 10086\n\n发送时间：2023-11-15 06:13:20\n转发时间：2023-11-15 06:13:30\n\n设备主机名: Pixel-8\n"
	if got := msg3.GenComplexText(cst); got != expected3 {
		t.Fatalf("State 3 mismatch:\nwant:\n%q\ngot:\n%q", expected3, got)
	}

	// State 4: both hostname and battery_level
	msg4 := Message{
		MsgType:      MsgTypeText,
		Title:        "10086",
		Content:      "Notice",
		Hostname:     "Pixel-8",
		BatteryLevel: &batt,
		TransmitTime: 1700000000,
		ReceiveTime:  1700000010,
	}
	expected4 := "Notice\n\n短信来自: 10086\n\n发送时间：2023-11-15 06:13:20\n转发时间：2023-11-15 06:13:30\n\n设备主机名: Pixel-8\n当前电量: 85%\n"
	if got := msg4.GenComplexText(cst); got != expected4 {
		t.Fatalf("State 4 mismatch:\nwant:\n%q\ngot:\n%q", expected4, got)
	}

	// Sanitization of newline/tab characters in hostname (anti-injection)
	msgDirty := Message{
		MsgType:      MsgTypeText,
		Title:        "10086",
		Content:      "Notice",
		Hostname:     " Pixel-8\r\nFake-Header: evil\tphone  ",
		BatteryLevel: &batt,
		TransmitTime: 1700000000,
		ReceiveTime:  1700000010,
	}
	expectedDirty := "Notice\n\n短信来自: 10086\n\n发送时间：2023-11-15 06:13:20\n转发时间：2023-11-15 06:13:30\n\n设备主机名: Pixel-8 Fake-Header: evil phone\n当前电量: 85%\n"
	if got := msgDirty.GenComplexText(cst); got != expectedDirty {
		t.Fatalf("Sanitization mismatch:\nwant:\n%q\ngot:\n%q", expectedDirty, got)
	}

	// Whitespace-only hostname should be suppressed
	msgWhitespace := Message{
		MsgType:      MsgTypeText,
		Title:        "10086",
		Content:      "Notice",
		Hostname:     "  \r\n\t  ",
		BatteryLevel: &batt,
		TransmitTime: 1700000000,
		ReceiveTime:  1700000010,
	}
	if got := msgWhitespace.GenComplexText(cst); got != expected2 {
		t.Fatalf("Whitespace hostname should be ignored:\nwant:\n%q\ngot:\n%q", expected2, got)
	}

	// Empty text body (e.g. default type with empty title and content) with metadata should not have leading newline
	msgEmptyBody := Message{
		Hostname:     "Gateway-Node",
		BatteryLevel: &batt,
	}
	expectedEmptyBody := "设备主机名: Gateway-Node\n当前电量: 85%\n"
	if got := msgEmptyBody.GenComplexText(cst); got != expectedEmptyBody {
		t.Fatalf("Empty body with metadata mismatch:\nwant:\n%q\ngot:\n%q", expectedEmptyBody, got)
	}

	// Call message with hostname and battery
	msgCall := Message{
		MsgType:      MsgTypeCall,
		Title:        "13800138000",
		Hostname:     "Pixel-8",
		BatteryLevel: &batt,
		IncomingTime: 1700000000,
	}
	expectedCall := "未接来电：13800138000\n\n来电时间：2023-11-15 06:13:20\n\n设备主机名: Pixel-8\n当前电量: 85%\n"
	if got := msgCall.GenComplexText(cst); got != expectedCall {
		t.Fatalf("Call with metadata mismatch:\nwant:\n%q\ngot:\n%q", expectedCall, got)
	}

	// AppNotification message with hostname
	msgApp := Message{
		MsgType:   MsgTypeAppNotification,
		AppName:   "WeChat",
		Title:     "Friend",
		Content:   "Hello",
		Hostname:  "Pixel-8",
		Timestamp: 1700000000,
	}
	expectedApp := "【WeChat】Friend\n\nHello\n\n通知时间：2023-11-15 06:13:20\n\n设备主机名: Pixel-8\n"
	if got := msgApp.GenComplexText(cst); got != expectedApp {
		t.Fatalf("AppNotification with metadata mismatch:\nwant:\n%q\ngot:\n%q", expectedApp, got)
	}

	// AppNotification message with hostname and battery
	msgAppBoth := Message{
		MsgType:      MsgTypeAppNotification,
		AppName:      "WeChat",
		Title:        "Friend",
		Content:      "Hello",
		Hostname:     "Pixel-8",
		BatteryLevel: &batt,
		Timestamp:    1700000000,
	}
	expectedAppBoth := "【WeChat】Friend\n\nHello\n\n通知时间：2023-11-15 06:13:20\n\n设备主机名: Pixel-8\n当前电量: 85%\n"
	if got := msgAppBoth.GenComplexText(cst); got != expectedAppBoth {
		t.Fatalf("AppNotification with both metadata mismatch:\nwant:\n%q\ngot:\n%q", expectedAppBoth, got)
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

func TestMessage_UnmarshalJSON_NilReceiver(t *testing.T) {
	var m *Message
	err := m.UnmarshalJSON([]byte(`{"content":"test"}`))
	if err == nil {
		t.Fatal("expected error when unmarshaling into nil *Message receiver, got nil")
	}
}

func TestFlexibleTimestamp_UnmarshalJSON_NilReceiver(t *testing.T) {
	var ft *FlexibleTimestamp
	err := ft.UnmarshalJSON([]byte(`1700000000`))
	if err == nil {
		t.Fatal("expected error when unmarshaling into nil *FlexibleTimestamp receiver, got nil")
	}
}

func TestSanitizeSingleLine_ControlCharacters(t *testing.T) {
	input := "Host\x00\x08Name\x1b[31mEvil\x07"
	got := sanitizeSingleLine(input)
	for _, r := range got {
		if r < 32 || r == 127 {
			t.Fatalf("sanitizeSingleLine left control character %q in %q", r, got)
		}
	}
	if !strings.Contains(got, "Host") || !strings.Contains(got, "Name") {
		t.Fatalf("sanitizeSingleLine stripped valid characters: got %q", got)
	}
}

func TestSanitizeSingleLine_UnicodeLineTerminators(t *testing.T) {
	input := "Pixel-8\u2028Fake: injected\u2029More: evil\u0085NEL"
	got := sanitizeSingleLine(input)
	if strings.ContainsAny(got, "\u2028\u2029\u0085\r\n") {
		t.Fatalf("sanitizeSingleLine allowed Unicode line terminators: got %q", got)
	}
	expected := "Pixel-8 Fake: injected More: evil NEL"
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestMessage_UnmarshalJSON_EdgeCases(t *testing.T) {
	// 1. JSON null has no effect on existing struct
	msg := &Message{Title: "KeepMe", TimeSensitive: true}
	err := json.Unmarshal([]byte("null"), msg)
	if err != nil {
		t.Fatalf("unexpected error for JSON null: %v", err)
	}
	if msg.Title != "KeepMe" || !msg.TimeSensitive {
		t.Errorf("expected JSON null to have no effect on struct fields, got Title=%q, TimeSensitive=%v", msg.Title, msg.TimeSensitive)
	}

	// 2. Empty byte slice and whitespace only byte slice should return error
	var emptyMsg Message
	if err := json.Unmarshal([]byte(""), &emptyMsg); err == nil {
		t.Errorf("expected error for empty byte slice, got nil")
	}
	if err := json.Unmarshal([]byte("   "), &emptyMsg); err == nil {
		t.Errorf("expected error for whitespace only byte slice, got nil")
	}

	// 3. Unknown fields are safely ignored while known fields are parsed
	unknownMsg := &Message{Title: "InitialTitle"}
	err = json.Unmarshal([]byte(`{"unknown_custom_field": "some_value", "time_sensitive": 1}`), unknownMsg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if unknownMsg.Title != "InitialTitle" {
		t.Errorf("expected Title to remain 'InitialTitle', got %q", unknownMsg.Title)
	}
	if !unknownMsg.TimeSensitive {
		t.Errorf("expected TimeSensitive to be updated to true, got false")
	}
}

func TestMessage_AppNotification_LocalizedFieldsAndExplicitTimezone(t *testing.T) {
	cst := time.FixedZone("Asia/Shanghai", 8*3600)
	rawLogJSON := `{
		"content": "AlertBot:[Prod][High][cpu_usage]\nCPU usage exceeds 90%",
		"battery_level": "79",
		"hostname": "TestDevice01",
		"title": "Production System Alerts",
		"msg_type": "app_notification",
		"timestamp": "2026-10-18T18:36:44+08:00",
		"app_name": "AlertHub",
		"time_sensitive": "否"
	}`

	var msg Message
	if err := json.Unmarshal([]byte(rawLogJSON), &msg); err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}

	if msg.MsgType != MsgTypeAppNotification {
		t.Errorf("expected MsgType %q, got %q", MsgTypeAppNotification, msg.MsgType)
	}
	if msg.AppName != "AlertHub" {
		t.Errorf("expected AppName 'AlertHub', got %q", msg.AppName)
	}
	if msg.Title != "Production System Alerts" {
		t.Errorf("expected Title 'Production System Alerts', got %q", msg.Title)
	}
	if msg.Content != "AlertBot:[Prod][High][cpu_usage]\nCPU usage exceeds 90%" {
		t.Errorf("content mismatch, got %q", msg.Content)
	}
	if msg.BatteryLevel == nil || *msg.BatteryLevel != 79 {
		t.Errorf("expected BatteryLevel 79, got %v", msg.BatteryLevel)
	}
	if msg.Hostname != "TestDevice01" {
		t.Errorf("expected Hostname 'TestDevice01', got %q", msg.Hostname)
	}
	expectedTime := time.Date(2026, 10, 18, 18, 36, 44, 0, cst).Unix()
	if int64(msg.Timestamp) != expectedTime {
		t.Errorf("expected Timestamp %d, got %d", expectedTime, msg.Timestamp)
	}
	if msg.TimeSensitive != false {
		t.Errorf("expected TimeSensitive false for '否', got true")
	}

	// Verify formatted complex text
	complexText := msg.GenComplexText(cst)
	expectedHeader := "【AlertHub】Production System Alerts"
	if !strings.Contains(complexText, expectedHeader) {
		t.Errorf("expected complexText to contain %q, got:\n%s", expectedHeader, complexText)
	}
	if !strings.Contains(complexText, "通知时间：2026-10-18 18:36:44") {
		t.Errorf("expected complexText to contain '通知时间：2026-10-18 18:36:44', got:\n%s", complexText)
	}
	if !strings.Contains(complexText, "设备主机名: TestDevice01") {
		t.Errorf("expected complexText to contain '设备主机名: TestDevice01', got:\n%s", complexText)
	}
	if !strings.Contains(complexText, "当前电量: 79%") {
		t.Errorf("expected complexText to contain '当前电量: 79%%', got:\n%s", complexText)
	}

	// Variant: when time_sensitive is "是"
	rawLogTimeSensitive := `{
		"content": "AlertBot:[Prod][High][cpu_usage]\nCPU usage exceeds 90%",
		"battery_level": "79",
		"hostname": "TestDevice01",
		"title": "Production System Alerts",
		"msg_type": "app_notification",
		"timestamp": "2026-10-18T18:36:44+08:00",
		"app_name": "AlertHub",
		"time_sensitive": "是"
	}`
	var msgTS Message
	if err := json.Unmarshal([]byte(rawLogTimeSensitive), &msgTS); err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}
	if !msgTS.TimeSensitive {
		t.Errorf("expected TimeSensitive true for '是', got false")
	}
}

func TestMessage_Emergency_FlexibleUnmarshal(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected bool
	}{
		{"boolean true", `{"emergency": true}`, true},
		{"boolean false", `{"emergency": false}`, false},
		{"string true", `{"emergency": "true"}`, true},
		{"string false", `{"emergency": "false"}`, false},
		{"integer 1", `{"emergency": 1}`, true},
		{"integer 0", `{"emergency": 0}`, false},
		{"string 1", `{"emergency": "1"}`, true},
		{"string 0", `{"emergency": "0"}`, false},
		{"chinese shi 是", `{"emergency": "是"}`, true},
		{"chinese fou 否", `{"emergency": "否"}`, false},
		{"chinese zhen 真", `{"emergency": "真"}`, true},
		{"chinese jia 假", `{"emergency": "假"}`, false},
		{"english yes", `{"emergency": "yes"}`, true},
		{"english no", `{"emergency": "no"}`, false},
		{"null", `{"emergency": null}`, false},
		{"invalid string", `{"emergency": "invalid"}`, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var msg Message
			err := json.Unmarshal([]byte(tc.input), &msg)
			if err != nil {
				t.Fatalf("unexpected unmarshal error for %s: %v", tc.input, err)
			}
			if msg.Emergency != tc.expected {
				t.Errorf("expected Emergency %v for input %s, got %v", tc.expected, tc.input, msg.Emergency)
			}
		})
	}
}

func TestMessage_GenComplexText_InheritsInputTimezone(t *testing.T) {
	rawLogJSON := `{
		"content": "AlertBot:[Prod][High][cpu_usage]\nCPU usage exceeds 90%",
		"title": "Production System Alerts",
		"msg_type": "app_notification",
		"timestamp": "2026-10-18T18:36:44+08:00",
		"app_name": "AlertHub"
	}`

	var msg Message
	if err := json.Unmarshal([]byte(rawLogJSON), &msg); err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}

	// Calling GenComplexText() with no arguments (as in Telegram, Email, Console deliverers)
	// MUST preserve the input's explicit +08:00 timezone (18:36:44).
	if msg.TimeLocation() == nil {
		t.Fatal("expected non-nil TimeLocation from explicit ISO8601 input")
	}
	complexText := msg.GenComplexText()
	if !strings.Contains(complexText, "通知时间：2026-10-18 18:36:44") {
		t.Fatalf("expected complexText to inherit input timezone and show '18:36:44', but got:\n%s", complexText)
	}
}

func TestMessage_IdempotencyKey_Parsing(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{"normal string", `{"idempotency_key": "abc-123"}`, "abc-123"},
		{"trim whitespace", `{"idempotency_key": "  \t  key-with-spaces \n "}`, "key-with-spaces"},
		{"null value", `{"idempotency_key": null}`, ""},
		{"omitted field", `{"title": "hello"}`, ""},
		{"empty string", `{"idempotency_key": ""}`, ""},
		{"only whitespace", `{"idempotency_key": "   \n\t  "}`, ""},
		{"null character inside string", "{\"idempotency_key\": \"key\\u0000with\\u0000null\"}", "key\x00with\x00null"},
		{"multibyte characters chinese japanese arabic", `{"idempotency_key": "  订单-番号-123-日本語-عربي  "}`, "订单-番号-123-日本語-عربي"},
		{"special symbols and punctuation", `{"idempotency_key": "  !@#$%^&*()_+-=[]{}|;':,./<>?` + "`" + `~  "}`, "!@#$%^&*()_+-=[]{}|;':,./<>?`~"},
		{"emojis and symbols", `{"idempotency_key": "  🔥🔔🚀✅🎯  "}`, "🔥🔔🚀✅🎯"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var msg Message
			if err := json.Unmarshal([]byte(tc.input), &msg); err != nil {
				t.Fatalf("unexpected unmarshal error: %v", err)
			}
			if msg.IdempotencyKey != tc.expected {
				t.Errorf("expected IdempotencyKey %q, got %q", tc.expected, msg.IdempotencyKey)
			}
		})
	}
}

func TestMessage_IdempotencyKey_UnicodeTruncate(t *testing.T) {
	// Construct a string with 300 runes: 150 Chinese chars + 150 emojis
	chinesePart := strings.Repeat("中", 150)
	emojiPart := strings.Repeat("🚀", 150)
	fullStr := chinesePart + emojiPart // 300 runes

	expectedRunes := []rune(fullStr)[:256]
	expected := string(expectedRunes)

	inputJSON, err := json.Marshal(map[string]string{
		"idempotency_key": fullStr,
	})
	if err != nil {
		t.Fatalf("failed to marshal JSON: %v", err)
	}

	var msg Message
	if err := json.Unmarshal(inputJSON, &msg); err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}

	if len([]rune(msg.IdempotencyKey)) != 256 {
		t.Fatalf("expected exactly 256 runes, got %d", len([]rune(msg.IdempotencyKey)))
	}
	if msg.IdempotencyKey != expected {
		t.Fatalf("expected truncated string to match expected 256 runes")
	}

	// Boundary check: exactly 256 runes should not be truncated
	input256, err := json.Marshal(map[string]string{
		"idempotency_key": expected,
	})
	if err != nil {
		t.Fatalf("failed to marshal JSON: %v", err)
	}
	var msg256 Message
	if err := json.Unmarshal(input256, &msg256); err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}
	if msg256.IdempotencyKey != expected {
		t.Fatalf("expected exact 256 runes preserved without change")
	}

	// Mixed 1-byte, 2-byte, 3-byte, and 4-byte runes truncation
	// 'a' (1-byte), 'ж' (2-byte), '测' (3-byte), '🎉' (4-byte) = 4 runes per block
	var mixedBuilder strings.Builder
	for i := 0; i < 70; i++ { // 70 * 4 = 280 runes
		mixedBuilder.WriteString("aж测🎉")
	}
	mixedStr := mixedBuilder.String()
	expectedMixed := string([]rune(mixedStr)[:256])

	inputMixed, err := json.Marshal(map[string]string{
		"idempotency_key": mixedStr,
	})
	if err != nil {
		t.Fatalf("failed to marshal mixed JSON: %v", err)
	}
	var msgMixed Message
	if err := json.Unmarshal(inputMixed, &msgMixed); err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}
	if len([]rune(msgMixed.IdempotencyKey)) != 256 {
		t.Fatalf("expected 256 runes, got %d", len([]rune(msgMixed.IdempotencyKey)))
	}
	if msgMixed.IdempotencyKey != expectedMixed {
		t.Fatalf("expected mixed truncated string to match rune-safe slice")
	}
}

func TestMessage_Clone_PreservesIdempotencyKey(t *testing.T) {
	orig := &Message{
		Title:          "Original",
		IdempotencyKey: "unique-key-12345",
	}

	cloned := orig.Clone()
	if cloned == nil {
		t.Fatal("expected non-nil clone")
	}
	if cloned.IdempotencyKey != orig.IdempotencyKey {
		t.Fatalf("expected cloned IdempotencyKey %q, got %q", orig.IdempotencyKey, cloned.IdempotencyKey)
	}

	// Mutating cloned must not affect original
	cloned.IdempotencyKey = "mutated-key"
	if orig.IdempotencyKey != "unique-key-12345" {
		t.Fatalf("original IdempotencyKey was mutated, got %q", orig.IdempotencyKey)
	}
}

func TestTruncateRunes_BoundaryAndNegative(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		maxRunes int
		expected string
	}{
		{"negative maxRunes", "hello world", -1, ""},
		{"zero maxRunes", "hello world", 0, ""},
		{"empty string negative", "", -5, ""},
		{"empty string zero", "", 0, ""},
		{"single rune max 1", "你好世界", 1, "你"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			res := truncateRunes(tc.input, tc.maxRunes)
			if res != tc.expected {
				t.Fatalf("expected truncateRunes(%q, %d) = %q, got %q", tc.input, tc.maxRunes, tc.expected, res)
			}
		})
	}
}
