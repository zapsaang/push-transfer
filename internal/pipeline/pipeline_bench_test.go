package pipeline

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/zapsaang/push-transfer/internal/config"
	"github.com/zapsaang/push-transfer/internal/model"
)

func BenchmarkPipeline_Process_Pass(b *testing.B) {
	rules := config.RulesConfig{
		FilterWhite: []string{`^重要通知`, `(?i)whitelist`},
		FilterBlack: []string{`^垃圾短信`, `(?i)spam`},
		Captcha:     []string{`验证码[是为：:]\s*(\d{4,6})`, `code[: ]+(\d{6})`},
		Emergency:   []string{`(?i)urgent`, `(?i)emergency`, `报警`},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pipe, err := NewPipelineFromRules(rules, 500*time.Millisecond, logger)
	if err != nil {
		b.Fatal(err)
	}

	msg := &model.Message{
		MsgType: model.MsgTypeText,
		Title:   "10086",
		Content: "您本月话费账单已生成，请及时查看。",
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Reset state that may be mutated by strategies
		msg.CaptchaCode = ""
		msg.TimeSensitive = false
		msg.Emergency = false

		pass, err := pipe.Process(msg)
		if err != nil || !pass {
			b.Fatalf("expected pass, got pass=%v, err=%v", pass, err)
		}
	}
}

func BenchmarkPipeline_Process_Captcha(b *testing.B) {
	rules := config.RulesConfig{
		FilterWhite: []string{`^重要通知`},
		FilterBlack: []string{`^垃圾短信`},
		Captcha:     []string{`验证码[是为：:]\s*(\d{4,6})`, `code[: ]+(\d{6})`},
		Emergency:   []string{`(?i)urgent`},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pipe, err := NewPipelineFromRules(rules, 500*time.Millisecond, logger)
	if err != nil {
		b.Fatal(err)
	}

	msg := &model.Message{
		MsgType: model.MsgTypeText,
		Title:   "招商银行",
		Content: "【招商银行】您的动态验证码是：892103，请勿泄露给他人。",
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		msg.CaptchaCode = ""
		msg.TimeSensitive = false
		msg.Emergency = false

		pass, err := pipe.Process(msg)
		if err != nil || !pass {
			b.Fatalf("expected pass, got pass=%v, err=%v", pass, err)
		}
	}
}

func BenchmarkPipeline_Process_Emergency(b *testing.B) {
	rules := config.RulesConfig{
		FilterWhite: []string{`^重要通知`},
		FilterBlack: []string{`^垃圾短信`},
		Captcha:     []string{`验证码[是为：:]\s*(\d{4,6})`},
		Emergency:   []string{`(?i)urgent`, `(?i)emergency`, `报警`, `严重警告`},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pipe, err := NewPipelineFromRules(rules, 500*time.Millisecond, logger)
	if err != nil {
		b.Fatal(err)
	}

	msg := &model.Message{
		MsgType: model.MsgTypeText,
		Title:   "监控中心",
		Content: "【严重警告】生产集群发生异常，请立即排查！",
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		msg.CaptchaCode = ""
		msg.TimeSensitive = false
		msg.Emergency = false

		pass, err := pipe.Process(msg)
		if err != nil || !pass {
			b.Fatalf("expected pass, got pass=%v, err=%v", pass, err)
		}
	}
}

func BenchmarkPipeline_Process_Whitelist(b *testing.B) {
	rules := config.RulesConfig{
		FilterWhite: []string{`^重要通知`, `(?i)whitelist`},
		FilterBlack: []string{`.*`}, // All else blacklisted
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pipe, err := NewPipelineFromRules(rules, 500*time.Millisecond, logger)
	if err != nil {
		b.Fatal(err)
	}

	msg := &model.Message{
		MsgType: model.MsgTypeText,
		Title:   "系统通知",
		Content: "重要通知：白名单短信内容放行测试",
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pass, err := pipe.Process(msg)
		if err != nil || !pass {
			b.Fatalf("expected pass, got pass=%v, err=%v", pass, err)
		}
	}
}

func BenchmarkPipeline_Process_Blacklist(b *testing.B) {
	rules := config.RulesConfig{
		FilterWhite: []string{`^重要通知`},
		FilterBlack: []string{`^垃圾短信`, `(?i)spam`, `退订`},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pipe, err := NewPipelineFromRules(rules, 500*time.Millisecond, logger)
	if err != nil {
		b.Fatal(err)
	}

	msg := &model.Message{
		MsgType: model.MsgTypeText,
		Title:   "营销短信",
		Content: "买一送一特惠活动，回复TD退订",
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pass, err := pipe.Process(msg)
		if err != nil || pass {
			b.Fatalf("expected rejected, got pass=%v, err=%v", pass, err)
		}
	}
}

func BenchmarkPipeline_Process_AppNotification(b *testing.B) {
	rules := config.RulesConfig{
		FilterWhite: []string{`^重要通知`},
		FilterBlack: []string{`^广告`},
		Captcha:     []string{`验证码[是为：:]\s*(\d{4,6})`},
		Emergency:   []string{`(?i)emergency`},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pipe, err := NewPipelineFromRules(rules, 500*time.Millisecond, logger)
	if err != nil {
		b.Fatal(err)
	}

	battery := 90
	msg := &model.Message{
		MsgType:      model.MsgTypeAppNotification,
		Title:        "微信",
		Content:      "张三：今晚一起吃饭吗？",
		AppName:      "WeChat",
		Hostname:     "Pixel-7",
		BatteryLevel: &battery,
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		msg.CaptchaCode = ""
		msg.TimeSensitive = false
		msg.Emergency = false

		pass, err := pipe.Process(msg)
		if err != nil || !pass {
			b.Fatalf("expected pass, got pass=%v, err=%v", pass, err)
		}
	}
}
