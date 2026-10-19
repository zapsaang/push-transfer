package e2e_test

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestE2E_IMAP_TriggerFlow(t *testing.T) {
	bin := builtBinaryPath
	pki := setupTestPKI(t)

	mockBark := newMockBarkServer(t, nil) // plaintext bark
	defer mockBark.close()

	mockIMAP := newMockIMAPServer(t, pki)
	defer mockIMAP.close()

	// Pre-seed an unread trigger email
	triggerUID := mockIMAP.addMessage(
		"PushTransfer Email Trigger",
		`{"msg_type":"text","title":"银行通知","content":"您的验证码是 778899，请妥善保管。"}`,
	)

	// Pre-seed an ordinary non-trigger email
	nonTriggerUID := mockIMAP.addMessage(
		"Weekly Team Newsletter",
		`Hello team, here is the weekly update.`,
	)

	cmd := exec.Command(bin)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"RUN_MODE=imap",
		"SSL_CERT_FILE=" + pki.caFilePath,
		"EMAIL_TRIGGER_LOGIN_EMAIL=imap-user@example.com",
		"EMAIL_TRIGGER_PASSWORD=imapsecret",
		"EMAIL_TRIGGER_IMAP_SERVER=127.0.0.1",
		fmt.Sprintf("EMAIL_TRIGGER_IMAP_PORT=%d", mockIMAP.port()),
		"EMAIL_TRIGGER_MAILBOX=INBOX",
		"BARK_DEVICE_KEY_LIST=dev-imap-test",
		"BARK_POST_URL=" + mockBark.url(),
		`REGEX_CAPTCHA=["验证码[是为：:]\\s*(\\d{4,6})"]`,
		"LOG_LEVEL=debug",
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("failed to create stdout pipe: %v", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start IMAP worker process: %v", err)
	}

	// Wait until the listener is connected or log output shows startup
	connectedCh := make(chan struct{}, 1)
	go func() {
		scanner := bufio.NewScanner(stdoutPipe)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.Contains(line, "starting IMAP email trigger listener") {
				select {
				case connectedCh <- struct{}{}:
				default:
				}
			}
		}
	}()

	select {
	case <-connectedCh:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("timed out waiting for IMAP listener to start")
	}

	// Wait for IMAP consumption and downstream delivery
	deadline := time.Now().Add(5 * time.Second)
	delivered := false
	for time.Now().Before(deadline) {
		payloads := mockBark.getPayloads()
		if len(payloads) > 0 {
			delivered = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !delivered {
		_ = cmd.Process.Kill()
		t.Fatal("timed out waiting for IMAP trigger message to be delivered to Bark")
	}

	// 1. Assert downstream delivery content
	barkPayloads := mockBark.getPayloads()
	if len(barkPayloads) != 1 {
		t.Fatalf("expected exactly 1 Bark delivery, got %d", len(barkPayloads))
	}
	if barkPayloads[0]["copy"] != "778899" {
		t.Errorf("expected extracted captcha 778899, got %v", barkPayloads[0]["copy"])
	}

	// 2. Assert seen flags on IMAP server
	seenUIDs := mockIMAP.getSeenUIDs()
	foundTrigger := false
	foundNonTrigger := false
	for _, uid := range seenUIDs {
		if uid == triggerUID {
			foundTrigger = true
		}
		if uid == nonTriggerUID {
			foundNonTrigger = true
		}
	}

	if !foundTrigger {
		t.Errorf("expected trigger email UID %d to be marked \\Seen", triggerUID)
	}
	if foundNonTrigger {
		t.Errorf("non-trigger email UID %d must NOT be marked \\Seen", nonTriggerUID)
	}

	// 3. Graceful shutdown
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("failed to send SIGTERM: %v", err)
	}

	exitCh := make(chan error, 1)
	go func() { exitCh <- cmd.Wait() }()

	select {
	case err := <-exitCh:
		if err != nil {
			t.Errorf("expected clean exit for IMAP process, got %v", err)
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("process failed to terminate within 5 seconds")
	}
}
