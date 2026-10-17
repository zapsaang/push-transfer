package deliver_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zapsaang/push-transfer/internal/crypto"
	"github.com/zapsaang/push-transfer/internal/deliver"
	"github.com/zapsaang/push-transfer/internal/ingress/imap"
	"github.com/zapsaang/push-transfer/internal/model"
)

func TestBarkDeliver(t *testing.T) {
	t.Run("plaintext payload verification", func(t *testing.T) {
		var receivedBody map[string]interface{}
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			_ = json.Unmarshal(b, &receivedBody)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"code": 200, "message": "success"}`))
		}))
		defer ts.Close()

		bark := deliver.NewBarkDeliver(deliver.BarkOptions{
			PostURL:    ts.URL,
			DeviceKeys: []string{"dev_test_123"},
		})

		msg := &model.Message{
			MsgType:     model.MsgTypeText,
			Title:       "10086",
			Content:     "Your code is 123456",
			CaptchaCode: "123456",
			Emergency:   true,
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		err := bark.Send(ctx, msg)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		if receivedBody["title"] != "New Message From 10086" {
			t.Errorf("expected title 'New Message From 10086', got %v", receivedBody["title"])
		}
		if receivedBody["group"] != "text" {
			t.Errorf("expected group 'text', got %v", receivedBody["group"])
		}
		if receivedBody["body"] != "Your code is 123456" {
			t.Errorf("expected body 'Your code is 123456', got %v", receivedBody["body"])
		}
		if receivedBody["device_key"] != "dev_test_123" {
			t.Errorf("expected device_key 'dev_test_123', got %v", receivedBody["device_key"])
		}
		if receivedBody["copy"] != "123456" {
			t.Errorf("expected copy '123456', got %v", receivedBody["copy"])
		}
		if receivedBody["level"] != "critical" {
			t.Errorf("expected level 'critical', got %v", receivedBody["level"])
		}
		if int(receivedBody["autoCopy"].(float64)) != 1 {
			t.Errorf("expected autoCopy 1, got %v", receivedBody["autoCopy"])
		}
		if int(receivedBody["isArchive"].(float64)) != 1 {
			t.Errorf("expected isArchive 1, got %v", receivedBody["isArchive"])
		}
		if receivedBody["volume"] != "5" {
			t.Errorf("expected the documented string volume \"5\", got %#v", receivedBody["volume"])
		}
	})

	t.Run("call message payload verification", func(t *testing.T) {
		var receivedBody map[string]interface{}
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			_ = json.Unmarshal(b, &receivedBody)
			w.WriteHeader(http.StatusOK)
		}))
		defer ts.Close()

		bark := deliver.NewBarkDeliver(deliver.BarkOptions{
			PostURL:    ts.URL,
			DeviceKeys: []string{"dev_call_key"},
		})

		msg := &model.Message{
			MsgType: model.MsgTypeCall,
			Title:   "13800138000",
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		err := bark.Send(ctx, msg)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		if receivedBody["title"] != "New Call From 13800138000" {
			t.Errorf("expected title 'New Call From 13800138000', got %v", receivedBody["title"])
		}
		if receivedBody["group"] != "call" {
			t.Errorf("expected group 'call', got %v", receivedBody["group"])
		}
		if receivedBody["copy"] != "13800138000" {
			t.Errorf("expected copy '13800138000', got %v", receivedBody["copy"])
		}
		bodyVal, ok := receivedBody["body"].(string)
		if !ok || bodyVal != "" {
			t.Errorf("expected body to be empty string \"\", got %v (type %T)", receivedBody["body"], receivedBody["body"])
		}
	})

	t.Run("call message with non-empty content preserves body string", func(t *testing.T) {
		var receivedBody map[string]interface{}
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			_ = json.Unmarshal(b, &receivedBody)
			w.WriteHeader(http.StatusOK)
		}))
		defer ts.Close()

		bark := deliver.NewBarkDeliver(deliver.BarkOptions{
			PostURL:    ts.URL,
			DeviceKeys: []string{"dev_call_key"},
		})

		msg := &model.Message{
			MsgType: model.MsgTypeCall,
			Title:   "13800138000",
			Content: "Missed 3 times",
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		err := bark.Send(ctx, msg)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		if receivedBody["body"] != "Missed 3 times" {
			t.Errorf("expected body 'Missed 3 times', got %v", receivedBody["body"])
		}
	})

	t.Run("ciphertext payload when crypto key is provided", func(t *testing.T) {
		aesKey := []byte("1234567890123456") // 16 bytes
		var receivedBody map[string]interface{}
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			_ = json.Unmarshal(b, &receivedBody)
			w.WriteHeader(http.StatusOK)
		}))
		defer ts.Close()

		bark := deliver.NewBarkDeliver(deliver.BarkOptions{
			PostURL:    ts.URL,
			DeviceKeys: []string{"dev_crypto_123"},
			CryptoKey:  aesKey,
		})

		msg := &model.Message{
			MsgType:     model.MsgTypeText,
			Title:       "SecretBank",
			Content:     "Your secret is safe",
			CaptchaCode: "8888",
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		err := bark.Send(ctx, msg)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		cipherTextStr, ok := receivedBody["ciphertext"].(string)
		if !ok || cipherTextStr == "" {
			t.Fatalf("expected ciphertext in payload, got %v", receivedBody)
		}

		// Bark decrypts with the *separate* top-level `iv` parameter; the IV must not be
		// embedded in the ciphertext. See https://bark.day.app/en-us/params.md ("iv").
		ivStr, ok := receivedBody["iv"].(string)
		if !ok || ivStr == "" {
			t.Fatalf("expected a separate top-level `iv` field for encrypted Bark pushes, got %v", receivedBody)
		}
		if len(ivStr) != 16 {
			t.Fatalf("expected `iv` to be exactly 16 bytes (Bark AES-128-CBC), got %d (%q)", len(ivStr), ivStr)
		}

		// Decrypt and verify payload
		decrypted, err := crypto.DecryptBark(crypto.BarkPayload{Ciphertext: cipherTextStr, IV: ivStr}, aesKey)
		if err != nil {
			t.Fatalf("failed to decrypt Bark payload: %v", err)
		}

		var innerPayload map[string]interface{}
		if err := json.Unmarshal(decrypted, &innerPayload); err != nil {
			t.Fatalf("failed to unmarshal decrypted JSON: %v", err)
		}

		if innerPayload["title"] != "New Message From SecretBank" {
			t.Errorf("decrypted title mismatch: %v", innerPayload["title"])
		}
		if innerPayload["group"] != "text" {
			t.Errorf("decrypted group mismatch: %v", innerPayload["group"])
		}
		if innerPayload["body"] != "Your secret is safe" {
			t.Errorf("decrypted body mismatch: %v", innerPayload["body"])
		}
		if innerPayload["copy"] != "8888" {
			t.Errorf("decrypted copy mismatch: %v", innerPayload["copy"])
		}
	})
}

func TestTelegramDeliver(t *testing.T) {
	var receivedBody map[string]interface{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/bot123456:ABC-DEF/sendMessage") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = json.Unmarshal(b, &receivedBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok": true}`))
	}))
	defer ts.Close()

	tg := deliver.NewTelegramDeliver(deliver.TelegramOptions{
		BaseURL:  ts.URL,
		BotToken: "123456:ABC-DEF",
		ChatID:   987654321,
	})

	battery := 85
	msg := &model.Message{
		MsgType:      model.MsgTypeText,
		Title:        "<Bank & Co>",
		Content:      "Alert: balance < $100 & > $50",
		BatteryLevel: &battery,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := tg.Send(ctx, msg)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if int64(receivedBody["chat_id"].(float64)) != 987654321 {
		t.Errorf("expected chat_id 987654321, got %v", receivedBody["chat_id"])
	}
	if receivedBody["parse_mode"] != "HTML" {
		t.Errorf("expected parse_mode 'HTML', got %v", receivedBody["parse_mode"])
	}

	text, ok := receivedBody["text"].(string)
	if !ok {
		t.Fatalf("expected string text in body, got %v", receivedBody["text"])
	}

	// Verify HTML escaping of < and &
	if strings.Contains(text, "<Bank & Co>") {
		t.Errorf("text was not properly HTML escaped: %s", text)
	}
	if !strings.Contains(text, "&lt;Bank &amp; Co&gt;") {
		t.Errorf("expected escaped text, got: %s", text)
	}

	t.Run("excessively long text is truncated safely", func(t *testing.T) {
		longContent := strings.Repeat("长消息内容ABC123", 500) // ~5500 runes
		longMsg := &model.Message{
			MsgType: model.MsgTypeText,
			Title:   "LongSender",
			Content: longContent,
		}

		err := tg.Send(context.Background(), longMsg)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		sentText, ok := receivedBody["text"].(string)
		if !ok {
			t.Fatalf("expected string text in body, got %v", receivedBody["text"])
		}

		runeCount := len([]rune(sentText))
		if runeCount > 4096 {
			t.Errorf("expected text length <= 4096 runes, got %d", runeCount)
		}
		if !strings.HasSuffix(sentText, "...") {
			t.Errorf("expected truncated text to end with '...', got ending %q", sentText[len(sentText)-10:])
		}
	})

	t.Run("long text with entities near boundary avoids broken entity", func(t *testing.T) {
		// 3990 standard characters followed by special chars < and &
		edgeContent := strings.Repeat("a", 3990) + "<>&<>&<>&" + strings.Repeat("z", 100)
		edgeMsg := &model.Message{
			MsgType: model.MsgTypeText,
			Title:   "EdgeSender",
			Content: edgeContent,
		}

		err := tg.Send(context.Background(), edgeMsg)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		sentText, ok := receivedBody["text"].(string)
		if !ok {
			t.Fatalf("expected string text in body, got %v", receivedBody["text"])
		}

		runeCount := len([]rune(sentText))
		if runeCount > 4096 {
			t.Errorf("expected text length <= 4096 runes, got %d", runeCount)
		}
		if !strings.HasSuffix(sentText, "...") {
			t.Errorf("expected truncated text to end with '...', got ending %q", sentText[len(sentText)-10:])
		}
		// Ensure no unclosed entity right before "..."
		trimmed := strings.TrimSuffix(sentText, "...")
		lastAmp := strings.LastIndex(trimmed, "&")
		if lastAmp != -1 {
			lastSemi := strings.LastIndex(trimmed, ";")
			if lastSemi < lastAmp {
				t.Errorf("found broken HTML entity at boundary in %q", trimmed[lastAmp:])
			}
		}
	})
}

