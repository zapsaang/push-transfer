package imap_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"mime/multipart"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/zapsaang/push-transfer/internal/ingress/imap"
	"github.com/zapsaang/push-transfer/internal/model"
)

func TestParser_RFC2047Subject(t *testing.T) {
	t.Run("multiple encoded words in Subject (UTF-8 Base64)", func(t *testing.T) {
		// "PushTransfer " + "Email Trigger"
		raw := "=?utf-8?B?UHVzaFRyYW5zZmVyIA==?= =?utf-8?B?RW1haWwgVHJpZ2dlcg==?="
		decoded, err := imap.DecodeSubject(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := "PushTransfer Email Trigger"
		if decoded != expected {
			t.Errorf("expected %q, got %q", expected, decoded)
		}
		if !imap.IsTriggerSubject(decoded) {
			t.Errorf("expected %q to be recognized as trigger subject", decoded)
		}
	})

	t.Run("Q-encoding Subject", func(t *testing.T) {
		raw := "=?utf-8?Q?PushTransfer_Email_Trigger?="
		decoded, err := imap.DecodeSubject(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := "PushTransfer Email Trigger"
		if decoded != expected {
			t.Errorf("expected %q, got %q", expected, decoded)
		}
		if !imap.IsTriggerSubject(decoded) {
			t.Errorf("expected %q to be recognized as trigger subject", decoded)
		}
	})

	t.Run("GBK encoded Subject", func(t *testing.T) {
		// "=?GBK?B?tPq/qr3j?=" -> GBK bytes for "测试邮件"
		raw := "=?GBK?B?yrnTw0VtYWlsIFRyaWdnZXI=?=" // "转发Email Trigger" in GBK
		decoded, err := imap.DecodeSubject(raw)
		if err != nil {
			t.Fatalf("unexpected error decoding GBK: %v", err)
		}
		if !strings.Contains(decoded, "Email Trigger") {
			t.Errorf("expected decoded subject to contain 'Email Trigger', got %q", decoded)
		}
	})

	t.Run("empty subject", func(t *testing.T) {
		decoded, err := imap.DecodeSubject("")
		if err != nil {
			t.Fatalf("unexpected error on empty subject: %v", err)
		}
		if decoded != "" {
			t.Errorf("expected empty string, got %q", decoded)
		}
		if imap.IsTriggerSubject(decoded) {
			t.Error("empty subject should not match trigger subject")
		}
	})

	t.Run("ignore non-matching subjects", func(t *testing.T) {
		subjects := []string{
			"Hello World",
			"Weekly Newsletter",
			"PushTransfer Other Trigger",
			"pushtransfer email trigger", // matching is case sensitive: 'PushTransfer Email Trigger'
		}
		for _, s := range subjects {
			decoded, err := imap.DecodeSubject(s)
			if err != nil {
				t.Fatalf("unexpected error on subject %q: %v", s, err)
			}
			if imap.IsTriggerSubject(decoded) {
				t.Errorf("subject %q should be ignored, but matched trigger", s)
			}
		}
	})
}

func TestParser_MIMEBodyExtraction(t *testing.T) {
	t.Run("multipart/mixed with plain text and attachment", func(t *testing.T) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)

		// 1. Plain text inline part
		textHeader := make(textproto.MIMEHeader)
		textHeader.Set("Content-Type", "text/plain; charset=utf-8")
		textHeader.Set("Content-Disposition", "inline")
		pw, err := mw.CreatePart(textHeader)
		if err != nil {
			t.Fatalf("failed to create text part: %v", err)
		}
		expectedBody := `{"msg_type":"text","title":"10086","content":"mixed body"}`
		_, _ = pw.Write([]byte(expectedBody))

		// 2. Attachment part (should be ignored)
		attHeader := make(textproto.MIMEHeader)
		attHeader.Set("Content-Type", "text/plain; charset=utf-8")
		attHeader.Set("Content-Disposition", `attachment; filename="data.txt"`)
		aw, err := mw.CreatePart(attHeader)
		if err != nil {
			t.Fatalf("failed to create attachment part: %v", err)
		}
		_, _ = aw.Write([]byte("ignore this attachment content"))

		_ = mw.Close()

		rawMsg := "Subject: PushTransfer Email Trigger\r\n" +
			"MIME-Version: 1.0\r\n" +
			"Content-Type: multipart/mixed; boundary=" + mw.Boundary() + "\r\n\r\n" +
			buf.String()

		msg, ok, err := imap.ParseEmail(strings.NewReader(rawMsg))
		if err != nil {
			t.Fatalf("failed to parse email: %v", err)
		}
		if !ok {
			t.Fatal("expected email to be processed, but was ignored")
		}
		if msg == nil {
			t.Fatal("expected non-nil message")
		}
		if msg.Content != "mixed body" {
			t.Errorf("expected msg.Content 'mixed body', got %q", msg.Content)
		}
	})

	t.Run("multipart/alternative with plain text and html", func(t *testing.T) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)

		// Plain text part
		textHeader := make(textproto.MIMEHeader)
		textHeader.Set("Content-Type", "text/plain; charset=utf-8")
		pw, err := mw.CreatePart(textHeader)
		if err != nil {
			t.Fatalf("failed to create text part: %v", err)
		}
		expectedBody := `{"msg_type":"text","title":"10010","content":"alternative body"}`
		_, _ = pw.Write([]byte(expectedBody))

		// HTML part
		htmlHeader := make(textproto.MIMEHeader)
		htmlHeader.Set("Content-Type", "text/html; charset=utf-8")
		hw, err := mw.CreatePart(htmlHeader)
		if err != nil {
			t.Fatalf("failed to create html part: %v", err)
		}
		_, _ = hw.Write([]byte("<html><body>HTML content</body></html>"))

		_ = mw.Close()

		rawMsg := "Subject: PushTransfer Email Trigger\r\n" +
			"MIME-Version: 1.0\r\n" +
			"Content-Type: multipart/alternative; boundary=" + mw.Boundary() + "\r\n\r\n" +
			buf.String()

		msg, ok, err := imap.ParseEmail(strings.NewReader(rawMsg))
		if err != nil {
			t.Fatalf("failed to parse alternative email: %v", err)
		}
		if !ok {
			t.Fatal("expected email to be processed")
		}
		if msg.Content != "alternative body" {
			t.Errorf("expected msg.Content 'alternative body', got %q", msg.Content)
		}
	})

	t.Run("non-multipart plain email", func(t *testing.T) {
		rawMsg := "Subject: PushTransfer Email Trigger\r\n" +
			"Content-Type: text/plain; charset=utf-8\r\n\r\n" +
			`{"msg_type":"call","title":"13800138000"}`

		msg, ok, err := imap.ParseEmail(strings.NewReader(rawMsg))
		if err != nil {
			t.Fatalf("failed to parse non-multipart email: %v", err)
		}
		if !ok {
			t.Fatal("expected email to be processed")
		}
		if msg.MsgType != model.MsgTypeCall {
			t.Errorf("expected msg_type %q, got %q", model.MsgTypeCall, msg.MsgType)
		}
		if msg.Title != "13800138000" {
			t.Errorf("expected title '13800138000', got %q", msg.Title)
		}
	})

	t.Run("app_notification plain email", func(t *testing.T) {
		rawMsg := "Subject: PushTransfer Email Trigger\r\n" +
			"Content-Type: text/plain; charset=utf-8\r\n\r\n" +
			`{"msg_type":"app_notification","app_name":"钉钉","title":"审批提醒","content":"您有一条新的请假审批"}`

		msg, ok, err := imap.ParseEmail(strings.NewReader(rawMsg))
		if err != nil {
			t.Fatalf("failed to parse app_notification email: %v", err)
		}
		if !ok {
			t.Fatal("expected email to be processed")
		}
		if msg.MsgType != model.MsgTypeAppNotification {
			t.Errorf("expected msg_type 'app_notification', got %q", msg.MsgType)
		}
		if msg.AppName != "钉钉" {
			t.Errorf("expected app_name '钉钉', got %q", msg.AppName)
		}
	})
}

func TestListener_Backoff(t *testing.T) {
	minDuration := 1 * time.Second
	maxDuration := 60 * time.Second

	// Verify exponential progression and bounds
	prevDuration := time.Duration(0)
	for attempt := 0; attempt < 8; attempt++ {
		b := imap.CalculateBackoff(attempt, minDuration, maxDuration)

		// Must stay within [minDuration * 0.75, maxDuration * 1.25]
		if b < minDuration/2 {
			t.Errorf("attempt %d backoff %v is below minimum", attempt, b)
		}
		if b > maxDuration+5*time.Second {
			t.Errorf("attempt %d backoff %v exceeds maximum limit", attempt, b)
		}

		if attempt > 0 && attempt < 5 {
			// Without jitter, 2^(n-1) increases exponentially
			// With jitter (±20%), duration should generally trend upward
			if b < prevDuration/2 {
				t.Errorf("attempt %d backoff %v unexpectedly decreased from %v", attempt, b, prevDuration)
			}
		}
		prevDuration = b
	}

	// For large attempt numbers, backoff must be capped around max
	largeBackoff := imap.CalculateBackoff(20, minDuration, maxDuration)
	if largeBackoff > maxDuration+10*time.Second {
		t.Errorf("large attempt backoff %v not capped to max %v", largeBackoff, maxDuration)
	}
}

func TestListener_GracefulShutdown(t *testing.T) {
	listener := imap.NewListener(imap.ListenerOptions{
		IMAPServer: "127.0.0.1",
		IMAPPort:   9993,
		LoginEmail: "test@example.com",
		Password:   "secret",
	})

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately to verify graceful exit without hang
	cancel()

	err := listener.Start(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Errorf("expected nil or context.Canceled on graceful shutdown, got: %v", err)
	}
}

