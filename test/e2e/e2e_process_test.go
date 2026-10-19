package e2e_test

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

var (
	builtBinaryPath string
)

func TestMain(m *testing.M) {
	tempDir, err := os.MkdirTemp("", "push-transfer-e2e-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create temp dir: %v\n", err)
		os.Exit(1)
	}

	binPath := filepath.Join(tempDir, "push-transfer")
	cmd := exec.Command("go", "build", "-trimpath", "-o", binPath, "../../cmd/push-transfer")
	out, err := cmd.CombinedOutput()
	if err != nil {
		_ = os.RemoveAll(tempDir)
		fmt.Fprintf(os.Stderr, "go build failed: %v (output: %s)\n", err, string(out))
		os.Exit(1)
	}

	builtBinaryPath = binPath
	code := m.Run()
	_ = os.RemoveAll(tempDir)
	os.Exit(code)
}

func TestProcess_CLI_Flags(t *testing.T) {
	bin := builtBinaryPath

	t.Run("-version flag exits with code 0", func(t *testing.T) {
		cmd := exec.Command(bin, "-version")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("expected exit code 0, got %v (output: %s)", err, string(out))
		}
		if !strings.Contains(string(out), "push-transfer version") {
			t.Errorf("expected version output, got %q", string(out))
		}
	})

	t.Run("-v flag exits with code 0", func(t *testing.T) {
		cmd := exec.Command(bin, "-v")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("expected exit code 0, got %v (output: %s)", err, string(out))
		}
		if !strings.Contains(string(out), "push-transfer version") {
			t.Errorf("expected version output, got %q", string(out))
		}
	})

	t.Run("-h flag exits with code 0", func(t *testing.T) {
		cmd := exec.Command(bin, "-h")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("expected exit code 0 for -h, got %v (output: %s)", err, string(out))
		}
		if !strings.Contains(string(out), "Usage of push-transfer") {
			t.Errorf("expected usage output, got %q", string(out))
		}
	})

	t.Run("invalid flag exits with non-zero code", func(t *testing.T) {
		cmd := exec.Command(bin, "--unknown-flag-xyz")
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("expected failure for unknown flag, got success (output: %s)", string(out))
		}
	})
}

func TestProcess_FailFast_Validations(t *testing.T) {
	bin := builtBinaryPath

	t.Run("missing RUN_MODE exits non-zero", func(t *testing.T) {
		cmd := exec.Command(bin)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatal("expected failure on missing RUN_MODE")
		}
		if !strings.Contains(string(out), "RUN_MODE is required") {
			t.Errorf("expected RUN_MODE error in output, got: %s", string(out))
		}
	})

	t.Run("missing AUTH_CREDENTIALS for webhook exits non-zero", func(t *testing.T) {
		cmd := exec.Command(bin)
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"),
			"RUN_MODE=webhook",
		}
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatal("expected failure on missing credentials")
		}
		if !strings.Contains(string(out), "AUTH_CREDENTIALS") {
			t.Errorf("expected AUTH_CREDENTIALS error in output, got: %s", string(out))
		}
	})

	t.Run("invalid TIMEZONE exits non-zero", func(t *testing.T) {
		cmd := exec.Command(bin)
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"),
			"RUN_MODE=webhook",
			`AUTH_CREDENTIALS=[{"name":"test","token":"sec-token-12345"}]`,
			"TIMEZONE=Invalid/Unknown_Zone",
		}
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatal("expected failure on invalid TIMEZONE")
		}
		if !strings.Contains(string(out), "TIMEZONE") {
			t.Errorf("expected TIMEZONE error in output, got: %s", string(out))
		}
	})
}