func TestTelegramDeliver_AppNotification(t *testing.T) {
	var receivedBody map[string]interface{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = json.Unmarshal(b, &receivedBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok": true}`))
	}))
	defer ts.Close()

	tg := deliver.NewTelegramDeliver(deliver.TelegramOptions{
		BaseURL:  ts.URL,
		BotToken: "token",
		ChatID:   123456,
	})

	msg := &model.Message{
		MsgType:   model.MsgTypeAppNotification,
		AppName:   "微信",
		Title:     "<群聊>",
		Content:   "重要通知 <alert>",
		Timestamp: 1700000000,
	}

	err := tg.Send(context.Background(), msg)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	text, ok := receivedBody["text"].(string)
	if !ok {
		t.Fatalf("expected string text in body, got %v", receivedBody["text"])
	}
	if !strings.Contains(text, "【微信】&lt;群聊&gt;") {
		t.Errorf("expected escaped app title, got: %s", text)
	}
	if !strings.Contains(text, "重要通知 &lt;alert&gt;") {
		t.Errorf("expected escaped content, got: %s", text)
	}
}

func TestConsoleDeliver(t *testing.T) {
	console := deliver.NewConsoleDeliver()
	msg := &model.Message{
		MsgType: model.MsgTypeText,
		Title:   "Console Test",
		Content: "Hello Console",
	}

	err := console.Send(context.Background(), msg)
	if err != nil {
		t.Fatalf("console deliver should not return error, got %v", err)
	}
}

type mockTarget struct {
	name      string
	delay     time.Duration
	returnErr error
	callCount int32
}

func (m *mockTarget) Name() string { return m.name }
func (m *mockTarget) Send(ctx context.Context, _ *model.Message) error {
	atomic.AddInt32(&m.callCount, 1)
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return m.returnErr
}

func TestDispatcher_ConcurrencyAndTimeout(t *testing.T) {
	slowTarget := &mockTarget{
		name:  "slow",
		delay: 10 * time.Second,
	}

	dispatcher := deliver.NewDispatcher(deliver.DispatcherOptions{
		Timeout: 300 * time.Millisecond,
		Targets: []deliver.Target{slowTarget},
	})

	start := time.Now()
	err := dispatcher.Deliver(context.Background(), &model.Message{Title: "timeout test"})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("dispatcher hung! Elapsed: %v", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Errorf("expected deadline exceeded error, got %v", err)
	}
}

