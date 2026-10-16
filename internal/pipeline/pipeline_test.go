package pipeline

import (
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zapsaang/push-transfer/internal/config"
	"github.com/zapsaang/push-transfer/internal/model"
)

// TestRegexLookaround verifies regexp2 supports lookahead (?=...) and lookbehind (?<=...)
// which standard Go regexp does not support.
func TestRegexLookaround(t *testing.T) {
	// Standard library regexp should fail on lookbehind
	stdPattern := `(?<=验证码[:：])\d{4,6}`
	if _, err := regexp.Compile(stdPattern); err == nil { //nolint:staticcheck
		t.Fatal("expected standard regexp to fail on lookbehind, but it succeeded")
	}

	// SafeRegexEngine with lookbehind
	lookbehindPattern := `(?<=验证码[:：])\d{4,6}`
	engine, err := NewSafeRegexEngine(lookbehindPattern, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("failed to compile lookbehind pattern: %v", err)
	}

	match, err := engine.FindStringMatch("您的验证码:952701已生效")
	if err != nil {
		t.Fatalf("FindStringMatch failed: %v", err)
	}
	if match == nil {
		t.Fatal("expected lookbehind to match, but got nil")
	}
	if match.String() != "952701" {
		t.Fatalf("expected '952701', got %q", match.String())
	}

	// SafeRegexEngine with positive and negative lookahead
	lookaheadPattern := `\d{4,6}(?=[a-z]+)`
	engineLA, err := NewSafeRegexEngine(lookaheadPattern, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("failed to compile lookahead pattern: %v", err)
	}

	matchLA, err := engineLA.FindStringMatch("code 123456xyz end")
	if err != nil {
		t.Fatalf("FindStringMatch failed: %v", err)
	}
	if matchLA == nil || matchLA.String() != "123456" {
		t.Fatalf("expected '123456', got %v", matchLA)
	}
}

// TestRegexReDoSTimeout verifies that a catastrophic backtracking regex times out
// within ~500ms without blocking permanently.
func TestRegexReDoSTimeout(t *testing.T) {
	// Catastrophic backtracking pattern (O(2^n))
	pattern := `(a+)+$`
	timeout := 100 * time.Millisecond
	engine, err := NewSafeRegexEngine(pattern, timeout)
	if err != nil {
		t.Fatalf("failed to compile engine: %v", err)
	}

	// Non-matching input that causes backtracking
	evilInput := "aaaaaaaaaaaaaaaaaaaaaaaaaaaa!"

	start := time.Now()
	matched, err := engine.MatchString(evilInput)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected timeout error, got matched=%v, err=nil", matched)
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("expected error containing 'timeout', got: %v", err)
	}

	// Verify it returned promptly (within 600ms, well below permanent hang)
	if elapsed > 600*time.Millisecond {
		t.Fatalf("ReDoS took too long to abort: %v (expected <= 600ms)", elapsed)
	}
}

// TestRegexConcurrency verifies thread safety under high concurrency without data race (-race).
func TestRegexConcurrency(t *testing.T) {
	pattern := `(?<=code:)\d{4,6}`
	engine, err := NewSafeRegexEngine(pattern, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("failed to compile engine: %v", err)
	}

	const goroutines = 50
	const iterations = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				// Alternating match and non-match inputs
				matched, err := engine.MatchString("code:123456")
				if err != nil || !matched {
					t.Errorf("routine %d iter %d: expected match, got matched=%v, err=%v", id, j, matched, err)
					return
				}

				match, err := engine.FindStringMatch("prefix code:654321 suffix")
				if err != nil || match == nil {
					t.Errorf("routine %d iter %d: expected FindStringMatch result, got err=%v", id, j, err)
					return
				}
				if match.String() != "654321" {
					t.Errorf("routine %d iter %d: expected '654321', got %q", id, j, match.String())
					return
				}

				nonMatch, err := engine.MatchString("no match here")
				if err != nil || nonMatch {
					t.Errorf("routine %d iter %d: expected no match, got matched=%v, err=%v", id, j, nonMatch, err)
					return
				}
			}
		}(i)
	}

	wg.Wait()
}