func TestExtractTextPlain(t *testing.T) {
	t.Run("valid plain text", func(t *testing.T) {
		raw := "Content-Type: text/plain; charset=utf-8\r\n\r\nHello World Plain"
		content, err := imap.ExtractTextPlain(strings.NewReader(raw))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if content != "Hello World Plain" {
			t.Errorf("expected 'Hello World Plain', got %q", content)
		}
	})

	t.Run("only attachment without inline body", func(t *testing.T) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		attHeader := make(textproto.MIMEHeader)
		attHeader.Set("Content-Type", "text/plain")
		attHeader.Set("Content-Disposition", `attachment; filename="file.txt"`)
		pw, _ := mw.CreatePart(attHeader)
		_, _ = pw.Write([]byte("attachment content"))
		_ = mw.Close()

		rawMsg := "MIME-Version: 1.0\r\n" +
			"Content-Type: multipart/mixed; boundary=" + mw.Boundary() + "\r\n\r\n" +
			buf.String()

		_, err := imap.ExtractTextPlain(strings.NewReader(rawMsg))
		if err == nil {
			t.Fatal("expected error when no inline text part is present, got nil")
		}
	})
}

func TestParseEmail_Errors(t *testing.T) {
	t.Run("invalid JSON body", func(t *testing.T) {
		raw := "Subject: PushTransfer Email Trigger\r\n" +
			"Content-Type: text/plain\r\n\r\n" +
			"not a json"
		_, _, err := imap.ParseEmail(strings.NewReader(raw))
		if err == nil {
			t.Fatal("expected error parsing invalid json, got nil")
		}
	})

	t.Run("corrupted mail header", func(t *testing.T) {
		raw := ":::invalid"
		_, _, err := imap.ParseEmail(strings.NewReader(raw))
		if err == nil {
			t.Log("parsed without error")
		}
	})

	t.Run("missing msg_type in trigger email returns error", func(t *testing.T) {
		raw := "Subject: PushTransfer Email Trigger\r\n" +
			"Content-Type: text/plain\r\n\r\n" +
			`{"title":"no msg_type"}`
		_, _, err := imap.ParseEmail(strings.NewReader(raw))
		if err == nil {
			t.Fatal("expected error parsing email with missing msg_type, got nil")
		}
	})

	t.Run("invalid msg_type in trigger email returns error", func(t *testing.T) {
		raw := "Subject: PushTransfer Email Trigger\r\n" +
			"Content-Type: text/plain\r\n\r\n" +
			`{"msg_type":"invalid_type","title":"bad"}`
		_, _, err := imap.ParseEmail(strings.NewReader(raw))
		if err == nil {
			t.Fatal("expected error parsing email with invalid msg_type, got nil")
		}
	})
}