func TestDispatcher_PartialAndAllFailed(t *testing.T) {
	t.Run("all targets fail", func(t *testing.T) {
		fail1 := &mockTarget{name: "f1", returnErr: errors.New("network error")}
		fail2 := &mockTarget{name: "f2", returnErr: errors.New("auth error")}

		dispatcher := deliver.NewDispatcher(deliver.DispatcherOptions{
			Timeout: 1 * time.Second,
			Targets: []deliver.Target{fail1, fail2},
		})

		err := dispatcher.Deliver(context.Background(), &model.Message{Title: "all fail test"})
		if err == nil {
			t.Fatal("expected error when all targets fail, got nil")
		}
		if !errors.Is(err, deliver.ErrAllTargetsFailed) {
			t.Errorf("expected ErrAllTargetsFailed, got %v", err)
		}
	})

	t.Run("at least one target succeeds (partial success)", func(t *testing.T) {
		fail := &mockTarget{name: "f1", returnErr: errors.New("transient error")}
		success := &mockTarget{name: "s1", returnErr: nil}

		dispatcher := deliver.NewDispatcher(deliver.DispatcherOptions{
			Timeout: 1 * time.Second,
			Targets: []deliver.Target{fail, success},
		})

		err := dispatcher.Deliver(context.Background(), &model.Message{Title: "partial success test"})
		if err != nil {
			t.Fatalf("expected nil error on partial success, got: %v", err)
		}
	})

	t.Run("empty targets list returns nil", func(t *testing.T) {
		dispatcher := deliver.NewDispatcher(deliver.DispatcherOptions{})
		err := dispatcher.Deliver(context.Background(), &model.Message{Title: "test"})
		if err != nil {
			t.Fatalf("expected nil for empty targets, got: %v", err)
		}
		err = dispatcher.Send(context.Background(), &model.Message{Title: "test"})
		if err != nil {
			t.Fatalf("expected nil for Send with empty targets, got: %v", err)
		}
	})
}

