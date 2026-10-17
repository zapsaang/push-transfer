// Package model defines core domain entities and data transfer objects for push-transfer.
package model

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// FlexibleTimestamp represents a Unix timestamp in seconds with defensive parsing
// for null, string numbers, 0, negative values, and 13-digit millisecond timestamps.
type FlexibleTimestamp int64

// UnmarshalJSON unmarshals a JSON value into FlexibleTimestamp with fault-tolerant heuristics.
func (ft *FlexibleTimestamp) UnmarshalJSON(b []byte) error {
	str := strings.TrimSpace(string(b))
	if str == "null" || str == `""` || str == "" {
		*ft = 0
		return nil
	}

	// Unquote if it's a JSON string
	if len(str) >= 2 && str[0] == '"' && str[len(str)-1] == '"' {
		str = strings.TrimSpace(str[1 : len(str)-1])
		if str == "" || str == "null" {
			*ft = 0
			return nil
		}
	}

	raw, err := strconv.ParseInt(str, 10, 64)
	if err != nil {
		// Non-numeric format degrades gracefully to 0 per system specification
		*ft = 0
		return nil //nolint:nilerr // graceful degradation for malformed client timestamps
	}

	// 13-digit millisecond timestamp converted to seconds (> 1e11)
	if raw > 1e11 || raw < -1e11 {
		raw /= 1000
	}

	*ft = FlexibleTimestamp(raw)
	return nil
}

// Format formats the timestamp according to standard layout "2006-01-02 15:04:05".
// Returns "未知时间" for values <= 0. If loc is nil, time.Local is used.
func (ft FlexibleTimestamp) Format(loc *time.Location) string {
	if ft <= 0 {
		return "未知时间"
	}
	if loc == nil {
		loc = time.Local
	}
	return time.Unix(int64(ft), 0).In(loc).Format("2006-01-02 15:04:05")
}

// MsgType represents the strongly-typed message category.
type MsgType string

// Supported standard message types across push-transfer.
const (
	MsgTypeText            MsgType = "text"
	MsgTypeCall            MsgType = "call"
	MsgTypeAppNotification MsgType = "app_notification"
)

// Valid reports whether m is one of the supported message types.
func (m MsgType) Valid() bool {
	return m == MsgTypeText || m == MsgTypeCall || m == MsgTypeAppNotification
}

// Message represents the standard data transfer object across push-transfer.
type Message struct {
	MsgType      MsgType           `json:"msg_type"`
	AppName      string            `json:"app_name,omitempty"`
	Title        string            `json:"title"`
	Content      string            `json:"content"`
	BatteryLevel *int              `json:"battery_level,omitempty"`
	Timestamp    FlexibleTimestamp `json:"timestamp,omitempty"`
	SendTime     FlexibleTimestamp `json:"send_time,omitempty"`
	ReceiveTime  FlexibleTimestamp `json:"receive_time,omitempty"`
	TransmitTime FlexibleTimestamp `json:"transmit_time,omitempty"`
	IncomingTime FlexibleTimestamp `json:"incoming_time,omitempty"`
	Emergency    bool              `json:"emergency,omitempty"`
	CaptchaCode  string            `json:"captcha_code,omitempty"`
}

// SetCaptcha sets the extracted captcha code.
func (m *Message) SetCaptcha(captchaCode string) {
	if m == nil {
		return
	}
	m.CaptchaCode = captchaCode
}

// SetEmergency sets whether this message is an emergency alert.
func (m *Message) SetEmergency(emergency bool) {
	if m == nil {
		return
	}
	m.Emergency = emergency
}

// Clone returns a deep copy of Message so concurrent delivery targets cannot mutate shared state.
func (m *Message) Clone() *Message {
	if m == nil {
		return nil
	}
	cloned := *m
	if m.BatteryLevel != nil {
		batt := *m.BatteryLevel
		cloned.BatteryLevel = &batt
	}
	return &cloned
}

func firstValidTimestamp(candidates ...FlexibleTimestamp) FlexibleTimestamp {
	for _, ts := range candidates {
		if ts > 0 {
			return ts
		}
	}
	return 0
}

// GenComplexText generates human-readable multi-channel text across supported message types.
func (m *Message) GenComplexText(loc ...*time.Location) string {
	if m == nil {
		return ""
	}

	var l *time.Location
	if len(loc) > 0 && loc[0] != nil {
		l = loc[0]
	}

	var text string
	switch m.MsgType {
	case MsgTypeCall:
		incomingTime := firstValidTimestamp(m.IncomingTime, m.Timestamp, m.SendTime, m.ReceiveTime, m.TransmitTime)
		text = fmt.Sprintf("未接来电：%s\n\n来电时间：%s\n", strings.TrimSpace(m.Title), incomingTime.Format(l))
	case MsgTypeText:
		sendTime := firstValidTimestamp(m.TransmitTime, m.SendTime, m.Timestamp)
		receiveTime := firstValidTimestamp(m.ReceiveTime, m.Timestamp)
		text = fmt.Sprintf("%s\n\n短信来自: %s\n\n发送时间：%s\n转发时间：%s\n",
			strings.TrimSpace(m.Content), strings.TrimSpace(m.Title), sendTime.Format(l), receiveTime.Format(l))
	case MsgTypeAppNotification:
		eventTime := firstValidTimestamp(m.Timestamp, m.ReceiveTime, m.SendTime, m.TransmitTime, m.IncomingTime)
		appName := strings.TrimSpace(m.AppName)
		if appName == "" {
			appName = "应用通知"
		}
		title := strings.TrimSpace(m.Title)
		header := fmt.Sprintf("【%s】", appName)
		if title != "" {
			header = fmt.Sprintf("【%s】%s", appName, title)
		}
		content := strings.TrimSpace(m.Content)
		if content != "" {
			text = fmt.Sprintf("%s\n\n%s\n\n通知时间：%s\n", header, content, eventTime.Format(l))
		} else {
			text = fmt.Sprintf("%s\n\n通知时间：%s\n", header, eventTime.Format(l))
		}
	default:
		title := strings.TrimSpace(m.Title)
		content := strings.TrimSpace(m.Content)
		switch {
		case title != "" && content != "":
			text = fmt.Sprintf("%s\n\n%s\n", title, content)
		case title != "":
			text = fmt.Sprintf("%s\n", title)
		case content != "":
			text = fmt.Sprintf("%s\n", content)
		}
	}

	if m.BatteryLevel != nil {
		text += fmt.Sprintf("\n当前电量: %d%%\n", *m.BatteryLevel)
	}

	return text
}
