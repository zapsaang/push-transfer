package e2e_test

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zapsaang/push-transfer/internal/crypto"
)

// mockBarkServer intercepts Bark push notifications and supports automatic AES decryption.
type mockBarkServer struct {
	server    *httptest.Server
	cryptoKey []byte
	mu        sync.Mutex
	payloads  []map[string]interface{}
}

func newMockBarkServer(t testing.TB, cryptoKey []byte) *mockBarkServer {
	t.Helper()
	m := &mockBarkServer{cryptoKey: cryptoKey}
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer func() { _ = r.Body.Close() }()

		var raw map[string]interface{}
		if err := json.Unmarshal(body, &raw); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// If payload is encrypted, decrypt it if key is available
		if cipherText, ok := raw["ciphertext"].(string); ok && len(m.cryptoKey) > 0 {
			iv, _ := raw["iv"].(string)
			plain, err := crypto.DecryptBarkCBC(cipherText, m.cryptoKey, iv)
			if err == nil {
				var decrypted map[string]interface{}
				if err := json.Unmarshal(plain, &decrypted); err == nil {
					// retain device_key if present on envelope
					if devKey, hasDev := raw["device_key"]; hasDev {
						decrypted["device_key"] = devKey
					}
					raw = decrypted
				}
			}
		}

		m.mu.Lock()
		m.payloads = append(m.payloads, raw)
		m.mu.Unlock()

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":200,"message":"success"}`))
	}))
	return m
}

func (m *mockBarkServer) close() {
	if m.server != nil {
		m.server.Close()
	}
}

func (m *mockBarkServer) url() string {
	return m.server.URL + "/push"
}

func (m *mockBarkServer) getPayloads() []map[string]interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	res := make([]map[string]interface{}, len(m.payloads))
	copy(res, m.payloads)
	return res
}

// mockTelegramServer intercepts Telegram bot messages.
type mockTelegramServer struct {
	server   *httptest.Server
	mu       sync.Mutex
	payloads []map[string]interface{}
}

func newMockTelegramServer(t testing.TB) *mockTelegramServer {
	t.Helper()
	m := &mockTelegramServer{}
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/bot") || !strings.HasSuffix(r.URL.Path, "/sendMessage") {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer func() { _ = r.Body.Close() }()

		var payload map[string]interface{}
		if err := json.Unmarshal(body, &payload); err == nil {
			m.mu.Lock()
			m.payloads = append(m.payloads, payload)
			m.mu.Unlock()
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":12345}}`))
	}))
	return m
}

func (m *mockTelegramServer) close() {
	if m.server != nil {
		m.server.Close()
	}
}

func (m *mockTelegramServer) getPayloads() []map[string]interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	res := make([]map[string]interface{}, len(m.payloads))
	copy(res, m.payloads)
	return res
}

// receivedEmail captures an email received by mockSMTPServer.
type receivedEmail struct {
	From    string
	To      string
	Subject string
	Body    string
	Raw     string
}

// mockSMTPServer is a real TLS-listening SMTP server.
type mockSMTPServer struct {
	listener net.Listener
	mu       sync.Mutex
	emails   []receivedEmail
	doneCh   chan struct{}
}

func newMockSMTPServer(t testing.TB, pki *testPKI) *mockSMTPServer {
	t.Helper()

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{pki.serverCertTLS},
		MinVersion:   tls.VersionTLS12,
	}

	listener, err := tls.Listen("tcp", "127.0.0.1:0", tlsConfig)
	if err != nil {
		t.Fatalf("failed to start TLS SMTP mock listener: %v", err)
	}

	m := &mockSMTPServer{
		listener: listener,
		doneCh:   make(chan struct{}),
	}

	go m.serve()
	return m
}

func (m *mockSMTPServer) port() int {
	return m.listener.Addr().(*net.TCPAddr).Port
}

func (m *mockSMTPServer) close() {
	_ = m.listener.Close()
	<-m.doneCh
}

func (m *mockSMTPServer) getEmails() []receivedEmail {
	m.mu.Lock()
	defer m.mu.Unlock()
	res := make([]receivedEmail, len(m.emails))
	copy(res, m.emails)
	return res
}

func (m *mockSMTPServer) serve() {
	defer close(m.doneCh)
	for {
		conn, err := m.listener.Accept()
		if err != nil {
			return
		}
		go m.handleConn(conn)
	}
}

