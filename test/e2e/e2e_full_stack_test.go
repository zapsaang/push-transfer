package e2e_test

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fullStackHarness encapsulates a running push-transfer daemon with mock downstream targets.
type fullStackHarness struct {
	cmd        *exec.Cmd
	baseURL    string
	mockBark   *mockBarkServer
	mockTG     *mockTelegramServer
	mockSMTP   *mockSMTPServer
	httpClient *http.Client
}

func startFullStackDaemon(t testing.TB) *fullStackHarness {
	t.Helper()

	bin := builtBinaryPath
	pki := setupTestPKI(t)

	const barkKey = "1234567890123456"
	mockBark := newMockBarkServer(t, []byte(barkKey))
	mockTG := newMockTelegramServer(t)
	mockSMTP := newMockSMTPServer(t, pki)

	cmd := exec.Command(bin)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"PORT=0",
		"RUN_MODE=webhook",
		// Trust the test CA for SMTP TLS dialing
		"SSL_CERT_FILE=" + pki.caFilePath,
		// Multi-credential configuration (Bearer + Basic)
		`AUTH_CREDENTIALS=[{"name":"bearer-admin","token":"bearer-token-12345"},{"name":"basic-user","username":"admin","password":"secretpassword"}]`,
		// Bark delivery
		"BARK_DEVICE_KEY_LIST=dev-e2e-main",
		"BARK_POST_URL=" + mockBark.url(),
		"AES_128_ECB_KEY=" + barkKey,
		// Telegram delivery
		"TELEGRAM_BOT_TOKEN=123:TG_TOKEN",
		"TELEGRAM_CHAT_ID=998877",
		"TELEGRAM_API_URL=" + mockTG.server.URL,
		// SMTP delivery
		"EMAIL_DELIVER_SENDER_EMAIL=sender@example.com",
		"EMAIL_DELIVER_RECEIVER_EMAIL=receiver@example.com",
		"EMAIL_DELIVER_PASSWORD=smtppassword",
		"EMAIL_DELIVER_SMTP_SERVER=127.0.0.1",
		fmt.Sprintf("EMAIL_DELIVER_SMTP_PORT=%d", mockSMTP.port()),
		// Pipeline rules
		`REGEX_FILTER_WHITE=["^重要通知", "(?i)whitelist"]`,
		`REGEX_FILTER_BLACK=["^垃圾短信", "(?i)spam"]`,
		`REGEX_CAPTCHA=["验证码[是为：:]\\s*(\\d{4,6})"]`,
		`REGEX_EMERGENCY=["(?i)urgent", "(?i)emergency", "严重告警"]`,
		"TIMEZONE=Asia/Shanghai",
		"LOG_LEVEL=info",
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("failed to create stdout pipe: %v", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start process: %v", err)
	}

	addrCh := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdoutPipe)
		re := regexp.MustCompile(`addr=(\S+)`)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.Contains(line, "starting HTTP webhook server") {
				matches := re.FindStringSubmatch(line)
				if len(matches) > 1 {
					addrCh <- matches[1]
					break
				}
			}
		}
	}()

	var serverAddr string
	select {
	case serverAddr = <-addrCh:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("timed out waiting for push-transfer to start")
	}

	_, port, err := net.SplitHostPort(serverAddr)
	if err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("failed to parse host port from %q: %v", serverAddr, err)
	}

	baseURL := fmt.Sprintf("http://127.0.0.1:%s", port)
	httpClient := &http.Client{Timeout: 5 * time.Second}

	// Health check probe
	hResp, err := httpClient.Get(baseURL + "/healthz")
	if err != nil || hResp.StatusCode != http.StatusOK {
		_ = cmd.Process.Kill()
		t.Fatalf("health probe failed on startup: %v", err)
	}
	_ = hResp.Body.Close()

	return &fullStackHarness{
		cmd:        cmd,
		baseURL:    baseURL,
		mockBark:   mockBark,
		mockTG:     mockTG,
		mockSMTP:   mockSMTP,
		httpClient: httpClient,
	}
}

func (h *fullStackHarness) stop(t testing.TB) {
	t.Helper()
	defer h.mockBark.close()
	defer h.mockTG.close()
	defer h.mockSMTP.close()

	if h.cmd != nil && h.cmd.Process != nil {
		_ = h.cmd.Process.Signal(syscall.SIGTERM)
		exitCh := make(chan error, 1)
		go func() { exitCh <- h.cmd.Wait() }()

		select {
		case err := <-exitCh:
			if err != nil {
				t.Errorf("expected clean process exit, got %v", err)
			}
		case <-time.After(5 * time.Second):
			_ = h.cmd.Process.Kill()
			t.Fatal("process failed to terminate gracefully on shutdown")
		}
	}
}