func TestListener_UIDHandlingAndIdleWait(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer func() { _ = clientConn.Close() }()
	defer func() { _ = serverConn.Close() }()

	var (
		mu              sync.Mutex
		commands        []string
		receivedUIDs    []string
		idleWaitChecked bool
		handlerCalled   int32
	)

	recordCmd := func(cmd string) {
		mu.Lock()
		defer mu.Unlock()
		commands = append(commands, cmd)
	}

	const rawEmail = "Subject: PushTransfer Email Trigger\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\n" +
		`{"msg_type":"text","title":"10086","content":"uid test body"}`

	go func() {
		r := bufio.NewReader(serverConn)
		w := bufio.NewWriter(serverConn)
		writeLine := func(s string) {
			_, _ = w.WriteString(s)
			_ = w.Flush()
		}

		// 1. Initial greeting with IDLE capability
		writeLine("* OK [CAPABILITY IMAP4rev1 IDLE] Mock IMAP Server Ready\r\n")

		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			parts := strings.SplitN(line, " ", 2)
			if len(parts) < 2 {
				continue
			}
			tag := parts[0]
			cmd := parts[1]
			upperCmd := strings.ToUpper(cmd)
			recordCmd(upperCmd)

			switch {
			case strings.HasPrefix(upperCmd, "CAPABILITY"):
				writeLine("* CAPABILITY IMAP4rev1 IDLE\r\n" + tag + " OK CAPABILITY completed\r\n")

			case strings.HasPrefix(upperCmd, "LOGIN"):
				writeLine(tag + " OK LOGIN completed\r\n")

			case strings.HasPrefix(upperCmd, "SELECT"):
				writeLine("* 1 EXISTS\r\n* 0 RECENT\r\n* OK [UIDVALIDITY 1] UIDs valid\r\n* FLAGS (\\Seen)\r\n* OK [PERMANENTFLAGS (\\Seen)] Flags permitted\r\n" + tag + " OK [READ-WRITE] SELECT completed\r\n")

			case strings.HasPrefix(upperCmd, "UID SEARCH"):
				// Return UID 2048 on initial search
				mu.Lock()
				if len(receivedUIDs) == 0 {
					receivedUIDs = append(receivedUIDs, "2048")
					mu.Unlock()
					writeLine("* SEARCH 2048\r\n" + tag + " OK UID SEARCH completed\r\n")
				} else {
					mu.Unlock()
					writeLine("* SEARCH\r\n" + tag + " OK UID SEARCH completed\r\n")
				}

			case strings.HasPrefix(upperCmd, "UID FETCH"):
				// Return literal containing rawEmail without extra trailing \r\n before closing paren
				fetchResp := fmt.Sprintf("* 1 FETCH (UID 2048 BODY[] {%d}\r\n%s)\r\n%s OK UID FETCH completed\r\n",
					len(rawEmail), rawEmail, tag)
				writeLine(fetchResp)

			case strings.HasPrefix(upperCmd, "UID STORE"):
				writeLine(tag + " OK UID STORE completed\r\n")

			case strings.HasPrefix(upperCmd, "IDLE"):
				// Enter IDLE: send continuation
				writeLine("+ idling\r\n")
				// Trigger unilateral data
				writeLine("* 2 EXISTS\r\n")

				// Expect client to send DONE\r\n
				doneLine, errDone := r.ReadString('\n')
				if errDone != nil {
					return
				}
				doneLine = strings.TrimRight(doneLine, "\r\n")
				if doneLine == "DONE" {
					// Verify client waits for tagged OK before sending next command
					time.Sleep(20 * time.Millisecond)
					mu.Lock()
					idleWaitChecked = true
					mu.Unlock()

					// Complete IDLE
					writeLine(tag + " OK IDLE completed\r\n")
				}

			case strings.HasPrefix(upperCmd, "LOGOUT"):
				writeLine("* BYE Logging out\r\n" + tag + " OK LOGOUT completed\r\n")
				return
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	listener := imap.NewListener(imap.ListenerOptions{
		IMAPServer: "127.0.0.1",
		IMAPPort:   9993,
		LoginEmail: "test@example.com",
		Password:   "secret",
		DialConn: func(_ context.Context, _ string) (net.Conn, error) {
			return clientConn, nil
		},
		Handler: func(_ context.Context, msg *model.Message) error {
			atomic.AddInt32(&handlerCalled, 1)
			if msg.Content != "uid test body" {
				t.Errorf("expected msg content 'uid test body', got %q", msg.Content)
			}
			return nil
		},
	})

	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()

	_ = listener.Start(ctx)

	if atomic.LoadInt32(&handlerCalled) != 1 {
		t.Errorf("expected handler to be called once for UID 2048 email, got %d", handlerCalled)
	}

	mu.Lock()
	defer mu.Unlock()

	var hasUIDSearch, hasUIDFetch, hasUIDStore bool
	for _, cmd := range commands {
		if strings.HasPrefix(cmd, "UID SEARCH") {
			hasUIDSearch = true
		}
		if strings.HasPrefix(cmd, "UID FETCH 2048") {
			hasUIDFetch = true
		}
		if strings.HasPrefix(cmd, "UID STORE 2048") {
			hasUIDStore = true
		}
	}

	if !hasUIDSearch {
		t.Error("expected UID SEARCH to be executed")
	}
	if !hasUIDFetch {
		t.Error("expected UID FETCH 2048 to be executed")
	}
	if !hasUIDStore {
		t.Error("expected UID STORE 2048 to be executed")
	}
	if !idleWaitChecked {
		t.Error("expected IDLE Wait to be checked during DONE sequence")
	}
}

func TestListener_FallbackPollingWhenNoIdle(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer func() { _ = clientConn.Close() }()
	defer func() { _ = serverConn.Close() }()

	var (
		mu           sync.Mutex
		commands     []string
		noopReceived int32
	)

	go func() {
		r := bufio.NewReader(serverConn)
		w := bufio.NewWriter(serverConn)
		writeLine := func(s string) {
			_, _ = w.WriteString(s)
			_ = w.Flush()
		}

		// Greeting without IDLE capability
		writeLine("* OK [CAPABILITY IMAP4rev1] Mock IMAP Server Ready\r\n")

		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			parts := strings.SplitN(line, " ", 2)
			if len(parts) < 2 {
				continue
			}
			tag := parts[0]
			cmd := parts[1]
			upperCmd := strings.ToUpper(cmd)

			mu.Lock()
			commands = append(commands, upperCmd)
			mu.Unlock()

			switch {
			case strings.HasPrefix(upperCmd, "CAPABILITY"):
				writeLine("* CAPABILITY IMAP4rev1\r\n" + tag + " OK CAPABILITY completed\r\n")

			case strings.HasPrefix(upperCmd, "LOGIN"):
				writeLine(tag + " OK LOGIN completed\r\n")

			case strings.HasPrefix(upperCmd, "SELECT"):
				writeLine("* 0 EXISTS\r\n* 0 RECENT\r\n* OK [UIDVALIDITY 1] UIDs valid\r\n* FLAGS (\\Seen)\r\n* OK [PERMANENTFLAGS (\\Seen)] Flags permitted\r\n" + tag + " OK [READ-WRITE] SELECT completed\r\n")

			case strings.HasPrefix(upperCmd, "UID SEARCH"):
				writeLine("* SEARCH\r\n" + tag + " OK UID SEARCH completed\r\n")

			case strings.HasPrefix(upperCmd, "NOOP"):
				atomic.AddInt32(&noopReceived, 1)
				writeLine(tag + " OK NOOP completed\r\n")

			case strings.HasPrefix(upperCmd, "LOGOUT"):
				writeLine("* BYE Logging out\r\n" + tag + " OK LOGOUT completed\r\n")
				return
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	listener := imap.NewListener(imap.ListenerOptions{
		IMAPServer:   "127.0.0.1",
		IMAPPort:     9993,
		LoginEmail:   "test@example.com",
		Password:     "secret",
		PollInterval: 30 * time.Millisecond,
		DialConn: func(_ context.Context, _ string) (net.Conn, error) {
			return clientConn, nil
		},
	})

	go func() {
		// Wait for a few polling intervals then cancel
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()

	_ = listener.Start(ctx)

	if atomic.LoadInt32(&noopReceived) < 1 {
		t.Errorf("expected at least 1 NOOP during fallback polling, got %d", noopReceived)
	}

	mu.Lock()
	defer mu.Unlock()

	for _, cmd := range commands {
		if strings.HasPrefix(cmd, "IDLE") {
			t.Errorf("server without IDLE capability should not receive IDLE command, but got: %s", cmd)
		}
	}
}

func TestListener_DialErrorAndBackoff(t *testing.T) {
	var dialAttempts int32
	listener := imap.NewListener(imap.ListenerOptions{
		IMAPServer: "127.0.0.1",
		IMAPPort:   1993,
		LoginEmail: "test@example.com",
		Password:   "secret",
		MinBackoff: 10 * time.Millisecond,
		MaxBackoff: 50 * time.Millisecond,
		DialClient: func(_ context.Context, _ string) (*imapclient.Client, error) {
			atomic.AddInt32(&dialAttempts, 1)
			return nil, errors.New("connection refused")
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_ = listener.Start(ctx)
	if atomic.LoadInt32(&dialAttempts) < 2 {
		t.Errorf("expected at least 2 dial attempts during retry backoff, got %d", dialAttempts)
	}
}

func TestListener_ConsumptionSemanticsAndRetry(t *testing.T) {
	t.Run("handler error keeps email UNSEEN and retries in-session via backoff timer without disconnecting", func(t *testing.T) {
		clientConn, serverConn := net.Pipe()
		defer func() { _ = clientConn.Close() }()
		defer func() { _ = serverConn.Close() }()

		var (
			mu                       sync.Mutex
			uid3001Seen              bool
			storeBeforeSecondAttempt bool
			handlerAttempts          int32
			dialCalls                int32
			idleCount                int32
			storeDone                = make(chan struct{})
		)

		const rawEmail = "Subject: PushTransfer Email Trigger\r\n" +
			"Content-Type: text/plain; charset=utf-8\r\n\r\n" +
			`{"msg_type":"text","title":"10086","content":"retry me"}`

		go func() {
			r := bufio.NewReader(serverConn)
			w := bufio.NewWriter(serverConn)
			writeLine := func(s string) {
				_, _ = w.WriteString(s)
				_ = w.Flush()
			}

			writeLine("* OK [CAPABILITY IMAP4rev1 IDLE] Mock IMAP Server Ready\r\n")

			for {
				line, err := r.ReadString('\n')
				if err != nil {
					return
				}
				line = strings.TrimRight(line, "\r\n")
				parts := strings.SplitN(line, " ", 2)
				if len(parts) < 2 {
					continue
				}
				tag := parts[0]
				cmd := parts[1]
				upperCmd := strings.ToUpper(cmd)

				switch {
				case strings.HasPrefix(upperCmd, "CAPABILITY"):
					writeLine("* CAPABILITY IMAP4rev1 IDLE\r\n" + tag + " OK CAPABILITY completed\r\n")

				case strings.HasPrefix(upperCmd, "LOGIN"):
					writeLine(tag + " OK LOGIN completed\r\n")

				case strings.HasPrefix(upperCmd, "SELECT"):
					writeLine("* 1 EXISTS\r\n* 0 RECENT\r\n* OK [UIDVALIDITY 1] UIDs valid\r\n* FLAGS (\\Seen)\r\n* OK [PERMANENTFLAGS (\\Seen)] Flags permitted\r\n" + tag + " OK [READ-WRITE] SELECT completed\r\n")

				case strings.HasPrefix(upperCmd, "UID SEARCH"):
					mu.Lock()
					seen := uid3001Seen
					mu.Unlock()
					if !seen {
						writeLine("* SEARCH 3001\r\n" + tag + " OK UID SEARCH completed\r\n")
					} else {
						writeLine("* SEARCH\r\n" + tag + " OK UID SEARCH completed\r\n")
					}

				case strings.HasPrefix(upperCmd, "UID FETCH"):
					fetchResp := fmt.Sprintf("* 1 FETCH (UID 3001 BODY[] {%d}\r\n%s)\r\n%s OK UID FETCH completed\r\n",
						len(rawEmail), rawEmail, tag)
					writeLine(fetchResp)

				case strings.HasPrefix(upperCmd, "UID STORE"):
					mu.Lock()
					uid3001Seen = true
					if atomic.LoadInt32(&handlerAttempts) < 2 {
						storeBeforeSecondAttempt = true
					}
					mu.Unlock()
					writeLine(tag + " OK UID STORE completed\r\n")
					select {
					case <-storeDone:
					default:
						close(storeDone)
					}

				case strings.HasPrefix(upperCmd, "IDLE"):
					atomic.AddInt32(&idleCount, 1)
					writeLine("+ idling\r\n")
					doneLine, errDone := r.ReadString('\n')
					if errDone != nil {
						return
					}
					if strings.TrimRight(doneLine, "\r\n") == "DONE" {
						writeLine(tag + " OK IDLE completed\r\n")
					}

				case strings.HasPrefix(upperCmd, "LOGOUT"):
					writeLine("* BYE Logging out\r\n" + tag + " OK LOGOUT completed\r\n")
					return
				}
			}
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()

		listener := imap.NewListener(imap.ListenerOptions{
			IMAPServer:        "127.0.0.1",
			IMAPPort:          9993,
			LoginEmail:        "test@example.com",
			Password:          "secret",
			MinBackoff:        30 * time.Millisecond,
			MaxBackoff:        100 * time.Millisecond,
			HeartbeatInterval: 10 * time.Minute,
			DialConn: func(_ context.Context, _ string) (net.Conn, error) {
				if atomic.AddInt32(&dialCalls, 1) > 1 {
					return nil, errors.New("unexpected second dial; session should not disconnect on handler error")
				}
				return clientConn, nil
			},
			Handler: func(_ context.Context, _ *model.Message) error {
				attempt := atomic.AddInt32(&handlerAttempts, 1)
				if attempt == 1 {
					return errors.New("downstream 502")
				}
				return nil
			},
		})

		go func() {
			select {
			case <-storeDone:
				// If UID STORE happened after 2nd attempt, cancel immediately
				if atomic.LoadInt32(&handlerAttempts) >= 2 {
					time.Sleep(10 * time.Millisecond)
					cancel()
				}
			case <-ctx.Done():
			}
		}()

		_ = listener.Start(ctx)

		mu.Lock()
		seen := uid3001Seen
		prematureStore := storeBeforeSecondAttempt
		mu.Unlock()

		if prematureStore {
			t.Error("email was marked SEEN on first failed handler attempt; expected it to remain UNSEEN")
		}
		if got := atomic.LoadInt32(&handlerAttempts); got != 2 {
			t.Errorf("expected handler to be called 2 times (initial + 1 in-session retry), got %d", got)
		}
		if !seen {
			t.Error("expected email UID 3001 to be marked SEEN after second handler attempt succeeded")
		}
		if got := atomic.LoadInt32(&dialCalls); got != 1 {
			t.Errorf("expected exactly 1 DialConn call (in-session retry without disconnect), got %d", got)
		}
	})

	t.Run("malformed JSON poison email is marked SEEN immediately", func(t *testing.T) {
		clientConn, serverConn := net.Pipe()
		defer func() { _ = clientConn.Close() }()
		defer func() { _ = serverConn.Close() }()

		var (
			mu            sync.Mutex
			uid3002Seen   bool
			handlerCalled int32
			storeDone     = make(chan struct{})
		)

		const poisonEmail = "Subject: PushTransfer Email Trigger\r\n" +
			"Content-Type: text/plain; charset=utf-8\r\n\r\n" +
			`{bad-json`

		go func() {
			r := bufio.NewReader(serverConn)
			w := bufio.NewWriter(serverConn)
			writeLine := func(s string) {
				_, _ = w.WriteString(s)
				_ = w.Flush()
			}

			writeLine("* OK [CAPABILITY IMAP4rev1 IDLE] Mock IMAP Server Ready\r\n")

			for {
				line, err := r.ReadString('\n')
				if err != nil {
					return
				}
				line = strings.TrimRight(line, "\r\n")
				parts := strings.SplitN(line, " ", 2)
				if len(parts) < 2 {
					continue
				}
				tag := parts[0]
				cmd := parts[1]
				upperCmd := strings.ToUpper(cmd)

				switch {
				case strings.HasPrefix(upperCmd, "CAPABILITY"):
					writeLine("* CAPABILITY IMAP4rev1 IDLE\r\n" + tag + " OK CAPABILITY completed\r\n")
				case strings.HasPrefix(upperCmd, "LOGIN"):
					writeLine(tag + " OK LOGIN completed\r\n")
				case strings.HasPrefix(upperCmd, "SELECT"):
					writeLine("* 1 EXISTS\r\n* 0 RECENT\r\n* OK [UIDVALIDITY 1] UIDs valid\r\n* FLAGS (\\Seen)\r\n* OK [PERMANENTFLAGS (\\Seen)] Flags permitted\r\n" + tag + " OK [READ-WRITE] SELECT completed\r\n")
				case strings.HasPrefix(upperCmd, "UID SEARCH"):
					mu.Lock()
					seen := uid3002Seen
					mu.Unlock()
					if !seen {
						writeLine("* SEARCH 3002\r\n" + tag + " OK UID SEARCH completed\r\n")
					} else {
						writeLine("* SEARCH\r\n" + tag + " OK UID SEARCH completed\r\n")
					}
				case strings.HasPrefix(upperCmd, "UID FETCH"):
					fetchResp := fmt.Sprintf("* 1 FETCH (UID 3002 BODY[] {%d}\r\n%s)\r\n%s OK UID FETCH completed\r\n",
						len(poisonEmail), poisonEmail, tag)
					writeLine(fetchResp)
				case strings.HasPrefix(upperCmd, "UID STORE"):
					mu.Lock()
					uid3002Seen = true
					mu.Unlock()
					writeLine(tag + " OK UID STORE completed\r\n")
					select {
					case <-storeDone:
					default:
						close(storeDone)
					}
				case strings.HasPrefix(upperCmd, "IDLE"):
					writeLine("+ idling\r\n")
					doneLine, errDone := r.ReadString('\n')
					if errDone != nil {
						return
					}
					if strings.TrimRight(doneLine, "\r\n") == "DONE" {
						writeLine(tag + " OK IDLE completed\r\n")
					}
				case strings.HasPrefix(upperCmd, "LOGOUT"):
					writeLine("* BYE Logging out\r\n" + tag + " OK LOGOUT completed\r\n")
					return
				}
			}
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()

		var logBuf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logBuf, nil))

		listener := imap.NewListener(imap.ListenerOptions{
			IMAPServer: "127.0.0.1",
			IMAPPort:   9993,
			LoginEmail: "test@example.com",
			Password:   "secret",
			Logger:     logger,
			DialConn: func(_ context.Context, _ string) (net.Conn, error) {
				return clientConn, nil
			},
			Handler: func(_ context.Context, _ *model.Message) error {
				atomic.AddInt32(&handlerCalled, 1)
				return nil
			},
		})

		go func() {
			select {
			case <-storeDone:
				time.Sleep(10 * time.Millisecond)
				cancel()
			case <-ctx.Done():
			}
		}()

		_ = listener.Start(ctx)

		if got := atomic.LoadInt32(&handlerCalled); got != 0 {
			t.Errorf("expected handler not to be called for poison email, got %d", got)
		}
		mu.Lock()
		seen := uid3002Seen
		mu.Unlock()
		if !seen {
			t.Error("expected malformed poison email UID 3002 to be marked SEEN immediately")
		}
		if !strings.Contains(logBuf.String(), "discarding malformed trigger email (poison pill)") {
			t.Errorf("expected poison pill error log, got logs: %s", logBuf.String())
		}
	})

	t.Run("trigger email with missing or invalid msg_type is marked SEEN immediately as poison pill", func(t *testing.T) {
		clientConn, serverConn := net.Pipe()
		defer func() { _ = clientConn.Close() }()
		defer func() { _ = serverConn.Close() }()

		var (
			mu            sync.Mutex
			uid3005Seen   bool
			handlerCalled int32
			storeDone     = make(chan struct{})
		)

		const invalidMsgTypeEmail = "Subject: PushTransfer Email Trigger\r\n" +
			"Content-Type: text/plain; charset=utf-8\r\n\r\n" +
			`{"title":"no_type","content":"missing msg_type"}`

		go func() {
			r := bufio.NewReader(serverConn)
			w := bufio.NewWriter(serverConn)
			writeLine := func(s string) {
				_, _ = w.WriteString(s)
				_ = w.Flush()
			}

			writeLine("* OK [CAPABILITY IMAP4rev1 IDLE] Mock IMAP Server Ready\r\n")

			for {
				line, err := r.ReadString('\n')
				if err != nil {
					return
				}
				line = strings.TrimRight(line, "\r\n")
				parts := strings.SplitN(line, " ", 2)
				if len(parts) < 2 {
					continue
				}
				tag := parts[0]
				cmd := parts[1]
				upperCmd := strings.ToUpper(cmd)

				switch {
				case strings.HasPrefix(upperCmd, "CAPABILITY"):
					writeLine("* CAPABILITY IMAP4rev1 IDLE\r\n" + tag + " OK CAPABILITY completed\r\n")
				case strings.HasPrefix(upperCmd, "LOGIN"):
					writeLine(tag + " OK LOGIN completed\r\n")
				case strings.HasPrefix(upperCmd, "SELECT"):
					writeLine("* 1 EXISTS\r\n* 0 RECENT\r\n* OK [UIDVALIDITY 1] UIDs valid\r\n* FLAGS (\\Seen)\r\n* OK [PERMANENTFLAGS (\\Seen)] Flags permitted\r\n" + tag + " OK [READ-WRITE] SELECT completed\r\n")
				case strings.HasPrefix(upperCmd, "UID SEARCH"):
					mu.Lock()
					seen := uid3005Seen
					mu.Unlock()
					if !seen {
						writeLine("* SEARCH 3005\r\n" + tag + " OK UID SEARCH completed\r\n")
					} else {
						writeLine("* SEARCH\r\n" + tag + " OK UID SEARCH completed\r\n")
					}
				case strings.HasPrefix(upperCmd, "UID FETCH"):
					fetchResp := fmt.Sprintf("* 1 FETCH (UID 3005 BODY[] {%d}\r\n%s)\r\n%s OK UID FETCH completed\r\n",
						len(invalidMsgTypeEmail), invalidMsgTypeEmail, tag)
					writeLine(fetchResp)
				case strings.HasPrefix(upperCmd, "UID STORE"):
					mu.Lock()
					uid3005Seen = true
					mu.Unlock()
					writeLine(tag + " OK UID STORE completed\r\n")
					select {
					case <-storeDone:
					default:
						close(storeDone)
					}
				case strings.HasPrefix(upperCmd, "IDLE"):
					writeLine("+ idling\r\n")
					doneLine, errDone := r.ReadString('\n')
					if errDone != nil {
						return
					}
					if strings.TrimRight(doneLine, "\r\n") == "DONE" {
						writeLine(tag + " OK IDLE completed\r\n")
					}
				case strings.HasPrefix(upperCmd, "LOGOUT"):
					writeLine("* BYE Logging out\r\n" + tag + " OK LOGOUT completed\r\n")
					return
				}
			}
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()

		var logBuf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logBuf, nil))

		listener := imap.NewListener(imap.ListenerOptions{
			IMAPServer: "127.0.0.1",
			IMAPPort:   9993,
			LoginEmail: "test@example.com",
			Password:   "secret",
			Logger:     logger,
			DialConn: func(_ context.Context, _ string) (net.Conn, error) {
				return clientConn, nil
			},
			Handler: func(_ context.Context, _ *model.Message) error {
				atomic.AddInt32(&handlerCalled, 1)
				return nil
			},
		})

		go func() {
			select {
			case <-storeDone:
				time.Sleep(10 * time.Millisecond)
				cancel()
			case <-ctx.Done():
			}
		}()

		_ = listener.Start(ctx)

		if got := atomic.LoadInt32(&handlerCalled); got != 0 {
			t.Errorf("expected handler not to be called for invalid msg_type poison email, got %d", got)
		}
		mu.Lock()
		seen := uid3005Seen
		mu.Unlock()
		if !seen {
			t.Error("expected invalid msg_type poison email UID 3005 to be marked SEEN immediately")
		}
		if !strings.Contains(logBuf.String(), "discarding malformed trigger email (poison pill)") {
			t.Errorf("expected poison pill error log, got logs: %s", logBuf.String())
		}
	})

	t.Run("email that is not a trigger is left UNSEEN and untouched", func(t *testing.T) {
		clientConn, serverConn := net.Pipe()
		defer func() { _ = clientConn.Close() }()
		defer func() { _ = serverConn.Close() }()

		var (
			mu             sync.Mutex
			uid3003Seen    bool
			handlerCalled  int32
			idleReached    = make(chan struct{})
			idleReachedOne sync.Once
		)

		const unrelatedEmail = "Subject: Your monthly invoice\r\n" +
			"Content-Type: text/plain; charset=utf-8\r\n\r\n" +
			`{"msg_type":"text","title":"bank","content":"not a trigger"}`

		go func() {
			r := bufio.NewReader(serverConn)
			w := bufio.NewWriter(serverConn)
			writeLine := func(s string) {
				_, _ = w.WriteString(s)
				_ = w.Flush()
			}

			writeLine("* OK [CAPABILITY IMAP4rev1 IDLE] Mock IMAP Server Ready\r\n")

			for {
				line, err := r.ReadString('\n')
				if err != nil {
					return
				}
				line = strings.TrimRight(line, "\r\n")
				parts := strings.SplitN(line, " ", 2)
				if len(parts) < 2 {
					continue
				}
				tag := parts[0]
				upperCmd := strings.ToUpper(parts[1])

				switch {
				case strings.HasPrefix(upperCmd, "CAPABILITY"):
					writeLine("* CAPABILITY IMAP4rev1 IDLE\r\n" + tag + " OK CAPABILITY completed\r\n")
				case strings.HasPrefix(upperCmd, "LOGIN"):
					writeLine(tag + " OK LOGIN completed\r\n")
				case strings.HasPrefix(upperCmd, "SELECT"):
					writeLine("* 1 EXISTS\r\n* 0 RECENT\r\n* OK [UIDVALIDITY 1] UIDs valid\r\n* FLAGS (\\Seen)\r\n* OK [PERMANENTFLAGS (\\Seen)] Flags permitted\r\n" + tag + " OK [READ-WRITE] SELECT completed\r\n")
				case strings.HasPrefix(upperCmd, "UID SEARCH"):
					mu.Lock()
					seen := uid3003Seen
					mu.Unlock()
					if !seen {
						writeLine("* SEARCH 3003\r\n" + tag + " OK UID SEARCH completed\r\n")
					} else {
						writeLine("* SEARCH\r\n" + tag + " OK UID SEARCH completed\r\n")
					}
				case strings.HasPrefix(upperCmd, "UID FETCH"):
					writeLine(fmt.Sprintf("* 1 FETCH (UID 3003 BODY[] {%d}\r\n%s)\r\n%s OK UID FETCH completed\r\n",
						len(unrelatedEmail), unrelatedEmail, tag))
				case strings.HasPrefix(upperCmd, "UID STORE"):
					mu.Lock()
					uid3003Seen = true
					mu.Unlock()
					writeLine(tag + " OK UID STORE completed\r\n")
				case strings.HasPrefix(upperCmd, "IDLE"):
					writeLine("+ idling\r\n")
					idleReachedOne.Do(func() { close(idleReached) })
					doneLine, errDone := r.ReadString('\n')
					if errDone != nil {
						return
					}
					if strings.TrimRight(doneLine, "\r\n") == "DONE" {
						writeLine(tag + " OK IDLE completed\r\n")
					}
				case strings.HasPrefix(upperCmd, "LOGOUT"):
					writeLine("* BYE Logging out\r\n" + tag + " OK LOGOUT completed\r\n")
					return
				}
			}
		}()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var logBuf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

		listener := imap.NewListener(imap.ListenerOptions{
			IMAPServer: "127.0.0.1",
			IMAPPort:   9993,
			LoginEmail: "test@example.com",
			Password:   "secret",
			Logger:     logger,
			DialConn: func(_ context.Context, _ string) (net.Conn, error) {
				return clientConn, nil
			},
			Handler: func(_ context.Context, _ *model.Message) error {
				atomic.AddInt32(&handlerCalled, 1)
				return nil
			},
		})

		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = listener.Start(ctx)
		}()

		// Once the client settles into IDLE the first fetch cycle is complete.
		select {
		case <-idleReached:
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for the listener to reach IDLE")
		}
		time.Sleep(20 * time.Millisecond)
		cancel()

		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for the listener to stop")
		}

		if got := atomic.LoadInt32(&handlerCalled); got != 0 {
			t.Errorf("expected handler not to be called for an unrelated email, got %d calls", got)
		}
		mu.Lock()
		seen := uid3003Seen
		mu.Unlock()
		if seen {
			t.Error("expected an unrelated (non-trigger) email to keep its UNSEEN state, but UID STORE was issued")
		}
		if !strings.Contains(logBuf.String(), "leaving non-trigger email unseen") || !strings.Contains(logBuf.String(), "Your monthly invoice") {
			t.Errorf("expected a log entry about leaving the non-trigger email unseen with subject, got logs: %s", logBuf.String())
		}
	})

	t.Run("custom Mailbox option selects configured mailbox", func(t *testing.T) {
		clientConn, serverConn := net.Pipe()
		defer func() { _ = clientConn.Close() }()
		defer func() { _ = serverConn.Close() }()

		var (
			mu          sync.Mutex
			selectCmd   string
			selectReady = make(chan struct{})
		)

		go func() {
			r := bufio.NewReader(serverConn)
			w := bufio.NewWriter(serverConn)
			writeLine := func(s string) {
				_, _ = w.WriteString(s)
				_ = w.Flush()
			}

			writeLine("* OK [CAPABILITY IMAP4rev1 IDLE] Mock IMAP Server Ready\r\n")

			for {
				line, err := r.ReadString('\n')
				if err != nil {
					return
				}
				line = strings.TrimRight(line, "\r\n")
				parts := strings.SplitN(line, " ", 2)
				if len(parts) < 2 {
					continue
				}
				tag := parts[0]
				cmd := parts[1]
				upperCmd := strings.ToUpper(cmd)

				switch {
				case strings.HasPrefix(upperCmd, "CAPABILITY"):
					writeLine("* CAPABILITY IMAP4rev1 IDLE\r\n" + tag + " OK CAPABILITY completed\r\n")
				case strings.HasPrefix(upperCmd, "LOGIN"):
					writeLine(tag + " OK LOGIN completed\r\n")
				case strings.HasPrefix(upperCmd, "SELECT"):
					mu.Lock()
					selectCmd = cmd
					mu.Unlock()
					writeLine("* 0 EXISTS\r\n* 0 RECENT\r\n* OK [UIDVALIDITY 1] UIDs valid\r\n* FLAGS (\\Seen)\r\n* OK [PERMANENTFLAGS (\\Seen)] Flags permitted\r\n" + tag + " OK [READ-WRITE] SELECT completed\r\n")
					select {
					case <-selectReady:
					default:
						close(selectReady)
					}
				case strings.HasPrefix(upperCmd, "UID SEARCH"):
					writeLine("* SEARCH\r\n" + tag + " OK UID SEARCH completed\r\n")
				case strings.HasPrefix(upperCmd, "IDLE"):
					writeLine("+ idling\r\n")
					doneLine, errDone := r.ReadString('\n')
					if errDone != nil {
						return
					}
					if strings.TrimRight(doneLine, "\r\n") == "DONE" {
						writeLine(tag + " OK IDLE completed\r\n")
					}
				case strings.HasPrefix(upperCmd, "LOGOUT"):
					writeLine("* BYE Logging out\r\n" + tag + " OK LOGOUT completed\r\n")
					return
				}
			}
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()

		listener := imap.NewListener(imap.ListenerOptions{
			IMAPServer: "127.0.0.1",
			IMAPPort:   9993,
			LoginEmail: "test@example.com",
			Password:   "secret",
			Mailbox:    "PushTransfer",
			DialConn: func(_ context.Context, _ string) (net.Conn, error) {
				return clientConn, nil
			},
		})

		go func() {
			select {
			case <-selectReady:
				time.Sleep(20 * time.Millisecond)
				cancel()
			case <-ctx.Done():
			}
		}()

		_ = listener.Start(ctx)

		mu.Lock()
		gotSelect := selectCmd
		mu.Unlock()
		if !strings.Contains(gotSelect, "PushTransfer") {
			t.Errorf("expected SELECT command to target 'PushTransfer', got %q", gotSelect)
		}
	})

	t.Run("non-IDLE polling fallback retries failed handler at min(PollInterval, retryDelay)", func(t *testing.T) {
		clientConn, serverConn := net.Pipe()
		defer func() { _ = clientConn.Close() }()
		defer func() { _ = serverConn.Close() }()

		var (
			mu              sync.Mutex
			uid4001Seen     bool
			handlerAttempts int32
			storeDone       = make(chan struct{})
		)

		const rawEmail = "Subject: PushTransfer Email Trigger\r\n" +
			"Content-Type: text/plain; charset=utf-8\r\n\r\n" +
			`{"msg_type":"text","title":"10086","content":"poll retry"}`

		go func() {
			r := bufio.NewReader(serverConn)
			w := bufio.NewWriter(serverConn)
			writeLine := func(s string) {
				_, _ = w.WriteString(s)
				_ = w.Flush()
			}

			// Greeting without IDLE capability
			writeLine("* OK [CAPABILITY IMAP4rev1] Mock IMAP Server Ready\r\n")

			for {
				line, err := r.ReadString('\n')
				if err != nil {
					return
				}
				line = strings.TrimRight(line, "\r\n")
				parts := strings.SplitN(line, " ", 2)
				if len(parts) < 2 {
					continue
				}
				tag := parts[0]
				cmd := parts[1]
				upperCmd := strings.ToUpper(cmd)

				switch {
				case strings.HasPrefix(upperCmd, "CAPABILITY"):
					writeLine("* CAPABILITY IMAP4rev1\r\n" + tag + " OK CAPABILITY completed\r\n")
				case strings.HasPrefix(upperCmd, "LOGIN"):
					writeLine(tag + " OK LOGIN completed\r\n")
				case strings.HasPrefix(upperCmd, "SELECT"):
					writeLine("* 1 EXISTS\r\n* 0 RECENT\r\n* OK [UIDVALIDITY 1] UIDs valid\r\n* FLAGS (\\Seen)\r\n* OK [PERMANENTFLAGS (\\Seen)] Flags permitted\r\n" + tag + " OK [READ-WRITE] SELECT completed\r\n")
				case strings.HasPrefix(upperCmd, "NOOP"):
					writeLine(tag + " OK NOOP completed\r\n")
				case strings.HasPrefix(upperCmd, "UID SEARCH"):
					mu.Lock()
					seen := uid4001Seen
					mu.Unlock()
					if !seen {
						writeLine("* SEARCH 4001\r\n" + tag + " OK UID SEARCH completed\r\n")
					} else {
						writeLine("* SEARCH\r\n" + tag + " OK UID SEARCH completed\r\n")
					}
				case strings.HasPrefix(upperCmd, "UID FETCH"):
					fetchResp := fmt.Sprintf("* 1 FETCH (UID 4001 BODY[] {%d}\r\n%s)\r\n%s OK UID FETCH completed\r\n",
						len(rawEmail), rawEmail, tag)
					writeLine(fetchResp)
				case strings.HasPrefix(upperCmd, "UID STORE"):
					mu.Lock()
					uid4001Seen = true
					mu.Unlock()
					writeLine(tag + " OK UID STORE completed\r\n")
					select {
					case <-storeDone:
					default:
						close(storeDone)
					}
				case strings.HasPrefix(upperCmd, "LOGOUT"):
					writeLine("* BYE Logging out\r\n" + tag + " OK LOGOUT completed\r\n")
					return
				}
			}
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()

		listener := imap.NewListener(imap.ListenerOptions{
			IMAPServer:   "127.0.0.1",
			IMAPPort:     9993,
			LoginEmail:   "test@example.com",
			Password:     "secret",
			PollInterval: 10 * time.Minute,
			MinBackoff:   25 * time.Millisecond,
			MaxBackoff:   60 * time.Millisecond,
			DialConn: func(_ context.Context, _ string) (net.Conn, error) {
				return clientConn, nil
			},
			Handler: func(_ context.Context, _ *model.Message) error {
				if atomic.AddInt32(&handlerAttempts, 1) == 1 {
					return errors.New("temporary failure")
				}
				return nil
			},
		})

		go func() {
			select {
			case <-storeDone:
				time.Sleep(10 * time.Millisecond)
				cancel()
			case <-ctx.Done():
			}
		}()

		_ = listener.Start(ctx)

		if got := atomic.LoadInt32(&handlerAttempts); got != 2 {
			t.Errorf("expected 2 handler attempts in polling fallback retry, got %d", got)
		}
		mu.Lock()
		seen := uid4001Seen
		mu.Unlock()
		if !seen {
			t.Error("expected UID 4001 to be marked SEEN after polling retry succeeded")
		}
	})
}

func TestListener_BackoffResetAfterSuccessfulSession(t *testing.T) {
	var (
		mu               sync.Mutex
		dialCount        int
		disconnectTime   time.Time
		reconnectElapsed time.Duration
		fifthDialCh      = make(chan struct{})
	)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	listener := imap.NewListener(imap.ListenerOptions{
		IMAPServer: "127.0.0.1",
		IMAPPort:   9993,
		LoginEmail: "test@example.com",
		Password:   "secret",
		MinBackoff: 20 * time.Millisecond,
		MaxBackoff: 500 * time.Millisecond,
		DialConn: func(_ context.Context, _ string) (net.Conn, error) {
			mu.Lock()
			dialCount++
			currentDial := dialCount
			if currentDial == 5 {
				reconnectElapsed = time.Since(disconnectTime)
				close(fifthDialCh)
				cancel()
			}
			mu.Unlock()

			if currentDial <= 3 {
				return nil, errors.New("connection refused")
			}
			if currentDial == 4 {
				clientConn, serverConn := net.Pipe()
				go func() {
					defer func() { _ = serverConn.Close() }()
					r := bufio.NewReader(serverConn)
					w := bufio.NewWriter(serverConn)
					writeLine := func(s string) {
						_, _ = w.WriteString(s)
						_ = w.Flush()
					}

					writeLine("* OK [CAPABILITY IMAP4rev1 IDLE] Mock IMAP Server Ready\r\n")

					for {
						line, err := r.ReadString('\n')
						if err != nil {
							return
						}
						line = strings.TrimRight(line, "\r\n")
						parts := strings.SplitN(line, " ", 2)
						if len(parts) < 2 {
							continue
						}
						tag := parts[0]
						cmd := parts[1]
						upperCmd := strings.ToUpper(cmd)

						switch {
						case strings.HasPrefix(upperCmd, "CAPABILITY"):
							writeLine("* CAPABILITY IMAP4rev1 IDLE\r\n" + tag + " OK CAPABILITY completed\r\n")
						case strings.HasPrefix(upperCmd, "LOGIN"):
							writeLine(tag + " OK LOGIN completed\r\n")
						case strings.HasPrefix(upperCmd, "SELECT"):
							writeLine("* 0 EXISTS\r\n* 0 RECENT\r\n* OK [UIDVALIDITY 1] UIDs valid\r\n* FLAGS (\\Seen)\r\n* OK [PERMANENTFLAGS (\\Seen)] Flags permitted\r\n" + tag + " OK [READ-WRITE] SELECT completed\r\n")
						case strings.HasPrefix(upperCmd, "UID SEARCH"):
							writeLine("* SEARCH\r\n" + tag + " OK UID SEARCH completed\r\n")
						case strings.HasPrefix(upperCmd, "IDLE"):
							writeLine("+ idling\r\n")
							// Give client a moment to enter IDLE wait state, then notify and drop connection
							time.Sleep(15 * time.Millisecond)
							mu.Lock()
							disconnectTime = time.Now()
							mu.Unlock()
							writeLine("* 1 EXISTS\r\n")
							return
						}
					}
				}()
				return clientConn, nil
			}
			return nil, errors.New("done")
		},
	})

	_ = listener.Start(ctx)

	select {
	case <-fifthDialCh:
	default:
		t.Fatal("5th dial did not occur before timeout")
	}

	mu.Lock()
	elapsed := reconnectElapsed
	mu.Unlock()

	if elapsed >= 60*time.Millisecond {
		t.Errorf("expected reconnect after established session to reset attempt to 0 (backoff < 60ms), got %v", elapsed)
	}
}

func TestListener_UnresponsiveServerTimeoutAndShutdown(t *testing.T) {
	t.Run("handshake timeout when server accepts TCP but never sends greeting", func(t *testing.T) {
		var dialCount int32
		secondDialCh := make(chan struct{})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var (
			hungMu    sync.Mutex
			hungConns []net.Conn
		)
		defer func() {
			hungMu.Lock()
			defer hungMu.Unlock()
			for _, c := range hungConns {
				_ = c.Close()
			}
		}()

		listener := imap.NewListener(imap.ListenerOptions{
			IMAPServer:  "127.0.0.1",
			IMAPPort:    9993,
			LoginEmail:  "test@example.com",
			Password:    "secret",
			DialTimeout: 80 * time.Millisecond,
			MinBackoff:  20 * time.Millisecond,
			MaxBackoff:  50 * time.Millisecond,
			DialConn: func(_ context.Context, _ string) (net.Conn, error) {
				n := atomic.AddInt32(&dialCount, 1)
				if n == 1 {
					cConn, sConn := net.Pipe()
					hungMu.Lock()
					hungConns = append(hungConns, cConn, sConn)
					hungMu.Unlock()
					// Server accepts connection but never writes greeting
					return cConn, nil
				}
				close(secondDialCh)
				cancel()
				return nil, errors.New("cancelled")
			},
		})

		done := make(chan error, 1)
		go func() {
			done <- listener.Start(ctx)
		}()

		select {
		case <-secondDialCh:
		case <-time.After(350 * time.Millisecond):
			cancel()
			t.Fatal("listener hung on silent server during handshake instead of timing out after DialTimeout (80ms)")
		}

		select {
		case err := <-done:
			// Context cancellation is the clean shutdown path, so Start must
			// hand back nil; any other value means an unexpected exit reason.
			if err != nil {
				t.Fatalf("listener.Start returned %v after cancel, want nil", err)
			}
		case <-time.After(200 * time.Millisecond):
			t.Fatal("listener.Start did not exit after cancel")
		}
	})

	t.Run("graceful shutdown completes promptly when server blackholes during IDLE", func(t *testing.T) {
		cConn, sConn := net.Pipe()
		defer func() { _ = cConn.Close() }()
		defer func() { _ = sConn.Close() }()

		idlingCh := make(chan struct{})
		go func() {
			r := bufio.NewReader(sConn)
			w := bufio.NewWriter(sConn)
			writeLine := func(s string) {
				_, _ = w.WriteString(s)
				_ = w.Flush()
			}

			writeLine("* OK [CAPABILITY IMAP4rev1 IDLE] Mock IMAP Server Ready\r\n")
			for {
				line, err := r.ReadString('\n')
				if err != nil {
					return
				}
				line = strings.TrimRight(line, "\r\n")
				parts := strings.SplitN(line, " ", 2)
				if len(parts) < 2 {
					continue
				}
				tag := parts[0]
				upperCmd := strings.ToUpper(parts[1])

				switch {
				case strings.HasPrefix(upperCmd, "CAPABILITY"):
					writeLine("* CAPABILITY IMAP4rev1 IDLE\r\n" + tag + " OK CAPABILITY completed\r\n")
				case strings.HasPrefix(upperCmd, "LOGIN"):
					writeLine(tag + " OK LOGIN completed\r\n")
				case strings.HasPrefix(upperCmd, "SELECT"):
					writeLine("* 0 EXISTS\r\n* 0 RECENT\r\n* OK [UIDVALIDITY 1] UIDs valid\r\n* FLAGS (\\Seen)\r\n* OK [PERMANENTFLAGS (\\Seen)] Flags permitted\r\n" + tag + " OK [READ-WRITE] SELECT completed\r\n")
				case strings.HasPrefix(upperCmd, "UID SEARCH"):
					writeLine("* SEARCH\r\n" + tag + " OK UID SEARCH completed\r\n")
				case strings.HasPrefix(upperCmd, "IDLE"):
					writeLine("+ idling\r\n")
					close(idlingCh)
					// Drain incoming bytes (like DONE\r\n) so write doesn't block, but NEVER reply!
					for {
						if _, err := r.ReadByte(); err != nil {
							return
						}
					}
				}
			}
		}()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		listener := imap.NewListener(imap.ListenerOptions{
			IMAPServer: "127.0.0.1",
			IMAPPort:   9993,
			LoginEmail: "test@example.com",
			Password:   "secret",
			DialConn: func(_ context.Context, _ string) (net.Conn, error) {
				return cConn, nil
			},
		})

		done := make(chan error, 1)
		go func() {
			done <- listener.Start(ctx)
		}()

		select {
		case <-idlingCh:
		case <-time.After(300 * time.Millisecond):
			t.Fatal("timed out waiting for listener to enter IDLE")
		}

		// Give listener 15ms to enter idle select, then cancel context
		time.Sleep(15 * time.Millisecond)
		cancel()

		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("expected nil on shutdown, got %v", err)
			}
		case <-time.After(400 * time.Millisecond):
			_ = cConn.Close()
			_ = sConn.Close()
			t.Fatal("listener.Start hung on graceful shutdown when server did not reply to DONE / LOGOUT")
		}
	})
}

type errFailingReader struct {
	err error
}

func (f *errFailingReader) Read(_ []byte) (int, error) {
	return 0, f.err
}

func TestParser_AdvancedErrorHandling(t *testing.T) {
	t.Run("DecodeSubject with invalid charset RFC2047 returns error", func(t *testing.T) {
		_, err := imap.DecodeSubject("=?unsupported-unknown-charset?B?1234?=")
		if err == nil {
			t.Fatal("expected error decoding invalid charset in RFC 2047 subject, got nil")
		}
	})

	t.Run("ExtractTextPlain with empty or html only email returns error", func(t *testing.T) {
		raw := "Subject: Hello\r\nContent-Type: text/html\r\n\r\n<h1>Hello</h1>"
		_, err := imap.ExtractTextPlain(strings.NewReader(raw))
		if err == nil {
			t.Fatal("expected error when no text/plain body exists, got nil")
		}
	})

	t.Run("ExtractTextPlain with malformed reader", func(t *testing.T) {
		errReader := &errFailingReader{err: errors.New("read fault")}
		_, err := imap.ExtractTextPlain(errReader)
		if err == nil {
			t.Fatal("expected error on failing reader, got nil")
		}
	})

	t.Run("ParseEmail with malformed RFC2047 subject returns error", func(t *testing.T) {
		raw := "Subject: =?unsupported-unknown-charset?B?1234?=\r\nContent-Type: text/plain\r\n\r\n{\"msg_type\":\"text\"}"
		_, _, err := imap.ParseEmail(strings.NewReader(raw))
		if err == nil {
			t.Fatal("expected error on invalid RFC2047 subject, got nil")
		}
	})

	t.Run("ParseEmail with whitespace text/plain body returns error", func(t *testing.T) {
		raw := "Subject: PushTransfer Email Trigger\r\nContent-Type: text/plain\r\n\r\n   \t  \r\n  "
		_, _, err := imap.ParseEmail(strings.NewReader(raw))
		if err == nil {
			t.Fatal("expected error on empty text/plain body, got nil")
		}
	})
}

func TestListener_HeartbeatDuringIdle(t *testing.T) {
	cConn, sConn := net.Pipe()
	defer func() { _ = cConn.Close() }()
	defer func() { _ = sConn.Close() }()

	var (
		mu            sync.Mutex
		commands      []string
		noopCount     int
		idleCount     int
		heartbeatDone = make(chan struct{})
	)

	go func() {
		r := bufio.NewReader(sConn)
		w := bufio.NewWriter(sConn)
		writeLine := func(s string) {
			_, _ = w.WriteString(s)
			_ = w.Flush()
		}

		writeLine("* OK [CAPABILITY IMAP4rev1 IDLE] Ready\r\n")

		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			parts := strings.SplitN(line, " ", 2)
			if len(parts) < 2 {
				continue
			}
			tag := parts[0]
			cmd := parts[1]
			upperCmd := strings.ToUpper(cmd)

			mu.Lock()
			commands = append(commands, upperCmd)
			mu.Unlock()

			switch {
			case strings.HasPrefix(upperCmd, "CAPABILITY"):
				writeLine("* CAPABILITY IMAP4rev1 IDLE\r\n" + tag + " OK CAPABILITY completed\r\n")
			case strings.HasPrefix(upperCmd, "LOGIN"):
				writeLine(tag + " OK LOGIN completed\r\n")
			case strings.HasPrefix(upperCmd, "SELECT"):
				writeLine(tag + " OK [READ-WRITE] SELECT completed\r\n")
			case strings.HasPrefix(upperCmd, "UID SEARCH"):
				writeLine("* SEARCH\r\n" + tag + " OK UID SEARCH completed\r\n")
			case strings.HasPrefix(upperCmd, "IDLE"):
				mu.Lock()
				idleCount++
				if idleCount >= 2 {
					select {
					case <-heartbeatDone:
					default:
						close(heartbeatDone)
					}
				}
				mu.Unlock()
				writeLine("+ idling\r\n")

				// Read DONE\r\n from client when heartbeat triggers stopIdle
				doneLine, errDone := r.ReadString('\n')
				if errDone != nil {
					return
				}
				if strings.TrimRight(doneLine, "\r\n") == "DONE" {
					writeLine(tag + " OK IDLE completed\r\n")
				}
			case strings.HasPrefix(upperCmd, "NOOP"):
				mu.Lock()
				noopCount++
				mu.Unlock()
				writeLine(tag + " OK NOOP completed\r\n")
			case strings.HasPrefix(upperCmd, "LOGOUT"):
				writeLine(tag + " OK LOGOUT completed\r\n")
				return
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	listener := imap.NewListener(imap.ListenerOptions{
		IMAPServer:        "127.0.0.1",
		IMAPPort:          9993,
		LoginEmail:        "heartbeat@example.com",
		Password:          "secret",
		HeartbeatInterval: 30 * time.Millisecond,
		DialConn: func(_ context.Context, _ string) (net.Conn, error) {
			return cConn, nil
		},
	})

	done := make(chan error, 1)
	go func() {
		done <- listener.Start(ctx)
	}()

	select {
	case <-heartbeatDone:
		// Heartbeat successfully fired NOOP and resumed!
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for heartbeat ticker to trigger NOOP during IDLE")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected error on shutdown: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("listener did not shut down in time")
	}

	mu.Lock()
	defer mu.Unlock()
	if noopCount < 1 {
		t.Errorf("expected at least 1 NOOP from heartbeat, got %d", noopCount)
	}
	if idleCount < 2 {
		t.Errorf("expected at least 2 IDLE commands (initial + re-entry after heartbeat), got %d", idleCount)
	}
}

func TestIMAP_ProtocolExtension_EndToEnd(t *testing.T) {
	rawEmail := "Subject: PushTransfer Email Trigger\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\n" +
		`{
			"msg_type": "text",
			"title": "中国联通",
			"content": "【中国联通】您的验证码为 123890，请勿向他人泄露。",
			"hostname": "pixel-gateway",
			"battery_level": "85%",
			"time_sensitive": true,
			"timestamp": "2026-10-18T00:15:20+08:00",
			"send_time": "Sun, 18 Oct 2026 00:15:20 +0800"
		}`

	msg, ok, err := imap.ParseEmail(strings.NewReader(rawEmail))
	if err != nil {
		t.Fatalf("failed to parse email: %v", err)
	}
	if !ok {
		t.Fatal("expected email to be processed, but was ignored")
	}
	if msg == nil {
		t.Fatal("expected non-nil message")
	}

	if msg.Timestamp != 1792253720 {
		t.Errorf("expected Timestamp 1792253720, got %d", msg.Timestamp)
	}
	if msg.SendTime != 1792253720 {
		t.Errorf("expected SendTime 1792253720, got %d", msg.SendTime)
	}
	if msg.BatteryLevel == nil || *msg.BatteryLevel != 85 {
		t.Errorf("expected BatteryLevel 85, got %v", msg.BatteryLevel)
	}
	if msg.Hostname != "pixel-gateway" {
		t.Errorf("expected Hostname 'pixel-gateway', got %q", msg.Hostname)
	}
	if !msg.TimeSensitive {
		t.Errorf("expected TimeSensitive true, got false")
	}

	cst := time.FixedZone("Asia/Shanghai", 8*3600)
	complexText := msg.GenComplexText(cst)
	if !strings.Contains(complexText, "设备主机名: pixel-gateway") {
		t.Errorf("expected complex text to contain '设备主机名: pixel-gateway', got:\n%s", complexText)
	}
	if !strings.Contains(complexText, "当前电量: 85%") {
		t.Errorf("expected complex text to contain '当前电量: 85%%', got:\n%s", complexText)
	}
}

func TestParseEmail_TimeSensitiveVariants(t *testing.T) {
	testCases := []struct {
		name          string
		timeSensitive string
		expected      bool
	}{
		{"string true", `"true"`, true},
		{"string false", `"false"`, false},
		{"integer 1", `1`, true},
		{"integer 0", `0`, false},
		{"string 1", `"1"`, true},
		{"string 0", `"0"`, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			rawEmail := fmt.Sprintf("Subject: PushTransfer Email Trigger\r\n"+
				"Content-Type: text/plain; charset=utf-8\r\n\r\n"+
				`{"msg_type":"text","title":"test","content":"body","time_sensitive":%s}`, tc.timeSensitive)

			msg, ok, err := imap.ParseEmail(strings.NewReader(rawEmail))
			if err != nil {
				t.Fatalf("failed to parse email: %v", err)
			}
			if !ok {
				t.Fatal("expected email to be processed, but was ignored")
			}
			if msg == nil {
				t.Fatal("expected non-nil message")
			}
			if msg.TimeSensitive != tc.expected {
				t.Errorf("expected TimeSensitive %v for %s, got %v", tc.expected, tc.timeSensitive, msg.TimeSensitive)
			}
		})
	}
}

func TestListener_TimeSensitive_EndToEndIntegration(t *testing.T) {
	testCases := []struct {
		name          string
		timeSensitive string
		expected      bool
	}{
		{"string true", `"true"`, true},
		{"string false", `"false"`, false},
		{"integer 1", `1`, true},
		{"integer 0", `0`, false},
		{"string 1", `"1"`, true},
		{"string 0", `"0"`, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			clientConn, serverConn := net.Pipe()
			defer func() { _ = clientConn.Close() }()
			defer func() { _ = serverConn.Close() }()

			var (
				mu          sync.Mutex
				emailSeen   bool
				delivered   *model.Message
				deliveredCh = make(chan struct{})
			)

			rawEmail := fmt.Sprintf("Subject: PushTransfer Email Trigger\r\n"+
				"MIME-Version: 1.0\r\n"+
				"Content-Type: text/plain; charset=utf-8\r\n\r\n"+
				`{"msg_type":"text","title":"E2E Test","content":"Verification Body","time_sensitive":%s}`, tc.timeSensitive)

			go func() {
				r := bufio.NewReader(serverConn)
				w := bufio.NewWriter(serverConn)
				writeLine := func(s string) {
					_, _ = w.WriteString(s)
					_ = w.Flush()
				}

				writeLine("* OK [CAPABILITY IMAP4rev1 IDLE] Mock IMAP Server Ready\r\n")

				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					line = strings.TrimRight(line, "\r\n")
					parts := strings.SplitN(line, " ", 2)
					if len(parts) < 2 {
						continue
					}
					tag := parts[0]
					cmd := parts[1]
					upperCmd := strings.ToUpper(cmd)

					switch {
					case strings.HasPrefix(upperCmd, "CAPABILITY"):
						writeLine("* CAPABILITY IMAP4rev1 IDLE\r\n" + tag + " OK CAPABILITY completed\r\n")
					case strings.HasPrefix(upperCmd, "LOGIN"):
						writeLine(tag + " OK LOGIN completed\r\n")
					case strings.HasPrefix(upperCmd, "SELECT"):
						writeLine("* 1 EXISTS\r\n* 0 RECENT\r\n* OK [UIDVALIDITY 1] UIDs valid\r\n* FLAGS (\\Seen)\r\n* OK [PERMANENTFLAGS (\\Seen)] Flags permitted\r\n" + tag + " OK [READ-WRITE] SELECT completed\r\n")
					case strings.HasPrefix(upperCmd, "UID SEARCH"):
						mu.Lock()
						seen := emailSeen
						mu.Unlock()
						if !seen {
							writeLine("* SEARCH 5001\r\n" + tag + " OK UID SEARCH completed\r\n")
						} else {
							writeLine("* SEARCH\r\n" + tag + " OK UID SEARCH completed\r\n")
						}
					case strings.HasPrefix(upperCmd, "UID FETCH"):
						fetchResp := fmt.Sprintf("* 1 FETCH (UID 5001 BODY[] {%d}\r\n%s)\r\n%s OK UID FETCH completed\r\n",
							len(rawEmail), rawEmail, tag)
						writeLine(fetchResp)
					case strings.HasPrefix(upperCmd, "UID STORE"):
						mu.Lock()
						emailSeen = true
						mu.Unlock()
						writeLine(tag + " OK UID STORE completed\r\n")
					case strings.HasPrefix(upperCmd, "IDLE"):
						writeLine("+ idling\r\n")
						for {
							l, errD := r.ReadString('\n')
							if errD != nil {
								return
							}
							if strings.TrimRight(l, "\r\n") == "DONE" {
								writeLine(tag + " OK IDLE completed\r\n")
								break
							}
						}
					case strings.HasPrefix(upperCmd, "LOGOUT"):
						writeLine("* BYE Logging out\r\n" + tag + " OK LOGOUT completed\r\n")
						return
					}
				}
			}()

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			listener := imap.NewListener(imap.ListenerOptions{
				IMAPServer: "127.0.0.1",
				IMAPPort:   9993,
				LoginEmail: "test@example.com",
				Password:   "secret",
				DialConn: func(_ context.Context, _ string) (net.Conn, error) {
					return clientConn, nil
				},
				Handler: func(_ context.Context, msg *model.Message) error {
					mu.Lock()
					delivered = msg.Clone()
					mu.Unlock()
					close(deliveredCh)
					return nil
				},
			})

			go func() {
				select {
				case <-deliveredCh:
					cancel()
				case <-time.After(1500 * time.Millisecond):
					cancel()
				}
			}()

			_ = listener.Start(ctx)

			mu.Lock()
			d := delivered
			seen := emailSeen
			mu.Unlock()

			if d == nil {
				t.Fatalf("expected message to be delivered to handler, got nil")
			}
			if d.TimeSensitive != tc.expected {
				t.Errorf("expected TimeSensitive %v for %s, got %v", tc.expected, tc.timeSensitive, d.TimeSensitive)
			}
			if !seen {
				t.Errorf("expected email UID 5001 to be marked as \\Seen after successful delivery")
			}
		})
	}
}

func TestListener_DebugLogging_TriggerEmail(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer func() { _ = clientConn.Close() }()
	defer func() { _ = serverConn.Close() }()

	const triggerEmail = "Subject: PushTransfer Email Trigger\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\n" +
		`{"msg_type":"text","title":"10086","content":"Your balance is low"}`

	go func() {
		r := bufio.NewReader(serverConn)
		w := bufio.NewWriter(serverConn)
		writeLine := func(s string) {
			_, _ = w.WriteString(s)
			_ = w.Flush()
		}

		writeLine("* OK [CAPABILITY IMAP4rev1 IDLE] Mock IMAP Server Ready\r\n")

		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			parts := strings.SplitN(line, " ", 2)
			if len(parts) < 2 {
				continue
			}
			tag := parts[0]
			upperCmd := strings.ToUpper(parts[1])

			switch {
			case strings.HasPrefix(upperCmd, "CAPABILITY"):
				writeLine("* CAPABILITY IMAP4rev1 IDLE\r\n" + tag + " OK CAPABILITY completed\r\n")
			case strings.HasPrefix(upperCmd, "LOGIN"):
				writeLine(tag + " OK LOGIN completed\r\n")
			case strings.HasPrefix(upperCmd, "SELECT"):
				writeLine("* 1 EXISTS\r\n* 0 RECENT\r\n* OK [UIDVALIDITY 1] UIDs valid\r\n* FLAGS (\\Seen)\r\n* OK [PERMANENTFLAGS (\\Seen)] Flags permitted\r\n" + tag + " OK [READ-WRITE] SELECT completed\r\n")
			case strings.HasPrefix(upperCmd, "UID SEARCH"):
				writeLine("* SEARCH 9001\r\n" + tag + " OK UID SEARCH completed\r\n")
			case strings.HasPrefix(upperCmd, "UID FETCH"):
				writeLine(fmt.Sprintf("* 1 FETCH (UID 9001 BODY[] {%d}\r\n%s)\r\n%s OK UID FETCH completed\r\n",
					len(triggerEmail), triggerEmail, tag))
			case strings.HasPrefix(upperCmd, "UID STORE"):
				writeLine(tag + " OK UID STORE completed\r\n")
			case strings.HasPrefix(upperCmd, "IDLE"):
				writeLine("+ idling\r\n")
				doneLine, errDone := r.ReadString('\n')
				if errDone != nil {
					return
				}
				if strings.TrimRight(doneLine, "\r\n") == "DONE" {
					writeLine(tag + " OK IDLE completed\r\n")
				}
			case strings.HasPrefix(upperCmd, "LOGOUT"):
				writeLine("* BYE Logging out\r\n" + tag + " OK LOGOUT completed\r\n")
				return
			}
		}
	}()

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	ctx, cancel := context.WithCancel(context.Background())

	listener := imap.NewListener(imap.ListenerOptions{
		IMAPServer: "127.0.0.1",
		IMAPPort:   9993,
		LoginEmail: "test@example.com",
		Password:   "secret",
		Logger:     logger,
		DialConn: func(_ context.Context, _ string) (net.Conn, error) {
			return clientConn, nil
		},
		Handler: func(_ context.Context, _ *model.Message) error {
			cancel()
			return nil
		},
	})

	_ = listener.Start(ctx)

	logStr := logBuf.String()
	if !strings.Contains(logStr, "imap parsed trigger email") ||
		!strings.Contains(logStr, "title=10086") ||
		!strings.Contains(logStr, "msg_type=text") {
		t.Errorf("expected debug log about parsed trigger email, got:\n%s", logStr)
	}
}

func TestListener_TimeLocation_AppliedToTriggerMessage(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer func() { _ = clientConn.Close() }()
	defer func() { _ = serverConn.Close() }()

	go func() {
		r := bufio.NewReader(serverConn)
		writeLine := func(s string) {
			_, _ = serverConn.Write([]byte(s))
		}

		writeLine("* OK IMAP4rev1 Service Ready\r\n")

		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			parts := strings.Fields(line)
			if len(parts) < 2 {
				continue
			}
			tag := parts[0]
			cmd := parts[1]
			upperCmd := strings.ToUpper(strings.TrimSpace(line[len(tag)+1:]))

			switch {
			case strings.HasPrefix(upperCmd, "CAPABILITY"):
				writeLine("* CAPABILITY IMAP4rev1 IDLE\r\n" + tag + " OK CAPABILITY completed\r\n")
			case cmd == "LOGIN":
				writeLine(tag + " OK LOGIN completed\r\n")
			case cmd == "SELECT":
				writeLine("* 1 EXISTS\r\n" + tag + " OK [READ-WRITE] SELECT completed\r\n")
			case strings.HasPrefix(upperCmd, "UID SEARCH"):
				writeLine("* SEARCH 101\r\n" + tag + " OK UID SEARCH completed\r\n")
			case strings.HasPrefix(upperCmd, "UID FETCH"):
				body := "From: test@example.com\r\nSubject: PushTransfer Email Trigger\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n{\"msg_type\":\"text\",\"title\":\"10086\",\"content\":\"Hello\",\"timestamp\":1700000000}"
				msg := fmt.Sprintf("* 1 FETCH (UID 101 BODY[] {%d}\r\n%s)\r\n%s OK UID FETCH completed\r\n", len(body), body, tag)
				writeLine(msg)
			case strings.HasPrefix(upperCmd, "UID STORE"):
				writeLine(tag + " OK UID STORE completed\r\n")
			case strings.HasPrefix(upperCmd, "IDLE"):
				writeLine("+ idling\r\n")
				doneLine, errDone := r.ReadString('\n')
				if errDone != nil {
					return
				}
				if strings.TrimRight(doneLine, "\r\n") == "DONE" {
					writeLine(tag + " OK IDLE completed\r\n")
				}
			case strings.HasPrefix(upperCmd, "LOGOUT"):
				writeLine("* BYE Logging out\r\n" + tag + " OK LOGOUT completed\r\n")
				return
			}
		}
	}()

	shanghai := time.FixedZone("Asia/Shanghai", 8*3600)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var capturedMsg *model.Message
	var mu sync.Mutex

	listener := imap.NewListener(imap.ListenerOptions{
		IMAPServer:   "127.0.0.1",
		IMAPPort:     9993,
		LoginEmail:   "test@example.com",
		Password:     "secret",
		TimeLocation: shanghai,
		DialConn: func(_ context.Context, _ string) (net.Conn, error) {
			return clientConn, nil
		},
		Handler: func(_ context.Context, m *model.Message) error {
			mu.Lock()
			capturedMsg = m.Clone()
			mu.Unlock()
			cancel()
			return nil
		},
	})

	_ = listener.Start(ctx)

	mu.Lock()
	msg := capturedMsg
	mu.Unlock()

	if msg == nil {
		t.Fatal("expected message to be handled, got nil")
	}
	text := msg.GenComplexText()
	// 1700000000 in Asia/Shanghai is 2023-11-15 06:13:20
	if !strings.Contains(text, "2023-11-15 06:13:20") {
		t.Errorf("expected complex text to format in Asia/Shanghai (2023-11-15 06:13:20), got:\n%s", text)
	}
}