func (m *mockSMTPServer) handleConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	writeLine := func(s string) {
		_, _ = w.WriteString(s)
		_ = w.Flush()
	}

	writeLine("220 127.0.0.1 ESMTP Mock Ready\r\n")

	var currentEmail receivedEmail
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")

		switch {
		case strings.HasPrefix(line, "EHLO") || strings.HasPrefix(line, "HELO"):
			writeLine("250-127.0.0.1\r\n250 AUTH PLAIN\r\n")
		case strings.HasPrefix(line, "AUTH PLAIN"):
			writeLine("235 2.7.0 Authentication successful\r\n")
		case strings.HasPrefix(line, "MAIL FROM:"):
			currentEmail.From = strings.TrimPrefix(line, "MAIL FROM:")
			currentEmail.From = strings.Trim(currentEmail.From, "<> ")
			writeLine("250 2.1.0 Ok\r\n")
		case strings.HasPrefix(line, "RCPT TO:"):
			currentEmail.To = strings.TrimPrefix(line, "RCPT TO:")
			currentEmail.To = strings.Trim(currentEmail.To, "<> ")
			writeLine("250 2.1.5 Ok\r\n")
		case line == "DATA":
			writeLine("354 End data with <CR><LF>.<CR><LF>\r\n")
			var dataBuilder strings.Builder
			for {
				dataLine, errData := r.ReadString('\n')
				if errData != nil {
					return
				}
				if dataLine == ".\r\n" {
					writeLine("250 2.0.0 Ok: queued\r\n")
					break
				}
				dataBuilder.WriteString(dataLine)
			}
			currentEmail.Raw = dataBuilder.String()
			// Extract Subject and Body
			parts := strings.SplitN(currentEmail.Raw, "\r\n\r\n", 2)
			headers := parts[0]
			if len(parts) > 1 {
				currentEmail.Body = parts[1]
			}
			for _, hLine := range strings.Split(headers, "\r\n") {
				if strings.HasPrefix(strings.ToLower(hLine), "subject:") {
					currentEmail.Subject = strings.TrimSpace(hLine[8:])
				}
			}

			m.mu.Lock()
			m.emails = append(m.emails, currentEmail)
			m.mu.Unlock()
			currentEmail = receivedEmail{}
		case line == "QUIT":
			writeLine("221 2.0.0 Bye\r\n")
			return
		default:
			writeLine("500 Command unrecognized\r\n")
		}
	}
}

// mockIMAPMessage represents an email message stored in the mock IMAP server.
type mockIMAPMessage struct {
	UID       uint32
	Seen      bool
	Subject   string
	RawRFC822 string
}

// mockIMAPServer is a real TLS-listening IMAPv4rev1 server supporting IDLE.
type mockIMAPServer struct {
	listener net.Listener
	mu       sync.Mutex
	messages []mockIMAPMessage
	seenUIDs []uint32
	doneCh   chan struct{}
	idleConn net.Conn
	idleWake chan struct{}
}

func newMockIMAPServer(t *testing.T, pki *testPKI) *mockIMAPServer {
	t.Helper()

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{pki.serverCertTLS},
		MinVersion:   tls.VersionTLS12,
	}

	listener, err := tls.Listen("tcp", "127.0.0.1:0", tlsConfig)
	if err != nil {
		t.Fatalf("failed to start TLS IMAP mock listener: %v", err)
	}

	m := &mockIMAPServer{
		listener: listener,
		doneCh:   make(chan struct{}),
		idleWake: make(chan struct{}, 10),
	}

	go m.serve()
	return m
}

func (m *mockIMAPServer) port() int {
	return m.listener.Addr().(*net.TCPAddr).Port
}

func (m *mockIMAPServer) close() {
	_ = m.listener.Close()
	m.mu.Lock()
	if m.idleConn != nil {
		_ = m.idleConn.Close()
	}
	m.mu.Unlock()
	<-m.doneCh
}

func (m *mockIMAPServer) addMessage(subject, jsonBody string) uint32 {
	m.mu.Lock()
	defer m.mu.Unlock()

	uid := uint32(len(m.messages) + 100)
	raw := fmt.Sprintf("From: sender@example.com\r\nTo: recipient@example.com\r\nSubject: %s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s",
		subject, jsonBody)

	m.messages = append(m.messages, mockIMAPMessage{
		UID:       uid,
		Seen:      false,
		Subject:   subject,
		RawRFC822: raw,
	})

	select {
	case m.idleWake <- struct{}{}:
	default:
	}

	return uid
}

func (m *mockIMAPServer) getSeenUIDs() []uint32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	res := make([]uint32, len(m.seenUIDs))
	copy(res, m.seenUIDs)
	return res
}