// TestFilterStrategy verifies whitelist priority over blacklist, blacklist blocking, and non-matching pass.
func TestFilterStrategy(t *testing.T) {
	whiteList := []string{`(?i)important`, `(?i)bank`}
	blackList := []string{`(?i)spam`, `(?i)bank`} // "bank" is in both!
	filter, err := NewFilterStrategy(whiteList, blackList, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("failed to create FilterStrategy: %v", err)
	}

	// 1. Whitelist priority: matches both white and black -> PASS
	msgBoth := &model.Message{Content: "Bank notification with spam keyword"}
	pass, err := filter.Process(msgBoth)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !pass {
		t.Fatal("expected whitelist priority to pass message, but got blocked")
	}

	// 2. Blacklist blocking: matches black only -> BLOCK
	msgBlack := &model.Message{Content: "This is pure spam advertising"}
	pass, err = filter.Process(msgBlack)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pass {
		t.Fatal("expected blacklist to block message, but got passed")
	}

	// 3. Non-matching pass: matches neither -> PASS
	msgNeither := &model.Message{Content: "Hello friend, see you tomorrow"}
	pass, err = filter.Process(msgNeither)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !pass {
		t.Fatal("expected non-matching message to pass, but got blocked")
	}

	// 4. Empty content pass
	msgEmpty := &model.Message{Content: ""}
	pass, err = filter.Process(msgEmpty)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !pass {
		t.Fatal("expected empty content to pass, but got blocked")
	}
}