func (h *fullStackHarness) post(t *testing.T, body, authHeader string) (resp *http.Response, resBody string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.baseURL+"/webhook", strings.NewReader(body))
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}

	resp, err = h.httpClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestE2E_FullStack_AllChannels(t *testing.T) {
	h := startFullStackDaemon(t)
	defer h.stop(t)

	auth := "Bearer bearer-token-12345"

	// 1. Normal message: verify Bark, Telegram, and Email (SMTP) all receive the delivery
	t.Run("Normal SMS all-channel delivery", func(t *testing.T) {
		body := `{"msg_type":"text","title":"Alice","content":"Hello from full stack E2E test"}`
		resp, resBody := h.post(t, body, auth)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, resBody)
		}

		// Verify Bark (decrypted)
		barkPayloads := h.mockBark.getPayloads()
		if len(barkPayloads) != 1 {
			t.Fatalf("expected 1 Bark delivery, got %d", len(barkPayloads))
		}
		if barkPayloads[0]["body"] != "Hello from full stack E2E test" {
			t.Errorf("Bark body mismatch: %v", barkPayloads[0]["body"])
		}
		if barkPayloads[0]["title"] != "New Message From Alice" {
			t.Errorf("Bark title mismatch: %v", barkPayloads[0]["title"])
		}

		// Verify Telegram
		tgPayloads := h.mockTG.getPayloads()
		if len(tgPayloads) != 1 {
			t.Fatalf("expected 1 Telegram delivery, got %d", len(tgPayloads))
		}
		tgText, _ := tgPayloads[0]["text"].(string)
		if !strings.Contains(tgText, "Hello from full stack E2E test") {
			t.Errorf("Telegram message missing content: %s", tgText)
		}

		// Verify Email (real TLS SMTP conversation)
		emails := h.mockSMTP.getEmails()
		if len(emails) != 1 {
			t.Fatalf("expected 1 SMTP email received, got %d", len(emails))
		}
		if emails[0].From != "sender@example.com" {
			t.Errorf("SMTP from mismatch: %s", emails[0].From)
		}
		if emails[0].To != "receiver@example.com" {
			t.Errorf("SMTP to mismatch: %s", emails[0].To)
		}
		// Decode RFC 2047 subject if encoded
		subject := emails[0].Subject
		dec := new(mime.WordDecoder)
		if decodedSub, err := dec.DecodeHeader(subject); err == nil {
			subject = decodedSub
		}
		if subject != "New Message From Alice" {
			t.Errorf("SMTP subject mismatch: %q", subject)
		}
		if !strings.Contains(emails[0].Body, "Hello from full stack E2E test") {
			t.Errorf("SMTP body missing content: %s", emails[0].Body)
		}
	})

	// 2. Emergency notification: assert critical level and volume "5"
	t.Run("Emergency alert delivery", func(t *testing.T) {
		body := `{"msg_type":"text","title":"监控中心","content":"严重告警：数据库主库异常宕机！"}`
		resp, _ := h.post(t, body, auth)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}

		barkPayloads := h.mockBark.getPayloads()
		lastBark := barkPayloads[len(barkPayloads)-1]
		if lastBark["level"] != "critical" {
			t.Errorf("expected level critical, got %v", lastBark["level"])
		}
		if lastBark["volume"] != "5" {
			t.Errorf("expected volume \"5\", got %#v", lastBark["volume"])
		}
	})

	// 3. Call event
	t.Run("Call event delivery", func(t *testing.T) {
		body := `{"msg_type":"call","title":"10086","content":"未接来电"}`
		resp, _ := h.post(t, body, auth)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}

		barkPayloads := h.mockBark.getPayloads()
		lastBark := barkPayloads[len(barkPayloads)-1]
		if lastBark["title"] != "New Call From 10086" {
			t.Errorf("expected call title, got %v", lastBark["title"])
		}
	})

	// 4. AppNotification with metadata (battery, hostname, app_name)
	t.Run("App notification with device metadata", func(t *testing.T) {
		body := `{
			"msg_type": "app_notification",
			"app_name": "钉钉",
			"title": "项目群",
			"content": "评审会议将在 10 分钟后开始",
			"hostname": "Workstation-Alpha",
			"battery_level": "88%"
		}`
		resp, _ := h.post(t, body, auth)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}

		barkPayloads := h.mockBark.getPayloads()
		lastBark := barkPayloads[len(barkPayloads)-1]
		if lastBark["title"] != "[钉钉] 项目群" {
			t.Errorf("expected bark title '[钉钉] 项目群', got: %v", lastBark["title"])
		}
		if lastBark["group"] != "钉钉" {
			t.Errorf("expected bark group '钉钉', got: %v", lastBark["group"])
		}

		// Verify rich metadata in Telegram delivery
		tgPayloads := h.mockTG.getPayloads()
		lastTG := tgPayloads[len(tgPayloads)-1]
		tgText, _ := lastTG["text"].(string)
		if !strings.Contains(tgText, "当前电量: 88%") {
			t.Errorf("expected battery in Telegram text, got: %s", tgText)
		}
		if !strings.Contains(tgText, "设备主机名: Workstation-Alpha") {
			t.Errorf("expected hostname in Telegram text, got: %s", tgText)
		}

		// Verify rich metadata in SMTP delivery
		emails := h.mockSMTP.getEmails()
		lastEmail := emails[len(emails)-1]
		if !strings.Contains(lastEmail.Body, "当前电量: 88%") {
			t.Errorf("expected battery in Email body, got: %s", lastEmail.Body)
		}
		if !strings.Contains(lastEmail.Body, "设备主机名: Workstation-Alpha") {
			t.Errorf("expected hostname in Email body, got: %s", lastEmail.Body)
		}
	})
}