func (m *mockIMAPServer) serve() {
	defer close(m.doneCh)
	for {
		conn, err := m.listener.Accept()
		if err != nil {
			return
		}
		go m.handleConn(conn)
	}
}

func (m *mockIMAPServer) handleConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	writeLine := func(s string) {
		_, _ = w.WriteString(s)
		_ = w.Flush()
	}

	// 1. Initial greeting
	writeLine("* OK [CAPABILITY IMAP4rev1 IDLE] Mock IMAP4rev1 Server Ready\r\n")

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
			m.mu.Lock()
			msgCount := len(m.messages)
			m.mu.Unlock()
			writeLine(fmt.Sprintf("* %d EXISTS\r\n* 0 RECENT\r\n* OK [UIDVALIDITY 1] UIDs valid\r\n* FLAGS (\\Seen)\r\n* OK [PERMANENTFLAGS (\\Seen)] Flags permitted\r\n%s OK [READ-WRITE] SELECT completed\r\n", msgCount, tag))
		case strings.HasPrefix(upperCmd, "UID SEARCH"):
			m.mu.Lock()
			var unseenUIDs []string
			for _, msg := range m.messages {
				if !msg.Seen {
					unseenUIDs = append(unseenUIDs, fmt.Sprintf("%d", msg.UID))
				}
			}
			m.mu.Unlock()
			if len(unseenUIDs) > 0 {
				writeLine(fmt.Sprintf("* SEARCH %s\r\n%s OK UID SEARCH completed\r\n", strings.Join(unseenUIDs, " "), tag))
			} else {
				writeLine(fmt.Sprintf("* SEARCH\r\n%s OK UID SEARCH completed\r\n", tag))
			}
		case strings.HasPrefix(upperCmd, "UID FETCH"):
			// Parse requested UID
			m.mu.Lock()
			for _, msg := range m.messages {
				if !strings.Contains(cmd, fmt.Sprintf("%d", msg.UID)) {
					continue
				}
				flags := ""
				if msg.Seen {
					flags = ` \Seen`
				}
				fetchResp := fmt.Sprintf("* 1 FETCH (UID %d FLAGS (%s) BODY[] {%d}\r\n%s)\r\n",
					msg.UID, flags, len(msg.RawRFC822), msg.RawRFC822)
				writeLine(fetchResp)
				break
			}
			m.mu.Unlock()
			writeLine(tag + " OK UID FETCH completed\r\n")
		case strings.HasPrefix(upperCmd, "UID STORE"):
			m.mu.Lock()
			for i := range m.messages {
				if strings.Contains(cmd, fmt.Sprintf("%d", m.messages[i].UID)) {
					if strings.Contains(upperCmd, `+FLAGS (\SEEN)`) || strings.Contains(upperCmd, `\SEEN`) {
						m.messages[i].Seen = true
						m.seenUIDs = append(m.seenUIDs, m.messages[i].UID)
					}
				}
			}
			m.mu.Unlock()
			writeLine(tag + " OK UID STORE completed\r\n")
		case strings.HasPrefix(upperCmd, "IDLE"):
			m.mu.Lock()
			m.idleConn = conn
			m.mu.Unlock()

			writeLine("+ idling\r\n")

			// Wait for either client DONE or internal wakeup (new mail)
			doneCh := make(chan string, 1)
			go func() {
				doneStr, _ := r.ReadString('\n')
				doneCh <- strings.TrimRight(doneStr, "\r\n")
			}()

			select {
			case d := <-doneCh:
				if strings.EqualFold(d, "DONE") {
					writeLine(tag + " OK IDLE completed\r\n")
				}
			case <-m.idleWake:
				// Send unilateral exists update
				m.mu.Lock()
				count := len(m.messages)
				m.mu.Unlock()
				writeLine(fmt.Sprintf("* %d EXISTS\r\n", count))
				// Wait for client to send DONE
				d := <-doneCh
				if strings.EqualFold(d, "DONE") {
					writeLine(tag + " OK IDLE completed\r\n")
				}
			}

			m.mu.Lock()
			m.idleConn = nil
			m.mu.Unlock()

		case strings.HasPrefix(upperCmd, "LOGOUT"):
			writeLine("* BYE IMAP4rev1 Server logging out\r\n" + tag + " OK LOGOUT completed\r\n")
			return
		default:
			writeLine(tag + " OK " + cmd + " completed\r\n")
		}
	}
}