// TestCaptchaStrategy verifies regex with capture group extracts group 1,
// while regex without capture group extracts full match.
func TestCaptchaStrategy(t *testing.T) {
	patterns := []string{
		`验证码[是为：:]\s*(\d{4,6})`, // Group 1 capture
		`\b\d{6}\b`,             // No capture group (full match)
	}
	captcha, err := NewCaptchaStrategy(patterns, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("failed to create CaptchaStrategy: %v", err)
	}

	// 1. Regex with capture group: must extract digits only (Group 1), not "验证码是：952701"
	msgGroup := &model.Message{Content: "【招商银行】您的验证码是：952701，5分钟内有效。"}
	pass, err := captcha.Process(msgGroup)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !pass {
		t.Fatal("expected captcha strategy to return pass=true")
	}
	if msgGroup.CaptchaCode != "952701" {
		t.Fatalf("expected CaptchaCode='952701', got %q", msgGroup.CaptchaCode)
	}

	// 2. Regex without capture group: extracts full match
	msgFull := &model.Message{Content: "Login passcode: 839201 for account"}
	pass, err = captcha.Process(msgFull)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !pass {
		t.Fatal("expected captcha strategy to return pass=true")
	}
	if msgFull.CaptchaCode != "839201" {
		t.Fatalf("expected CaptchaCode='839201', got %q", msgFull.CaptchaCode)
	}

	// 3. No match
	msgNoMatch := &model.Message{Content: "Normal conversation without any code"}
	pass, err = captcha.Process(msgNoMatch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !pass {
		t.Fatal("expected captcha strategy to return pass=true")
	}
	if msgNoMatch.CaptchaCode != "" {
		t.Fatalf("expected empty CaptchaCode, got %q", msgNoMatch.CaptchaCode)
	}
}

// TestEmergencyStrategy verifies that matching patterns set Emergency = true.
func TestEmergencyStrategy(t *testing.T) {
	patterns := []string{`(?i)110|120|紧急|fire|alarm`}
	emergency, err := NewEmergencyStrategy(patterns, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("failed to create EmergencyStrategy: %v", err)
	}

	// 1. Matches emergency
	msgAlarm := &model.Message{Content: "机房温度过高，触发火灾alarm！"}
	pass, err := emergency.Process(msgAlarm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !pass {
		t.Fatal("expected emergency strategy to return pass=true")
	}
	if !msgAlarm.Emergency {
		t.Fatal("expected Emergency=true, got false")
	}

	// 2. Non-matching message
	msgNormal := &model.Message{Content: "普通提醒：请按时吃饭"}
	pass, err = emergency.Process(msgNormal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !pass {
		t.Fatal("expected emergency strategy to return pass=true")
	}
	if msgNormal.Emergency {
		t.Fatal("expected Emergency=false, got true")
	}
}

// TestPipelineExecution verifies text message runs filter -> captcha -> emergency,
// call message passes through, and blocked message returns false.
func TestPipelineExecution(t *testing.T) {
	rules := config.RulesConfig{
		FilterWhite: []string{`(?i)white`},
		FilterBlack: []string{`(?i)ad|marketing`},
		Captcha:     []string{`code:\s*(\d{4,6})`},
		Emergency:   []string{`(?i)urgent`},
	}

	pipeline, err := NewPipelineFromRules(rules, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("failed to create Pipeline from rules: %v", err)
	}

	// 1. Text message with full pipeline matching
	textMsg := &model.Message{
		MsgType: "text",
		Title:   "Service",
		Content: "URGENT notice: code: 123456",
	}
	pass, err := pipeline.Process(textMsg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !pass {
		t.Fatal("expected text message to pass")
	}
	if textMsg.CaptchaCode != "123456" {
		t.Fatalf("expected CaptchaCode='123456', got %q", textMsg.CaptchaCode)
	}
	if !textMsg.Emergency {
		t.Fatal("expected Emergency=true, got false")
	}

	// 2. Blocked text message
	blockedMsg := &model.Message{
		MsgType: "text",
		Title:   "Spam",
		Content: "Huge Marketing discount today! code: 999999",
	}
	pass, err = pipeline.Process(blockedMsg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pass {
		t.Fatal("expected marketing text message to be blocked (pass=false)")
	}
	// Captcha should not have been extracted because pipeline stopped on filter block
	if blockedMsg.CaptchaCode != "" {
		t.Fatalf("expected CaptchaCode to remain empty on blocked message, got %q", blockedMsg.CaptchaCode)
	}

	// 3. Call message passes through directly without filtering
	callMsg := &model.Message{
		MsgType: "call",
		Title:   "10086",
		Content: "Marketing robocall", // even with blacklist word
	}
	pass, err = pipeline.Process(callMsg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !pass {
		t.Fatal("expected call message to pass through unconditionally")
	}
}

// TestEngineEdgeCases tests invalid patterns, default timeouts, and accessors.
func TestEngineEdgeCases(t *testing.T) {
	// Invalid regex
	_, err := NewSafeRegexEngine("[invalid", 0)
	if err == nil {
		t.Fatal("expected error on invalid regex pattern, got nil")
	}

	// Default timeout <= 0
	engine, err := NewSafeRegexEngine(`\d+`, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if engine.Timeout() != DefaultMatchTimeout {
		t.Fatalf("expected DefaultMatchTimeout (%v), got %v", DefaultMatchTimeout, engine.Timeout())
	}
	if engine.Pattern() != `\d+` {
		t.Fatalf("expected pattern '\\d+', got %q", engine.Pattern())
	}
}

// TestFilterEdgeCases tests nil msg, compilation errors, and error propagation.
func TestFilterEdgeCases(t *testing.T) {
	// Invalid white pattern
	_, err := NewFilterStrategy([]string{"[bad("}, nil, 0)
	if err == nil {
		t.Fatal("expected error on invalid white pattern")
	}

	// Invalid black pattern
	_, err = NewFilterStrategy(nil, []string{"[bad("}, 0)
	if err == nil {
		t.Fatal("expected error on invalid black pattern")
	}

	filter, err := NewFilterStrategy([]string{"ok"}, []string{"(a+)+$"}, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Nil message
	pass, err := filter.Process(nil)
	if err != nil || !pass {
		t.Fatalf("expected pass=true, err=nil for nil msg, got pass=%v, err=%v", pass, err)
	}

	// White pattern ReDoS error
	filterTimeout, err := NewFilterStrategy([]string{"(a+)+$"}, nil, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, err = filterTimeout.Process(&model.Message{Content: "aaaaaaaaaaaaaaaaaaaaaaaaaaaa!"})
	if err == nil {
		t.Fatal("expected error on whitelist regex timeout")
	}

	// Black pattern ReDoS error
	_, err = filter.Process(&model.Message{Content: "aaaaaaaaaaaaaaaaaaaaaaaaaaaa!"})
	if err == nil {
		t.Fatal("expected error on blacklist regex timeout")
	}
}

// TestCaptchaEdgeCases tests compilation failure, nil msg, and timeout handling.
func TestCaptchaEdgeCases(t *testing.T) {
	_, err := NewCaptchaStrategy([]string{"(unclosed"}, 0)
	if err == nil {
		t.Fatal("expected error on invalid captcha pattern")
	}

	captcha, err := NewCaptchaStrategy([]string{"(a+)+$"}, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Nil msg
	pass, err := captcha.Process(nil)
	if err != nil || !pass {
		t.Fatalf("expected pass=true, err=nil for nil msg, got pass=%v, err=%v", pass, err)
	}

	// Empty content
	pass, err = captcha.Process(&model.Message{Content: ""})
	if err != nil || !pass {
		t.Fatalf("expected pass=true, err=nil for empty content, got pass=%v, err=%v", pass, err)
	}

	// ReDoS timeout error
	_, err = captcha.Process(&model.Message{Content: "aaaaaaaaaaaaaaaaaaaaaaaaaaaa!"})
	if err == nil {
		t.Fatal("expected error on captcha regex timeout")
	}
}

// TestEmergencyEdgeCases tests compilation failure, nil msg, and timeout handling.
func TestEmergencyEdgeCases(t *testing.T) {
	_, err := NewEmergencyStrategy([]string{"(?<bad"}, 0)
	if err == nil {
		t.Fatal("expected error on invalid emergency pattern")
	}

	emergency, err := NewEmergencyStrategy([]string{"(a+)+$"}, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Nil msg
	pass, err := emergency.Process(nil)
	if err != nil || !pass {
		t.Fatalf("expected pass=true, err=nil for nil msg, got pass=%v, err=%v", pass, err)
	}

	// Empty content
	pass, err = emergency.Process(&model.Message{Content: ""})
	if err != nil || !pass {
		t.Fatalf("expected pass=true, err=nil for empty content, got pass=%v, err=%v", pass, err)
	}

	// ReDoS timeout error
	_, err = emergency.Process(&model.Message{Content: "aaaaaaaaaaaaaaaaaaaaaaaaaaaa!"})
	if err == nil {
		t.Fatal("expected error on emergency regex timeout")
	}
}

// TestPipelineEdgeCases tests rules creation failures, Execute alias, nil msg, and unknown msg type.
func TestPipelineEdgeCases(t *testing.T) {
	// Invalid rules
	_, err := NewPipelineFromRules(config.RulesConfig{FilterWhite: []string{"[bad"}}, 0)
	if err == nil {
		t.Fatal("expected error on invalid FilterWhite in rules")
	}

	_, err = NewPipelineFromRules(config.RulesConfig{Captcha: []string{"[bad"}}, 0)
	if err == nil {
		t.Fatal("expected error on invalid Captcha in rules")
	}

	_, err = NewPipelineFromRules(config.RulesConfig{Emergency: []string{"[bad"}}, 0)
	if err == nil {
		t.Fatal("expected error on invalid Emergency in rules")
	}

	pipeline, err := NewPipelineFromRules(config.RulesConfig{}, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Nil message
	pass, err := pipeline.Process(nil)
	if err != nil || !pass {
		t.Fatalf("expected pass=true, err=nil for nil msg, got pass=%v, err=%v", pass, err)
	}

	// Unknown msg_type passes through
	unknownMsg := &model.Message{MsgType: "unknown", Content: "something"}
	pass, err = pipeline.Process(unknownMsg)
	if err != nil || !pass {
		t.Fatalf("expected pass=true for unknown msg type")
	}

	// Execute alias
	pass, err = pipeline.Execute(&model.Message{MsgType: "call"})
	if err != nil || !pass {
		t.Fatalf("expected pass=true from Execute alias")
	}

	// Error in filter strategy during pipeline execution
	failingFilter, _ := NewFilterStrategy(nil, []string{"(a+)+$"}, 50*time.Millisecond)
	pWithFail := NewPipeline(failingFilter, nil, nil)
	_, err = pWithFail.Process(&model.Message{MsgType: "text", Content: "aaaaaaaaaaaaaaaaaaaaaaaaaaaa!"})
	if err == nil {
		t.Fatal("expected pipeline to return error when filter strategy fails")
	}

	// Warning in captcha / emergency should not abort text message
	failingCaptcha, _ := NewCaptchaStrategy([]string{"(a+)+$"}, 50*time.Millisecond)
	failingEmergency, _ := NewEmergencyStrategy([]string{"(a+)+$"}, 50*time.Millisecond)
	pWithWarn := NewPipeline(nil, failingCaptcha, failingEmergency)
	pass, err = pWithWarn.Process(&model.Message{MsgType: "text", Content: "aaaaaaaaaaaaaaaaaaaaaaaaaaaa!"})
	if err != nil || !pass {
		t.Fatalf("expected pipeline to pass even if captcha/emergency fail with warning, got pass=%v, err=%v", pass, err)
	}
}

func TestPipeline_CustomLoggerInjection(t *testing.T) {
	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	rules := config.RulesConfig{
		FilterBlack: []string{`(?i)spam`},
	}
	p, err := NewPipelineFromRules(rules, 100*time.Millisecond, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	pass, err := p.Process(&model.Message{
		MsgType: "text",
		Title:   "CustomLogSpam",
		Content: "this is spam message",
	})
	if err != nil || pass {
		t.Fatalf("expected message to be blocked without error, got pass=%v, err=%v", pass, err)
	}

	if !strings.Contains(buf.String(), "pipeline: message rejected by filter strategy") || !strings.Contains(buf.String(), "CustomLogSpam") {
		t.Fatalf("expected custom logger buffer to receive pipeline rejection log, got: %q", buf.String())
	}
}