func TestE2E_FullStack_Authentication(t *testing.T) {
	h := startFullStackDaemon(t)
	defer h.stop(t)

	body := `{"msg_type":"text","title":"Auth","content":"Test"}`

	t.Run("Bearer Token Authentication Success", func(t *testing.T) {
		resp, _ := h.post(t, body, "Bearer bearer-token-12345")
		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200 for valid Bearer token, got %d", resp.StatusCode)
		}
	})

	t.Run("HTTP Basic Authentication Success", func(t *testing.T) {
		basicAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:secretpassword"))
		resp, _ := h.post(t, body, basicAuth)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200 for valid Basic auth, got %d", resp.StatusCode)
		}
	})

	t.Run("Missing Authorization Header returns 401 with WWW-Authenticate", func(t *testing.T) {
		resp, _ := h.post(t, body, "")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", resp.StatusCode)
		}
		authHeader := resp.Header.Get("WWW-Authenticate")
		if !strings.Contains(authHeader, `Basic realm="push-transfer"`) {
			t.Errorf("expected WWW-Authenticate Basic realm challenge, got %q", authHeader)
		}
	})

	t.Run("Invalid Bearer Token returns 401 with WWW-Authenticate", func(t *testing.T) {
		resp, _ := h.post(t, body, "Bearer wrong-token")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", resp.StatusCode)
		}
		authHeader := resp.Header.Get("WWW-Authenticate")
		if !strings.Contains(authHeader, `Basic realm="push-transfer"`) {
			t.Errorf("expected WWW-Authenticate challenge, got %q", authHeader)
		}
	})

	t.Run("Invalid Basic Password returns 401", func(t *testing.T) {
		basicAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:wrongpassword"))
		resp, _ := h.post(t, body, basicAuth)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", resp.StatusCode)
		}
	})
}

func TestE2E_FullStack_EdgeCasesAndResilience(t *testing.T) {
	h := startFullStackDaemon(t)
	defer h.stop(t)

	auth := "Bearer bearer-token-12345"

	t.Run("Payload Too Large returns 413", func(t *testing.T) {
		// Create a > 1MB JSON body
		largeContent := strings.Repeat("A", 1024*1024+100)
		body := fmt.Sprintf(`{"msg_type":"text","title":"Big","content":%q}`, largeContent)
		resp, _ := h.post(t, body, auth)
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Errorf("expected 413 Payload Too Large, got %d", resp.StatusCode)
		}
	})

	t.Run("Invalid msg_type returns 400", func(t *testing.T) {
		body := `{"msg_type":"unsupported_type","title":"Test","content":"Content"}`
		resp, _ := h.post(t, body, auth)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400 Bad Request, got %d", resp.StatusCode)
		}
	})

	t.Run("Malformed JSON returns 400", func(t *testing.T) {
		body := `{"msg_type":"text", "content":`
		resp, _ := h.post(t, body, auth)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400 Bad Request for broken JSON, got %d", resp.StatusCode)
		}
	})

	t.Run("Blacklist regex filters and silently drops message", func(t *testing.T) {
		initBarkCount := len(h.mockBark.getPayloads())
		body := `{"msg_type":"text","title":"垃圾短信","content":"垃圾短信：优惠大促销！"}`
		resp, _ := h.post(t, body, auth)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200 OK for filtered message, got %d", resp.StatusCode)
		}
		// Assert no new deliveries arrived at Bark
		if len(h.mockBark.getPayloads()) != initBarkCount {
			t.Errorf("expected blacklisted message to be dropped without delivery")
		}
	})

	t.Run("Security Injection String Defenses", func(t *testing.T) {
		maliciousContent := "'; DROP TABLE messages; -- `id` $(whoami)"
		maliciousTitle := "SecurityTest\r\nBcc: evil@attacker.com"
		body := fmt.Sprintf(`{"msg_type":"text","title":%q,"content":%q}`, maliciousTitle, maliciousContent)
		resp, _ := h.post(t, body, auth)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}

		// Verify SMTP header was NOT split by CRLF
		emails := h.mockSMTP.getEmails()
		lastEmail := emails[len(emails)-1]
		headersPart := strings.SplitN(lastEmail.Raw, "\r\n\r\n", 2)[0]
		for _, hLine := range strings.Split(headersPart, "\r\n") {
			if strings.HasPrefix(strings.ToLower(hLine), "bcc:") {
				t.Errorf("CRLF injection successfully created a Bcc header: %s", hLine)
			}
		}

		// Verify malicious shell commands in content are preserved verbatim as pure text
		if !strings.Contains(lastEmail.Body, maliciousContent) {
			t.Errorf("expected verbatim content in email body, got: %s", lastEmail.Body)
		}
	})
}