func TestEmailDeliver(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer func() { _ = clientConn.Close() }()
	defer func() { _ = serverConn.Close() }()

	var receivedData strings.Builder
	go func() {
		// SMTP Server mock
		r := bufio.NewReader(serverConn)
		w := bufio.NewWriter(serverConn)
		writeLine := func(s string) {
			_, _ = w.WriteString(s)
			_ = w.Flush()
		}

		// 220 banner
		writeLine("220 smtp.example.com ESMTP\r\n")

		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			switch {
			case strings.HasPrefix(line, "EHLO") || strings.HasPrefix(line, "HELO"):
				writeLine("250-smtp.example.com\r\n250 AUTH PLAIN\r\n")
			case strings.HasPrefix(line, "AUTH PLAIN"):
				writeLine("235 2.7.0 Authentication successful\r\n")
			case strings.HasPrefix(line, "MAIL FROM:"):
				writeLine("250 2.1.0 Ok\r\n")
			case strings.HasPrefix(line, "RCPT TO:"):
				writeLine("250 2.1.5 Ok\r\n")
			case line == "DATA":
				writeLine("354 End data with <CR><LF>.<CR><LF>\r\n")
				for {
					dataLine, err := r.ReadString('\n')
					if err != nil {
						return
					}
					if dataLine == ".\r\n" {
						writeLine("250 2.0.0 Ok: queued\r\n")
						break
					}
					receivedData.WriteString(dataLine)
				}
			case line == "QUIT":
				writeLine("221 2.0.0 Bye\r\n")
				return
			}
		}
	}()

	emailClient := deliver.NewEmailDeliver(deliver.EmailOptions{
		SenderEmail:   "sender@example.com",
		ReceiverEmail: "receiver@example.com",
		Password:      "mypassword",
		SMTPServer:    "localhost",
		SMTPPort:      465,
		CustomDialer: func(_ context.Context, _, _ string) (net.Conn, error) {
			return clientConn, nil
		},
	})

	if emailClient.Name() != "email" {
		t.Errorf("expected name 'email', got %s", emailClient.Name())
	}

	msg := &model.Message{
		MsgType: model.MsgTypeText,
		Title:   "10086",
		Content: "Balance alert",
	}

	err := emailClient.Send(context.Background(), msg)
	if err != nil {
		t.Fatalf("email Send failed: %v", err)
	}

	emailContent := receivedData.String()
	if !strings.Contains(emailContent, "Subject: New Message From 10086") {
		t.Errorf("expected subject in email, got: %s", emailContent)
	}
	if !strings.Contains(emailContent, "From: sender@example.com") {
		t.Errorf("expected From in email, got: %s", emailContent)
	}
	if !strings.Contains(emailContent, "To: receiver@example.com") {
		t.Errorf("expected To in email, got: %s", emailContent)
	}
	if !strings.Contains(emailContent, "Balance alert") {
		t.Errorf("expected body in email, got: %s", emailContent)
	}

	runMockSMTP := func(t *testing.T, msg *model.Message) string {
		t.Helper()
		cConn, sConn := net.Pipe()
		defer func() { _ = cConn.Close() }()
		defer func() { _ = sConn.Close() }()

		var dataBuf strings.Builder
		go func() {
			r := bufio.NewReader(sConn)
			w := bufio.NewWriter(sConn)
			writeLine := func(s string) {
				_, _ = w.WriteString(s)
				_ = w.Flush()
			}
			writeLine("220 smtp.example.com ESMTP\r\n")
			for {
				line, err := r.ReadString('\n')
				if err != nil {
					return
				}
				line = strings.TrimRight(line, "\r\n")
				switch {
				case strings.HasPrefix(line, "EHLO") || strings.HasPrefix(line, "HELO"):
					writeLine("250-smtp.example.com\r\n250 AUTH PLAIN\r\n")
				case strings.HasPrefix(line, "AUTH PLAIN"):
					writeLine("235 2.7.0 Authentication successful\r\n")
				case strings.HasPrefix(line, "MAIL FROM:"):
					writeLine("250 2.1.0 Ok\r\n")
				case strings.HasPrefix(line, "RCPT TO:"):
					writeLine("250 2.1.5 Ok\r\n")
				case line == "DATA":
					writeLine("354 End data with <CR><LF>.<CR><LF>\r\n")
					for {
						dataLine, err := r.ReadString('\n')
						if err != nil {
							return
						}
						if dataLine == ".\r\n" {
							writeLine("250 2.0.0 Ok: queued\r\n")
							break
						}
						dataBuf.WriteString(dataLine)
					}
				case line == "QUIT":
					writeLine("221 2.0.0 Bye\r\n")
					return
				}
			}
		}()

		client := deliver.NewEmailDeliver(deliver.EmailOptions{
			SenderEmail:   "sender@example.com",
			ReceiverEmail: "receiver@example.com",
			Password:      "mypassword",
			SMTPServer:    "localhost",
			SMTPPort:      465,
			CustomDialer: func(_ context.Context, _, _ string) (net.Conn, error) {
				return cConn, nil
			},
		})
		if err := client.Send(context.Background(), msg); err != nil {
			t.Fatalf("Send failed: %v", err)
		}
		return dataBuf.String()
	}

	t.Run("non-ASCII Chinese subject is RFC 2047 encoded and symmetrically decodable", func(t *testing.T) {
		rawData := runMockSMTP(t, &model.Message{
			MsgType: model.MsgTypeText,
			Title:   "招商银行\r\nBcc: evil@example.com",
			Content: "验证码 123456",
		})

		parts := strings.SplitN(rawData, "\r\n\r\n", 2)
		headerBlock := parts[0]
		headerLines := strings.Split(headerBlock, "\r\n")

		var subjectVal string
		for _, line := range headerLines {
			if strings.HasPrefix(strings.ToLower(line), "bcc:") {
				t.Errorf("CRLF header injection succeeded! Found Bcc header line: %q", line)
			}
			if strings.HasPrefix(line, "Subject: ") {
				if subjectVal != "" {
					t.Errorf("multiple Subject lines found")
				}
				subjectVal = strings.TrimPrefix(line, "Subject: ")
			}
		}

		if subjectVal == "" {
			t.Fatalf("Subject header not found in:\n%s", headerBlock)
		}
		if !strings.Contains(strings.ToUpper(subjectVal), "=?UTF-8?Q?") {
			t.Errorf("expected RFC 2047 Q-encoded Subject, got: %q", subjectVal)
		}
		if strings.Contains(subjectVal, "招商银行") {
			t.Errorf("Subject must not contain raw unencoded Chinese characters: %q", subjectVal)
		}

		decoded, err := imap.DecodeSubject(subjectVal)
		if err != nil {
			t.Fatalf("imap.DecodeSubject failed: %v", err)
		}
		wantDecoded := "New Message From 招商银行Bcc: evil@example.com"
		if decoded != wantDecoded {
			t.Errorf("DecodeSubject = %q, want %q", decoded, wantDecoded)
		}
	})

	t.Run("pure ASCII subject with CRLF injection is sanitized", func(t *testing.T) {
		rawData := runMockSMTP(t, &model.Message{
			MsgType: model.MsgTypeText,
			Title:   "10086\r\nBcc: spy@evil.com",
			Content: "Test ASCII",
		})

		parts := strings.SplitN(rawData, "\r\n\r\n", 2)
		headerBlock := parts[0]
		for _, line := range strings.Split(headerBlock, "\r\n") {
			if strings.HasPrefix(strings.ToLower(line), "bcc:") {
				t.Errorf("CRLF injection found in ASCII header: %q", line)
			}
		}
		if !strings.Contains(headerBlock, "Subject: New Message From 10086Bcc: spy@evil.com\r\n") {
			t.Errorf("expected sanitized single-line ASCII subject, got:\n%s", headerBlock)
		}
	})

	t.Run("SMTP server hanging after TCP accept times out within deadline", func(t *testing.T) {
		cConn, sConn := net.Pipe()
		defer func() { _ = cConn.Close() }()
		defer func() { _ = sConn.Close() }()

		client := deliver.NewEmailDeliver(deliver.EmailOptions{
			SenderEmail:   "sender@example.com",
			ReceiverEmail: "receiver@example.com",
			SMTPServer:    "localhost",
			SMTPPort:      465,
			Timeout:       100 * time.Millisecond,
			CustomDialer: func(_ context.Context, _, _ string) (net.Conn, error) {
				return cConn, nil
			},
		})

		done := make(chan error, 1)
		start := time.Now()
		go func() {
			done <- client.Send(context.Background(), &model.Message{
				MsgType: model.MsgTypeText,
				Title:   "10086",
				Content: "timeout test",
			})
		}()

		select {
		case err := <-done:
			elapsed := time.Since(start)
			if err == nil {
				t.Fatal("expected timeout error when SMTP server hangs, got nil")
			}
			if elapsed > 300*time.Millisecond {
				t.Errorf("expected failure within 300ms, took %v", elapsed)
			}
		case <-time.After(300 * time.Millisecond):
			_ = cConn.Close()
			_ = sConn.Close()
			t.Fatal("emailClient.Send hung beyond 300ms when SMTP server did not send 220 banner")
		}
	})

	t.Run("SMTP command error closes connection immediately without hanging on deferred QUIT", func(t *testing.T) {
		cConn, sConn := net.Pipe()
		defer func() { _ = cConn.Close() }()
		defer func() { _ = sConn.Close() }()

		go func() {
			r := bufio.NewReader(sConn)
			w := bufio.NewWriter(sConn)
			writeLine := func(s string) {
				_, _ = w.WriteString(s)
				_ = w.Flush()
			}
			writeLine("220 smtp.example.com ESMTP\r\n")
			for {
				line, err := r.ReadString('\n')
				if err != nil {
					return
				}
				line = strings.TrimRight(line, "\r\n")
				switch {
				case strings.HasPrefix(line, "EHLO") || strings.HasPrefix(line, "HELO"):
					writeLine("250 smtp.example.com\r\n")
				case strings.HasPrefix(line, "MAIL FROM:"):
					writeLine("250 2.1.0 Ok\r\n")
				case strings.HasPrefix(line, "RCPT TO:"):
					writeLine("550 5.1.1 User unknown\r\n")
					// Simulate server that stops responding after rejecting RCPT TO
					time.Sleep(2 * time.Second)
					return
				}
			}
		}()

		client := deliver.NewEmailDeliver(deliver.EmailOptions{
			SenderEmail:   "sender@example.com",
			ReceiverEmail: "bad@example.com",
			SMTPServer:    "localhost",
			SMTPPort:      465,
			Timeout:       1 * time.Second,
			CustomDialer: func(_ context.Context, _, _ string) (net.Conn, error) {
				return cConn, nil
			},
		})

		start := time.Now()
		err := client.Send(context.Background(), &model.Message{
			MsgType: model.MsgTypeText,
			Title:   "10086",
			Content: "rcpt fail test",
		})
		elapsed := time.Since(start)
		if err == nil {
			t.Fatal("expected RCPT error, got nil")
		}
		if elapsed > 200*time.Millisecond {
			t.Fatalf("expected immediate error return (< 200ms) without blocking on deferred QUIT, but took %v", elapsed)
		}
	})
}

