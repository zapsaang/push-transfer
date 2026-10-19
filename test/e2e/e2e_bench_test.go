package e2e_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

type benchHarness struct {
	cmd        *exec.Cmd
	baseURL    string
	mockBark   *mockBarkServer
	mockTG     *mockTelegramServer
	httpClient *http.Client
}

func startBenchDaemon(t testing.TB) *benchHarness {
	t.Helper()

	bin := builtBinaryPath
	const barkKey = "1234567890123456"
	mockBark := newMockBarkServer(t, []byte(barkKey))
	mockTG := newMockTelegramServer(t)

	cmd := exec.Command(bin)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"PORT=0",
		"RUN_MODE=webhook",
		`AUTH_CREDENTIALS=[{"name":"bench-admin","token":"bench-secret-token"}]`,
		"BARK_DEVICE_KEY_LIST=dev-bench-1",
		"BARK_POST_URL=" + mockBark.url(),
		"AES_128_ECB_KEY=" + barkKey,
		"TELEGRAM_BOT_TOKEN=123:TG_BENCH",
		"TELEGRAM_CHAT_ID=10086",
		"TELEGRAM_API_URL=" + mockTG.server.URL,
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
	httpClient := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        1000,
			MaxIdleConnsPerHost: 200,
			IdleConnTimeout:     90 * time.Second,
		},
	}

	// Warmup health check probe
	hResp, err := httpClient.Get(baseURL + "/healthz")
	if err != nil || hResp.StatusCode != http.StatusOK {
		_ = cmd.Process.Kill()
		t.Fatalf("health probe failed on startup: %v", err)
	}
	_ = hResp.Body.Close()

	return &benchHarness{
		cmd:        cmd,
		baseURL:    baseURL,
		mockBark:   mockBark,
		mockTG:     mockTG,
		httpClient: httpClient,
	}
}

