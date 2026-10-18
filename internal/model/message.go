// Package model defines core domain entities and data transfer objects for push-transfer.
package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/mail"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// FlexibleTimestamp represents a Unix timestamp in seconds with defensive parsing
// for null, string numbers, 0, negative values, and 13-digit millisecond timestamps.
type FlexibleTimestamp int64

var iso8601WithZoneLayouts = []string{
	time.RFC3339Nano,                       // "2006-01-02T15:04:05.999999999Z07:00"
	"2006-01-02T15:04:05.999999999Z0700",   // 紧凑时区偏移，如 2026-10-18T00:15:20+0800
	"2006-01-02T15:04:05.999999999Z07",     // 仅小时偏移，如 2026-10-18T00:15:20+08
	"2006-01-02 15:04:05.999999999Z07:00",  // 空格分隔 + 冒号时区
	"2006-01-02 15:04:05.999999999Z0700",   // 空格分隔 + 紧凑时区
	"2006-01-02 15:04:05.999999999Z07",     // 空格分隔 + 仅小时偏移
	"2006-01-02 15:04:05.999999999 Z07:00", // 时间与时区间带空格，如 2026-10-18 00:15:20 +08:00
	"2006-01-02 15:04:05.999999999 Z0700",  // 时间与时区间带空格，如 2026-10-18 00:15:20 +0800
	"2006-01-02 15:04:05.999999999 Z07",    // 时间与时区间带空格，如 2026-10-18 00:15:20 +08
}

var iso8601LocalLayouts = []string{
	"2006-01-02T15:04:05.999999999", // 如 2026-10-18T00:15:20 或 2026-10-18T00:15:20.123
	"2006-01-02 15:04:05.999999999", // 如 2026-10-18 00:15:20 或 2026-10-18 00:15:20.123
}

func normalizeISO8601FractionComma(s string) string {
	if len(s) >= 21 && s[4] == '-' && s[7] == '-' && (s[10] == 'T' || s[10] == ' ') &&
		s[13] == ':' && s[16] == ':' && s[19] == ',' && s[20] >= '0' && s[20] <= '9' {
		return s[:19] + "." + s[20:]
	}
	return s
}

// maxValidEpochFloat is a defensive upper bound (approx year 33658 for ms) against int64 overflow on extreme floats.
const maxValidEpochFloat = 1e15

func parseFlexibleTimestamp(b []byte, loc *time.Location) (FlexibleTimestamp, error) {
	str := strings.TrimSpace(string(b))
	if str == "null" || str == `""` || str == "" {
		return 0, nil
	}

	if len(str) >= 2 && str[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return 0, nil //nolint:nilerr // graceful degradation for malformed client timestamps
		}
		str = strings.TrimSpace(s)
		if str == "" || str == "null" {
			return 0, nil
		}

		// 1. Fast-path: integer parsing
		if raw, err := strconv.ParseInt(str, 10, 64); err == nil {
			if raw > 1e11 || raw < -1e11 {
				raw /= 1000
			}
			return FlexibleTimestamp(raw), nil
		}

		normStr := normalizeISO8601FractionComma(str)

		// 2. Try ISO 8601 with explicit timezone
		for _, layout := range iso8601WithZoneLayouts {
			if t, err := time.Parse(layout, normStr); err == nil {
				return FlexibleTimestamp(t.Unix()), nil
			}
		}

		// 3. Try ISO 8601 local time without timezone
		if loc == nil {
			loc = time.Local
		}
		for _, layout := range iso8601LocalLayouts {
			if t, err := time.ParseInLocation(layout, normStr, loc); err == nil {
				return FlexibleTimestamp(t.Unix()), nil
			}
		}

		// 4. Try RFC 5322 / RFC 2822
		if t, err := mail.ParseDate(str); err == nil {
			return FlexibleTimestamp(t.Unix()), nil
		}

		// 5. Try floating-point epoch number in string
		if f, err := strconv.ParseFloat(str, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
			if math.Abs(f) > maxValidEpochFloat {
				return 0, nil
			}
			raw := int64(f)
			if raw > 1e11 || raw < -1e11 {
				raw /= 1000
			}
			return FlexibleTimestamp(raw), nil
		}

		return 0, nil
	}

	// Unquoted number or token
	raw, err := strconv.ParseInt(str, 10, 64)
	if err != nil {
		if f, ferr := strconv.ParseFloat(str, 64); ferr == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
			if math.Abs(f) > maxValidEpochFloat {
				return 0, nil
			}
			raw = int64(f)
		} else {
			return 0, nil //nolint:nilerr // graceful degradation for malformed client timestamps
		}
	}
	if raw > 1e11 || raw < -1e11 {
		raw /= 1000
	}
	return FlexibleTimestamp(raw), nil
}