func TestBarkDeliver_URLsAndErrors(t *testing.T) {
	t.Run("post to custom URLs with ciphertext", func(t *testing.T) {
		aesKey := []byte("1234567890123456")
		var receivedBody map[string]interface{}
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &receivedBody)
			w.WriteHeader(http.StatusOK)
		}))
		defer ts.Close()

		bark := deliver.NewBarkDeliver(deliver.BarkOptions{
			URLs:      []string{ts.URL},
			CryptoKey: aesKey,
		})

		msg := &model.Message{
			MsgType: model.MsgTypeCall,
			Title:   "10010",
		}

		err := bark.Send(context.Background(), msg)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if _, ok := receivedBody["ciphertext"]; !ok {
			t.Errorf("expected ciphertext in url payload, got %v", receivedBody)
		}
		if iv, ok := receivedBody["iv"].(string); !ok || len(iv) != 16 {
			t.Errorf("expected a 16-byte `iv` alongside ciphertext in url payload, got %v", receivedBody["iv"])
		}
		if _, ok := receivedBody["device_key"]; ok {
			t.Errorf("expected no device_key for URL targets, got %v", receivedBody["device_key"])
		}
	})

	t.Run("http 500 error returns failure", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "internal server error", http.StatusInternalServerError)
		}))
		defer ts.Close()

		bark := deliver.NewBarkDeliver(deliver.BarkOptions{
			PostURL:    ts.URL,
			DeviceKeys: []string{"dev_fail"},
		})

		err := bark.Send(context.Background(), &model.Message{Title: "fail"})
		if err == nil {
			t.Fatal("expected error on 500, got nil")
		}
	})
}

func TestBarkDeliver_ConcurrentEndpoints(t *testing.T) {
	newSlowServer := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			select {
			case <-time.After(250 * time.Millisecond):
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"code":200}`))
			case <-r.Context().Done():
				return
			}
		}))
	}

	srv1 := newSlowServer()
	defer srv1.Close()
	srv2 := newSlowServer()
	defer srv2.Close()
	srv3 := newSlowServer()
	defer srv3.Close()
	srv4 := newSlowServer()
	defer srv4.Close()

	bark := deliver.NewBarkDeliver(deliver.BarkOptions{
		PostURL:    srv1.URL,
		DeviceKeys: []string{"dev_key_1", "dev_key_2"},
		URLs:       []string{srv2.URL, srv3.URL},
	})
	_ = srv4 // 4 slow endpoints total: 2 deviceKeys on srv1 + srv2 + srv3

	ctx, cancel := context.WithTimeout(context.Background(), 450*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := bark.Send(ctx, &model.Message{
		MsgType: model.MsgTypeText,
		Title:   "ConcurrentTest",
		Content: "Testing fan-out",
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("expected concurrent Bark fan-out to succeed within 450ms, got error after %v: %v", elapsed, err)
	}
	if elapsed >= 450*time.Millisecond {
		t.Errorf("expected elapsed < 450ms, took %v", elapsed)
	}
}

func TestNewDefaultHTTPClient_TransportTuning(t *testing.T) {
	client := deliver.NewDefaultHTTPClient(0)
	if client == nil {
		t.Fatal("expected non-nil http.Client")
	}
	if client.Timeout != 5*time.Second {
		t.Errorf("expected default timeout 5s, got %v", client.Timeout)
	}

	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport == nil {
		t.Fatalf("expected *http.Transport, got %T", client.Transport)
	}
	if !transport.ForceAttemptHTTP2 {
		t.Error("expected ForceAttemptHTTP2 to be true")
	}
	if transport.MaxIdleConns != 100 {
		t.Errorf("expected MaxIdleConns 100, got %d", transport.MaxIdleConns)
	}
	if transport.MaxIdleConnsPerHost != 10 {
		t.Errorf("expected MaxIdleConnsPerHost 10, got %d", transport.MaxIdleConnsPerHost)
	}
	if transport.IdleConnTimeout != 90*time.Second {
		t.Errorf("expected IdleConnTimeout 90s, got %v", transport.IdleConnTimeout)
	}
	if transport.TLSHandshakeTimeout != 5*time.Second {
		t.Errorf("expected TLSHandshakeTimeout 5s, got %v", transport.TLSHandshakeTimeout)
	}
	if transport.ExpectContinueTimeout != 1*time.Second {
		t.Errorf("expected ExpectContinueTimeout 1s, got %v", transport.ExpectContinueTimeout)
	}
}

type funcTarget struct {
	name string
	fn   func(ctx context.Context, msg *model.Message) error
}

func (f *funcTarget) Name() string { return f.name }
func (f *funcTarget) Send(ctx context.Context, msg *model.Message) error {
	return f.fn(ctx, msg)
}

func TestDispatcher_ConcurrentTargetMutationIsolation(t *testing.T) {
	mutator := &funcTarget{
		name: "mutator",
		fn: func(_ context.Context, msg *model.Message) error {
			for i := 0; i < 100; i++ {
				msg.Title = "MUTATED_TITLE"
				if msg.BatteryLevel != nil {
					*msg.BatteryLevel = 0
				}
			}
			return nil
		},
	}

	reader := &funcTarget{
		name: "reader",
		fn: func(_ context.Context, msg *model.Message) error {
			for i := 0; i < 100; i++ {
				if msg.Title != "OriginalSender" {
					t.Errorf("shared message Title was mutated to %q", msg.Title)
					return nil
				}
				if msg.BatteryLevel == nil || *msg.BatteryLevel != 88 {
					t.Errorf("shared message BatteryLevel was mutated to %v", msg.BatteryLevel)
					return nil
				}
				_ = msg.GenComplexText()
			}
			return nil
		},
	}

	dispatcher := deliver.NewDispatcher(deliver.DispatcherOptions{
		Timeout: 2 * time.Second,
		Targets: []deliver.Target{mutator, reader},
	})

	batt := 88
	origMsg := &model.Message{
		MsgType:      model.MsgTypeText,
		Title:        "OriginalSender",
		Content:      "Hello isolation",
		BatteryLevel: &batt,
	}

	if err := dispatcher.Deliver(context.Background(), origMsg); err != nil {
		t.Fatalf("Deliver failed: %v", err)
	}
}

func TestConsoleDeliver_CustomLogger(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	console := deliver.NewConsoleDeliver(logger)
	msg := &model.Message{
		MsgType: model.MsgTypeText,
		Title:   "CustomLoggerSender",
		Content: "Custom logger body",
	}

	if err := console.Send(context.Background(), msg); err != nil {
		t.Fatalf("ConsoleDeliver.Send failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "CustomLoggerSender") {
		t.Errorf("expected custom logger buffer to contain 'CustomLoggerSender', got: %q", output)
	}

	// Also verify Dispatcher and BarkDeliver custom logger injection
	var dispBuf bytes.Buffer
	dispLogger := slog.New(slog.NewJSONHandler(&dispBuf, nil))
	bark := deliver.NewBarkDeliver(deliver.BarkOptions{
		PostURL:    "http://127.0.0.1:1", // invalid port to trigger warn log
		DeviceKeys: []string{"dev_err"},
		Logger:     dispLogger,
	})
	disp := deliver.NewDispatcher(deliver.DispatcherOptions{
		Targets: []deliver.Target{bark},
		Timeout: 1 * time.Second,
		Logger:  dispLogger,
	})
	_ = disp.Deliver(context.Background(), msg)
	dispOut := dispBuf.String()
	if !strings.Contains(dispOut, "bark post to device_key failed") || !strings.Contains(dispOut, "target deliver failed") {
		t.Errorf("expected custom logger to capture Bark and Dispatcher warnings, got: %q", dispOut)
	}
}

func TestTelegramDeliver_Errors(t *testing.T) {
	t.Run("http 500 error returns failure", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "server error", http.StatusInternalServerError)
		}))
		defer ts.Close()

		tg := deliver.NewTelegramDeliver(deliver.TelegramOptions{
			BaseURL:  ts.URL,
			BotToken: "tok",
			ChatID:   1,
		})

		err := tg.Send(context.Background(), &model.Message{Title: "test"})
		if err == nil {
			t.Fatal("expected error on 500, got nil")
		}
	})

	// The Bot API contract is the JSON `ok` field; a proxy or a Bot API server may
	// return HTTP 200 with {"ok":false,...}. Ignoring that would report a successful
	// delivery for a message that was never sent.
	t.Run("HTTP 200 with ok:false is treated as a failure", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`)
		}))
		defer ts.Close()

		tg := deliver.NewTelegramDeliver(deliver.TelegramOptions{
			BaseURL:  ts.URL,
			BotToken: "tok",
			ChatID:   1,
		})

		err := tg.Send(context.Background(), &model.Message{Title: "test"})
		if err == nil {
			t.Fatal("expected error for ok:false, got nil")
		}
		if !strings.Contains(err.Error(), "chat not found") {
			t.Errorf("expected the Bot API description in the error, got %v", err)
		}
	})

	t.Run("HTTP 200 with ok:true succeeds", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":7}}`)
		}))
		defer ts.Close()

		tg := deliver.NewTelegramDeliver(deliver.TelegramOptions{
			BaseURL:  ts.URL,
			BotToken: "tok",
			ChatID:   1,
		})

		if err := tg.Send(context.Background(), &model.Message{Title: "test"}); err != nil {
			t.Fatalf("expected success for ok:true, got %v", err)
		}
	})
}