func TestProcess_FullLifecycle_GracefulShutdown(t *testing.T) {
	bin := builtBinaryPath

	// 1. Setup mock downstream servers
	const barkKey = "1234567890123456"
	mockBark := newMockBarkServer(t, []byte(barkKey))
	defer mockBark.close()

	mockTG := newMockTelegramServer(t)
	defer mockTG.close()

	// 2. Prepare environment and start child process
	cmd := exec.Command(bin)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"PORT=0", // OS ephemeral port
		"RUN_MODE=webhook",
		`AUTH_CREDENTIALS=[{"name":"admin-token","token":"e2e-token-xyz"}]`,
		"BARK_DEVICE_KEY_LIST=dev-e2e-1",
		"BARK_POST_URL=" + mockBark.url(),
		"AES_128_ECB_KEY=" + barkKey,
		"TELEGRAM_BOT_TOKEN=123:ABC",
		"TELEGRAM_CHAT_ID=10086",
		"TELEGRAM_API_URL=" + mockTG.server.URL,
		`REGEX_CAPTCHA=["验证码[是为：:]\\s*(\\d{4,6})"]`,
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

	// 3. Scan stdout to detect server listening address
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
		t.Fatal("timed out waiting for child process to listen")
	}

	_, port, err := net.SplitHostPort(serverAddr)
	if err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("failed to parse host port from %q: %v", serverAddr, err)
	}
	baseURL := fmt.Sprintf("http://127.0.0.1:%s", port)
	httpClient := &http.Client{Timeout: 5 * time.Second}

	// 4. Verify /healthz
	resp, err := httpClient.Get(baseURL + "/healthz")
	if err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("health check failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz expected 200, got %d", resp.StatusCode)
	}

	// 5. Verify /webhook delivery with Captcha extraction & AES encryption
	postBody := `{"msg_type":"text","title":"银行服务","content":"您的验证码是 654321，5分钟内有效。","idempotency_key":"e2e-key-100"}`
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/webhook", strings.NewReader(postBody))
	req.Header.Set("Authorization", "Bearer e2e-token-xyz")
	req.Header.Set("Content-Type", "application/json")

	resp, err = httpClient.Do(req)
	if err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("POST /webhook failed: %v", err)
	}
	respBytes, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d (body: %s)", resp.StatusCode, string(respBytes))
	}

	// Check Mock Bark received the message, decrypted, and extracted captcha
	barkPayloads := mockBark.getPayloads()
	if len(barkPayloads) != 1 {
		t.Fatalf("expected 1 Bark payload, got %d", len(barkPayloads))
	}
	t.Logf("received Bark payload: %#v", barkPayloads[0])
	if barkPayloads[0]["copy"] != "654321" {
		t.Errorf("expected copy 654321, got %v", barkPayloads[0]["copy"])
	}
	if barkPayloads[0]["autoCopy"] != float64(1) {
		t.Errorf("expected autoCopy 1, got %v", barkPayloads[0]["autoCopy"])
	}

	// 6. Test Idempotency: re-send same idempotency_key
	req2, _ := http.NewRequest(http.MethodPost, baseURL+"/webhook", strings.NewReader(postBody))
	req2.Header.Set("Authorization", "Bearer e2e-token-xyz")
	req2.Header.Set("Content-Type", "application/json")

	resp2, err := httpClient.Do(req2)
	if err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("POST /webhook second attempt failed: %v", err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("expected 200 on duplicate, got %d", resp2.StatusCode)
	}

	// Downstream should NOT receive duplicate
	barkPayloads2 := mockBark.getPayloads()
	if len(barkPayloads2) != 1 {
		t.Errorf("downstream received duplicate notification: got %d payloads, want 1", len(barkPayloads2))
	}

	// 7. Send SIGTERM and assert clean graceful shutdown (ExitCode 0)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("failed to send SIGTERM: %v", err)
	}

	exitCh := make(chan error, 1)
	go func() {
		exitCh <- cmd.Wait()
	}()

	select {
	case err := <-exitCh:
		if err != nil {
			t.Fatalf("process exited with non-zero error: %v", err)
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("process failed to gracefully terminate within 5 seconds")
	}
}