// UnmarshalJSON unmarshals a JSON value into FlexibleTimestamp with fault-tolerant heuristics.
func (ft *FlexibleTimestamp) UnmarshalJSON(b []byte) error {
	if ft == nil {
		return errors.New("model.FlexibleTimestamp: UnmarshalJSON on nil pointer")
	}
	v, err := parseFlexibleTimestamp(b, time.Local)
	if err != nil {
		return err
	}
	*ft = v
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
	MsgType       MsgType           `json:"msg_type"`
	AppName       string            `json:"app_name,omitempty"`
	Title         string            `json:"title"`
	Content       string            `json:"content"`
	Hostname      string            `json:"hostname,omitempty"`
	BatteryLevel  *int              `json:"battery_level,omitempty"`
	Timestamp     FlexibleTimestamp `json:"timestamp,omitempty"`
	SendTime      FlexibleTimestamp `json:"send_time,omitempty"`
	ReceiveTime   FlexibleTimestamp `json:"receive_time,omitempty"`
	TransmitTime  FlexibleTimestamp `json:"transmit_time,omitempty"`
	IncomingTime  FlexibleTimestamp `json:"incoming_time,omitempty"`
	TimeSensitive bool              `json:"time_sensitive,omitempty"`
	Emergency     bool              `json:"emergency,omitempty"`
	CaptchaCode   string            `json:"captcha_code,omitempty"`
}

func parseBatteryLevel(raw []byte) *int {
	str := strings.TrimSpace(string(raw))
	if str == "" || str == "null" {
		return nil
	}

	// Unquote if passed as JSON string
	if len(str) >= 2 && str[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil
		}
		str = strings.TrimSpace(s)
		if str == "" || str == "null" {
			return nil
		}
	}

	// Strip trailing percentage sign and whitespace
	str = strings.TrimSuffix(str, "%")
	str = strings.TrimSpace(str)

	// 1. Fast-path: integer parsing
	if val, err := strconv.Atoi(str); err == nil {
		if val < 0 || val > 100 {
			return nil
		}
		return &val
	}

	// 2. Fallback: float string parsing (e.g. "85.0" -> 85, "85.6%" -> 86)
	if f, err := strconv.ParseFloat(str, 64); err == nil {
		if math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f > 100 {
			return nil
		}
		val := int(math.Round(f))
		return &val
	}

	// Graceful degradation for malformed non-numeric values
	return nil
}

func parseTimeSensitive(raw []byte) bool {
	str := strings.TrimSpace(string(raw))
	if str == "" || str == "null" {
		return false
	}

	// Unquote if passed as JSON string
	if len(str) >= 2 && str[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return false
		}
		str = strings.TrimSpace(s)
		if str == "" || str == "null" {
			return false
		}
	}

	// 1. Fast-path: standard boolean parsing (supports "true", "false", "True", "False", "1", "0", "t", "f", "T", "F")
	if val, err := strconv.ParseBool(str); err == nil {
		return val
	}

	// 2. Fallback: float representation (e.g., 1.0, 1e0 or "1.0" -> true, 0.0, 0e0 or "0.0" -> false)
	if f, err := strconv.ParseFloat(str, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
		if f == 1.0 {
			return true
		}
		if f == 0.0 {
			return false
		}
	}

	return false
}

// UnmarshalJSON unmarshals JSON into Message with flexible battery_level and time_sensitive parsing,
// while preserving existing fields on the struct when omitted in the incoming JSON.
func (m *Message) UnmarshalJSON(b []byte) error {
	if m == nil {
		return errors.New("model.Message: UnmarshalJSON on nil pointer")
	}
	type Alias Message
	clonedAlias := Alias(*m)
	if m.BatteryLevel != nil {
		batt := *m.BatteryLevel
		clonedAlias.BatteryLevel = &batt
	}

	raw := struct {
		Alias
		BatteryLevel  json.RawMessage `json:"battery_level,omitempty"`
		TimeSensitive json.RawMessage `json:"time_sensitive,omitempty"`
	}{
		Alias: clonedAlias,
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*m = Message(raw.Alias)

	if len(raw.BatteryLevel) > 0 {
		m.BatteryLevel = parseBatteryLevel(raw.BatteryLevel)
	}
	if len(raw.TimeSensitive) > 0 {
		m.TimeSensitive = parseTimeSensitive(raw.TimeSensitive)
	}
	return nil
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

// SetTimeSensitive sets whether this message is time-sensitive.
func (m *Message) SetTimeSensitive(timeSensitive bool) {
	if m == nil {
		return
	}
	m.TimeSensitive = timeSensitive
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

	var metaLines []string
	if hostname := sanitizeSingleLine(m.Hostname); hostname != "" {
		metaLines = append(metaLines, fmt.Sprintf("设备主机名: %s", hostname))
	}
	if m.BatteryLevel != nil {
		metaLines = append(metaLines, fmt.Sprintf("当前电量: %d%%", *m.BatteryLevel))
	}
	if len(metaLines) > 0 {
		if text != "" {
			text += "\n"
		}
		text += strings.Join(metaLines, "\n") + "\n"
	}

	return text
}

func sanitizeSingleLine(s string) string {
	s = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ", "\t", " ").Replace(s)
	s = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 || r == 0x85 || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
			return ' '
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}