type endlessBody struct {
	readBytes int64
	maxTotal  int64
	closed    bool
}

func (e *endlessBody) Read(p []byte) (int, error) {
	if e.readBytes >= e.maxTotal {
		return 0, io.EOF
	}
	n := len(p)
	if int64(n) > e.maxTotal-e.readBytes {
		n = int(e.maxTotal - e.readBytes)
	}
	for i := 0; i < n; i++ {
		p[i] = 'x'
	}
	e.readBytes += int64(n)
	return n, nil
}

func (e *endlessBody) Close() error {
	e.closed = true
	return nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestDeliver_BoundedResponseDrain(t *testing.T) {
	const twoMB = 2 << 20
	const maxAllowedDrain = 64 << 10

	t.Run("BarkDeliver bounds response body drain to 64KB", func(t *testing.T) {
		body := &endlessBody{maxTotal: twoMB}
		client := &http.Client{
			Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       body,
					Header:     make(http.Header),
				}, nil
			}),
		}

		bark := deliver.NewBarkDeliver(deliver.BarkOptions{
			HTTPClient: client,
			PostURL:    "https://bark.example.com/push",
			DeviceKeys: []string{"dev1"},
		})

		if err := bark.Send(context.Background(), &model.Message{MsgType: model.MsgTypeText, Title: "T", Content: "C"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !body.closed {
			t.Error("expected response body to be closed")
		}
		if body.readBytes > maxAllowedDrain {
			t.Errorf("expected BarkDeliver to drain at most %d bytes, but drained %d bytes", maxAllowedDrain, body.readBytes)
		}
	})

	t.Run("TelegramDeliver bounds response body drain to 64KB", func(t *testing.T) {
		body := &endlessBody{maxTotal: twoMB}
		client := &http.Client{
			Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       body,
					Header:     make(http.Header),
				}, nil
			}),
		}

		tg := deliver.NewTelegramDeliver(deliver.TelegramOptions{
			HTTPClient: client,
			BaseURL:    "https://tg.example.com",
			BotToken:   "tok",
			ChatID:     123,
		})

		// The body is truncated at the drain limit, so it can no longer be a complete
		// Bot API document: the delivery is reported as unverified, but the read must
		// still be bounded and the body must still be released.
		err := tg.Send(context.Background(), &model.Message{MsgType: model.MsgTypeText, Title: "T", Content: "C"})
		if err == nil {
			t.Fatal("expected an unverified-delivery error for a truncated Bot API response, got nil")
		}
		if !body.closed {
			t.Error("expected response body to be closed")
		}
		if body.readBytes > maxAllowedDrain {
			t.Errorf("expected TelegramDeliver to drain at most %d bytes, but drained %d bytes", maxAllowedDrain, body.readBytes)
		}
	})
}