func (h *benchHarness) stop(t testing.TB) {
	t.Helper()
	defer h.mockBark.close()
	defer h.mockTG.close()

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

// runStressTest executes a mixed-traffic stress test at a specified concurrency and total request count.
func runStressTest(t *testing.T, h *benchHarness, concurrency, totalReqs int) {
	t.Helper()

	auth := "Bearer bench-secret-token"
	payloads := []struct {
		name string
		gen  func(id int64) string
	}{
		{
			name: "Normal SMS",
			gen: func(id int64) string {
				return fmt.Sprintf(`{"msg_type":"text","title":"10086","content":"您的话费余额已不足","idempotency_key":"norm-%d"}`, id)
			},
		},
		{
			name: "Captcha SMS",
			gen: func(id int64) string {
				return fmt.Sprintf(`{"msg_type":"text","title":"银行","content":"您的验证码是 889900，请在5分钟内输入。","idempotency_key":"cap-%d"}`, id)
			},
		},
		{
			name: "Emergency Alert",
			gen: func(id int64) string {
				return fmt.Sprintf(`{"msg_type":"text","title":"运维","content":"严重告警：机房温度超标","idempotency_key":"emg-%d"}`, id)
			},
		},
		{
			name: "Duplicate Idempotency",
			gen: func(id int64) string {
				return fmt.Sprintf(`{"msg_type":"text","title":"重试","content":"重试消息内容","idempotency_key":"dup-fixed-key-%d"}`, id%int64(concurrency))
			},
		},
	}

	var (
		wg          sync.WaitGroup
		successReqs int64
		failedReqs  int64
		latenciesMu sync.Mutex
		latencies   = make([]time.Duration, 0, totalReqs)
		curReqID    int64
	)

	var mBefore runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&mBefore)

	startTime := time.Now()
	reqsPerWorker := totalReqs / concurrency

	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < reqsPerWorker; i++ {
				id := atomic.AddInt64(&curReqID, 1)
				trafficType := payloads[id%int64(len(payloads))]
				body := trafficType.gen(id)

				req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, h.baseURL+"/webhook", strings.NewReader(body))
				if err != nil {
					atomic.AddInt64(&failedReqs, 1)
					continue
				}
				req.Header.Set("Authorization", auth)
				req.Header.Set("Content-Type", "application/json")

				t0 := time.Now()
				resp, err := h.httpClient.Do(req)
				dur := time.Since(t0)

				if err != nil || resp.StatusCode != http.StatusOK {
					atomic.AddInt64(&failedReqs, 1)
					if resp != nil {
						_ = resp.Body.Close()
					}
					continue
				}

				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()

				atomic.AddInt64(&successReqs, 1)
				latenciesMu.Lock()
				latencies = append(latencies, dur)
				latenciesMu.Unlock()
			}
		}()
	}

	wg.Wait()
	totalDuration := time.Since(startTime)

	var mAfter runtime.MemStats
	runtime.ReadMemStats(&mAfter)

	sort.Slice(latencies, func(i, j int) bool {
		return latencies[i] < latencies[j]
	})

	var (
		minLat  time.Duration
		maxLat  time.Duration
		meanLat time.Duration
		p50     time.Duration
		p90     time.Duration
		p95     time.Duration
		p99     time.Duration
		sumLat  time.Duration
	)

	if n := len(latencies); n > 0 {
		minLat = latencies[0]
		maxLat = latencies[n-1]
		for _, l := range latencies {
			sumLat += l
		}
		meanLat = sumLat / time.Duration(n)
		p50 = latencies[n*50/100]
		p90 = latencies[n*90/100]
		p95 = latencies[n*95/100]
		p99 = latencies[n*99/100]
	}

	qps := float64(successReqs) / totalDuration.Seconds()
	successRate := float64(successReqs) / float64(totalReqs) * 100.0

	report := fmt.Sprintf(`
================================================================================
          PUSH-TRANSFER 端到端高并发性能压测报告 (阶梯并发: %d)
================================================================================
【测试环境与配置】
  • 运行架构                     : Linux %s / %s (CPU: %d Cores)
  • 并发客户端数 (Concurrency)    : %d
  • 计划总请求数 (Planned Reqs)   : %d
  • 混合流量构成                 : 25%% Normal SMS / 25%% Captcha / 25%% Emergency / 25%% Duplicate
  • 下游出口通道                 : Bark (AES-128-CBC) + Telegram Bot API
  • 鉴权模式                     : Bearer Token (Constant-Time Sweeping)

【吞吐量与成功率】
  • 实际完成请求数 (Completed)    : %d
  • 成功请求数 (Successful)       : %d
  • 失败请求数 (Failed)           : %d
  • 成功率 (Success Rate)         : %.2f%%
  • 总测试耗时 (Total Duration)   : %v
  • 端到端吞吐量 (Throughput QPS) : %.2f req/s

【延迟分布 (Latency Distribution)】
  • 最小延迟 (Min Latency)        : %v
  • 平均延迟 (Mean Latency)       : %v
  • 50分位延迟 (P50 Median)       : %v
  • 90分位延迟 (P90)              : %v
  • 95分位延迟 (P95)              : %v
  • 99分位延迟 (P99)              : %v
  • 最大延迟 (Max Latency)        : %v

【内存分配与 GC 分析 (Runtime MemStats)】
  • 堆内存分配 (HeapAlloc)        : %.2f MB -> %.2f MB
  • 累计分配字节 (TotalAlloc)     : %.2f MB
  • 系统保留内存 (Sys Memory)     : %.2f MB
  • 垃圾回收次数 (NumGC Delta)    : %d 次
================================================================================
`,
		concurrency,
		runtime.GOOS,
		runtime.GOARCH,
		runtime.NumCPU(),
		concurrency,
		totalReqs,
		successReqs+failedReqs,
		successReqs,
		failedReqs,
		successRate,
		totalDuration.Round(time.Millisecond),
		qps,
		minLat.Round(time.Microsecond),
		meanLat.Round(time.Microsecond),
		p50.Round(time.Microsecond),
		p90.Round(time.Microsecond),
		p95.Round(time.Microsecond),
		p99.Round(time.Microsecond),
		maxLat.Round(time.Microsecond),
		float64(mBefore.HeapAlloc)/(1024*1024),
		float64(mAfter.HeapAlloc)/(1024*1024),
		float64(mAfter.TotalAlloc-mBefore.TotalAlloc)/(1024*1024),
		float64(mAfter.Sys)/(1024*1024),
		mAfter.NumGC-mBefore.NumGC,
	)

	t.Log(report)

	if failedReqs > 0 {
		t.Fatalf("stress test encountered %d failed requests", failedReqs)
	}
	if successRate < 99.9 {
		t.Fatalf("expected >= 99.9%% success rate, got %.2f%%", successRate)
	}
}

// TestE2E_HighConcurrency_Stress_Report executes a high-concurrency mixed-traffic
// stress test and generates a comprehensive performance report.
func TestE2E_HighConcurrency_Stress_Report(t *testing.T) {
	h := startBenchDaemon(t)
	defer h.stop(t)

	runStressTest(t, h, 20, 200)
}