func TestBarkDeliver_SanitizeAndDeduplicateEndpoints(t *testing.T) {
	var requestedPaths []string
	var mu sync.Mutex
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requestedPaths = append(requestedPaths, r.URL.Path)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":200}`))
	}))
	defer ts.Close()

	bark := deliver.NewBarkDeliver(deliver.BarkOptions{
		PostURL:    ts.URL,
		DeviceKeys: []string{" key1 ", "key1", "", "   ", "key2"},
		URLs:       []string{ts.URL + "/custom1", "  ", ts.URL + "/custom1"},
	})

	err := bark.Send(context.Background(), &model.Message{Title: "dedup test"})
	if err != nil {
		t.Fatalf("expected Send to succeed, got %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	// Should have requested: /key1, /key2, /custom1 -> exactly 3 requests
	if len(requestedPaths) != 3 {
		t.Fatalf("expected exactly 3 requests (2 unique keys, 1 unique custom url), got %d: %v", len(requestedPaths), requestedPaths)
	}
}

func TestEmailDeliver_DataWriteError(t *testing.T) {
	cConn, sConn := net.Pipe()
	defer func() { _ = cConn.Close() }()
	defer func() { _ = sConn.Close() }()

	go func() {
		r := bufio.NewReader(sConn)
		w := bufio.NewWriter(sConn)
		writeLine := func(s string) {
			_, _ = w.WriteString(s)
			_ = w.Flush()
		}

		writeLine("220 smtp.example.com ESMTP\r\n")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			switch {
			case strings.HasPrefix(line, "EHLO") || strings.HasPrefix(line, "HELO"):
				writeLine("250 smtp.example.com\r\n")
			case strings.HasPrefix(line, "MAIL FROM:"):
				writeLine("250 2.1.0 Ok\r\n")
			case strings.HasPrefix(line, "RCPT TO:"):
				writeLine("250 2.1.5 Ok\r\n")
			case line == "DATA":
				writeLine("354 Start mail input\r\n")
				// Forcefully close serverConn to simulate I/O failure during DATA write
				_ = sConn.Close()
				return
			}
		}
	}()

	client := deliver.NewEmailDeliver(deliver.EmailOptions{
		SenderEmail:   "sender@example.com",
		ReceiverEmail: "receiver@example.com",
		SMTPServer:    "localhost",
		SMTPPort:      465,
		Timeout:       500 * time.Millisecond,
		CustomDialer: func(_ context.Context, _, _ string) (net.Conn, error) {
			return cConn, nil
		},
	})

	err := client.Send(context.Background(), &model.Message{
		MsgType: model.MsgTypeText,
		Title:   "10086",
		Content: "Simulate data write error",
	})
	if err == nil {
		t.Fatal("expected error on broken data pipe, got nil")
	}
}

func TestBarkDeliver_GroupStateMatrix(t *testing.T) {
	testCases := []struct {
		name          string
		msg           *model.Message
		expectedGroup string
		shouldHaveKey bool
	}{
		{
			name:          "text message",
			msg:           &model.Message{MsgType: model.MsgTypeText, Title: "10086", Content: "Hello"},
			expectedGroup: "text",
			shouldHaveKey: true,
		},
		{
			name:          "call message",
			msg:           &model.Message{MsgType: model.MsgTypeCall, Title: "13800000000"},
			expectedGroup: "call",
			shouldHaveKey: true,
		},
		{
			name:          "app notification with app_name",
			msg:           &model.Message{MsgType: model.MsgTypeAppNotification, AppName: " 微信 ", Content: "Hello"},
			expectedGroup: "微信",
			shouldHaveKey: true,
		},
		{
			name:          "app notification with empty app_name fallback",
			msg:           &model.Message{MsgType: model.MsgTypeAppNotification, AppName: "", Content: "Hello"},
			expectedGroup: "app_notification",
			shouldHaveKey: true,
		},
		{
			name:          "app notification with whitespace app_name fallback",
			msg:           &model.Message{MsgType: model.MsgTypeAppNotification, AppName: "   ", Content: "Hello"},
			expectedGroup: "app_notification",
			shouldHaveKey: true,
		},
		{
			name:          "empty msg_type and app_name: group key omitted",
			msg:           &model.Message{MsgType: "", AppName: ""},
			expectedGroup: "",
			shouldHaveKey: false,
		},
		{
			name:          "whitespace msg_type and app_name: group key omitted",
			msg:           &model.Message{MsgType: "   ", AppName: "   "},
			expectedGroup: "",
			shouldHaveKey: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var receivedBody map[string]interface{}
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, err := io.ReadAll(r.Body)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				_ = json.Unmarshal(b, &receivedBody)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"code": 200}`))
			}))
			defer ts.Close()

			bark := deliver.NewBarkDeliver(deliver.BarkOptions{
				PostURL:    ts.URL,
				DeviceKeys: []string{"dev_test"},
			})

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			err := bark.Send(ctx, tc.msg)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			val, exists := receivedBody["group"]
			if tc.shouldHaveKey {
				if !exists {
					t.Fatalf("expected 'group' key in payload, but it was missing")
				}
				if val != tc.expectedGroup {
					t.Fatalf("expected group %q, got %q", tc.expectedGroup, val)
				}
			} else if exists {
				t.Fatalf("expected 'group' key to be omitted, but got %v", val)
			}
		})
	}
}

func TestBarkDeliver_TitleFourStates(t *testing.T) {
	testCases := []struct {
		name          string
		appName       string
		title         string
		captcha       string
		emergency     bool
		expectedTitle string
		expectedCopy  string
	}{
		{
			name:          "state 1: full info (app_name and title) with padded captcha and emergency",
			appName:       " 微信 ",
			title:         " 张三 ",
			captcha:       " 889911 ",
			emergency:     true,
			expectedTitle: "[微信] 张三",
			expectedCopy:  "889911",
		},
		{
			name:          "state 2: app_name only with whitespace-only captcha",
			appName:       "微信",
			title:         "  ",
			captcha:       "   ",
			expectedTitle: "[微信]",
			expectedCopy:  "",
		},
		{
			name:          "state 3: title only",
			appName:       "",
			title:         "系统通知",
			expectedTitle: "New Notification From 系统通知",
		},
		{
			name:          "state 4: none",
			appName:       "  ",
			title:         "",
			expectedTitle: "New Notification",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var receivedBody map[string]interface{}
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(b, &receivedBody)
				w.WriteHeader(http.StatusOK)
			}))
			defer ts.Close()

			bark := deliver.NewBarkDeliver(deliver.BarkOptions{
				PostURL:    ts.URL,
				DeviceKeys: []string{"dev_test"},
			})

			msg := &model.Message{
				MsgType:     model.MsgTypeAppNotification,
				AppName:     tc.appName,
				Title:       tc.title,
				Content:     "Hello",
				CaptchaCode: tc.captcha,
				Emergency:   tc.emergency,
			}

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			err := bark.Send(ctx, msg)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if got := receivedBody["title"]; got != tc.expectedTitle {
				t.Fatalf("expected title %q, got %v", tc.expectedTitle, got)
			}
			if tc.expectedCopy != "" {
				if got := receivedBody["copy"]; got != tc.expectedCopy {
					t.Fatalf("expected copy %q, got %v", tc.expectedCopy, got)
				}
				if got := int(receivedBody["autoCopy"].(float64)); got != 1 {
					t.Fatalf("expected autoCopy 1, got %v", got)
				}
			} else {
				if _, exists := receivedBody["copy"]; exists {
					t.Fatalf("expected 'copy' key to be omitted for blank captcha, got %v", receivedBody["copy"])
				}
				if _, exists := receivedBody["autoCopy"]; exists {
					t.Fatalf("expected 'autoCopy' key to be omitted for blank captcha, got %v", receivedBody["autoCopy"])
				}
			}
			if tc.emergency {
				if receivedBody["level"] != "critical" || receivedBody["volume"] != "5" {
					t.Fatalf("expected emergency critical/5, got level=%v volume=%v", receivedBody["level"], receivedBody["volume"])
				}
			}
		})
	}
}

func TestBarkDeliver_AppNotificationEncryptedAES(t *testing.T) {
	aesKey := []byte("1234567890123456")
	var receivedBody map[string]interface{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = json.Unmarshal(b, &receivedBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	bark := deliver.NewBarkDeliver(deliver.BarkOptions{
		PostURL:    ts.URL,
		DeviceKeys: []string{"dev_aes"},
		CryptoKey:  aesKey,
	})

	msg := &model.Message{
		MsgType:     model.MsgTypeAppNotification,
		AppName:     "钉钉",
		Title:       "告警",
		Content:     "CPU 高",
		CaptchaCode: "654321",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := bark.Send(ctx, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cipherTextStr, ok := receivedBody["ciphertext"].(string)
	if !ok || cipherTextStr == "" {
		t.Fatalf("expected ciphertext in payload, got %v", receivedBody)
	}
	ivStr, ok := receivedBody["iv"].(string)
	if !ok || ivStr == "" {
		t.Fatalf("expected iv in payload, got %v", receivedBody)
	}

	decrypted, err := crypto.DecryptBark(crypto.BarkPayload{Ciphertext: cipherTextStr, IV: ivStr}, aesKey)
	if err != nil {
		t.Fatalf("failed to decrypt Bark payload: %v", err)
	}

	var innerPayload map[string]interface{}
	if err := json.Unmarshal(decrypted, &innerPayload); err != nil {
		t.Fatalf("failed to unmarshal decrypted JSON: %v", err)
	}

	if innerPayload["group"] != "钉钉" {
		t.Errorf("decrypted group mismatch: got %v, want '钉钉'", innerPayload["group"])
	}
	if innerPayload["title"] != "[钉钉] 告警" {
		t.Errorf("decrypted title mismatch: got %v, want '[钉钉] 告警'", innerPayload["title"])
	}
	if innerPayload["body"] != "CPU 高" {
		t.Errorf("decrypted body mismatch: got %v, want 'CPU 高'", innerPayload["body"])
	}
	if innerPayload["copy"] != "654321" {
		t.Errorf("decrypted copy mismatch: got %v, want '654321'", innerPayload["copy"])
	}
}

func TestEmailDeliver_SubjectFourStates(t *testing.T) {
	testCases := []struct {
		name            string
		appName         string
		title           string
		expectedSubject string
	}{
		{
			name:            "state 1: full info (app_name and title)",
			appName:         " 微信 ",
			title:           " 张三 ",
			expectedSubject: "[微信] 张三",
		},
		{
			name:            "state 2: app_name only",
			appName:         "微信",
			title:           "  ",
			expectedSubject: "[微信] New Notification",
		},
		{
			name:            "state 3: title only",
			appName:         "",
			title:           "系统通知",
			expectedSubject: "New Notification From 系统通知",
		},
		{
			name:            "state 4: none",
			appName:         "  ",
			title:           "",
			expectedSubject: "New Notification",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			clientConn, serverConn := net.Pipe()
			defer func() { _ = clientConn.Close() }()
			defer func() { _ = serverConn.Close() }()

			var receivedData strings.Builder
			go func() {
				r := bufio.NewReader(serverConn)
				w := bufio.NewWriter(serverConn)
				writeLine := func(s string) {
					_, _ = w.WriteString(s)
					_ = w.Flush()
				}
				writeLine("220 smtp.example.com ESMTP\r\n")
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					line = strings.TrimRight(line, "\r\n")
					switch {
					case strings.HasPrefix(line, "EHLO") || strings.HasPrefix(line, "HELO"):
						writeLine("250 smtp.example.com\r\n")
					case strings.HasPrefix(line, "MAIL FROM:"):
						writeLine("250 2.1.0 Ok\r\n")
					case strings.HasPrefix(line, "RCPT TO:"):
						writeLine("250 2.1.5 Ok\r\n")
					case line == "DATA":
						writeLine("354 End data\r\n")
						for {
							dataLine, err := r.ReadString('\n')
							if err != nil {
								return
							}
							if dataLine == ".\r\n" {
								writeLine("250 2.0.0 Ok: queued\r\n")
								break
							}
							receivedData.WriteString(dataLine)
						}
					case line == "QUIT":
						writeLine("221 2.0.0 Bye\r\n")
						return
					}
				}
			}()

			emailClient := deliver.NewEmailDeliver(deliver.EmailOptions{
				SenderEmail:   "sender@example.com",
				ReceiverEmail: "receiver@example.com",
				SMTPServer:    "localhost",
				SMTPPort:      465,
				CustomDialer: func(_ context.Context, _, _ string) (net.Conn, error) {
					return clientConn, nil
				},
			})

			msg := &model.Message{
				MsgType: model.MsgTypeAppNotification,
				AppName: tc.appName,
				Title:   tc.title,
				Content: "Test content",
			}

			err := emailClient.Send(context.Background(), msg)
			if err != nil {
				t.Fatalf("email Send failed: %v", err)
			}

			content := receivedData.String()
			lines := strings.Split(content, "\r\n")
			var subjectLine string
			for _, l := range lines {
				if strings.HasPrefix(l, "Subject: ") {
					subjectLine = strings.TrimPrefix(l, "Subject: ")
					break
				}
			}
			if subjectLine == "" {
				t.Fatalf("Subject header not found in:\n%s", content)
			}

			var dec mime.WordDecoder
			decodedSubject, err := dec.DecodeHeader(subjectLine)
			if err != nil {
				decodedSubject = subjectLine
			}
			if decodedSubject != tc.expectedSubject {
				t.Fatalf("Subject mismatch: got %q, want %q (raw header: %q)", decodedSubject, tc.expectedSubject, subjectLine)
			}
		})
	}
}

func TestConsoleDeliver_LogAppName(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	console := deliver.NewConsoleDeliver(logger)
	msg := &model.Message{
		MsgType: model.MsgTypeAppNotification,
		AppName: "企业微信",
		Title:   "工作汇报",
		Content: "今天的工作已完成",
	}

	err := console.Send(context.Background(), msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	logOutput := buf.String()
	if !strings.Contains(logOutput, "app_name=企业微信") {
		t.Fatalf("expected log to contain 'app_name=企业微信', got:\n%s", logOutput)
	}
	if !strings.Contains(logOutput, "msg_type=app_notification") {
		t.Fatalf("expected log to contain 'msg_type=app_notification', got:\n%s", logOutput)
	}
}
