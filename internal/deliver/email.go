package deliver

import (
	"context"
	"crypto/tls"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strings"
	"time"

	"github.com/zapsaang/push-transfer/internal/model"
)

// EmailOptions configures EmailDeliver.
type EmailOptions struct {
	SenderEmail   string
	ReceiverEmail string
	Password      string
	SMTPServer    string
	SMTPPort      int
	Timeout       time.Duration
	CustomDialer  func(ctx context.Context, network, addr string) (net.Conn, error)
}

// EmailDeliver delivers notifications via SMTP SSL.
type EmailDeliver struct {
	senderEmail   string
	receiverEmail string
	password      string
	smtpServer    string
	smtpPort      int
	timeout       time.Duration
	customDialer  func(ctx context.Context, network, addr string) (net.Conn, error)
}

// NewEmailDeliver creates an EmailDeliver client.
func NewEmailDeliver(opts EmailOptions) *EmailDeliver {
	port := opts.SMTPPort
	if port <= 0 {
		port = 465
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &EmailDeliver{
		senderEmail:   opts.SenderEmail,
		receiverEmail: opts.ReceiverEmail,
		password:      opts.Password,
		smtpServer:    opts.SMTPServer,
		smtpPort:      port,
		timeout:       timeout,
		customDialer:  opts.CustomDialer,
	}
}

// Name returns the identifier for EmailDeliver.
func (e *EmailDeliver) Name() string {
	return "email"
}

func sanitizeHeaderValue(s string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(s)
}

func encodeRFC2047Subject(s string) string {
	s = sanitizeHeaderValue(s)
	needsEncoding := strings.Contains(s, "=?")
	if !needsEncoding {
		for i := 0; i < len(s); i++ {
			if s[i] > 127 || s[i] < 32 {
				needsEncoding = true
				break
			}
		}
	}
	if needsEncoding {
		return mime.QEncoding.Encode("UTF-8", s)
	}
	return s
}

// Send formats and transmits msg via SMTP SSL.
func (e *EmailDeliver) Send(ctx context.Context, msg *model.Message) error {
	if msg == nil {
		return nil
	}

	var subject string
	switch msg.MsgType {
	case "call":
		subject = fmt.Sprintf("New Call From %s", msg.Title)
	case "text":
		subject = fmt.Sprintf("New Message From %s", msg.Title)
	default:
		subject = msg.Title
	}

	sender := sanitizeHeaderValue(e.senderEmail)
	receiver := sanitizeHeaderValue(e.receiverEmail)
	body := msg.GenComplexText()
	headers := [][2]string{
		{"From", sender},
		{"To", receiver},
		{"Subject", encodeRFC2047Subject(subject)},
		{"MIME-Version", "1.0"},
		{"Content-Type", "text/plain; charset=UTF-8"},
	}

	var message strings.Builder
	for _, h := range headers {
		fmt.Fprintf(&message, "%s: %s\r\n", h[0], h[1])
	}
	message.WriteString("\r\n")
	message.WriteString(body)

	addr := fmt.Sprintf("%s:%d", e.smtpServer, e.smtpPort)

	dialCtx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	var conn net.Conn
	var err error
	if e.customDialer != nil {
		conn, err = e.customDialer(dialCtx, "tcp", addr)
	} else {
		tlsDialer := &tls.Dialer{
			Config: &tls.Config{
				ServerName: e.smtpServer,
			},
		}
		conn, err = tlsDialer.DialContext(dialCtx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("failed to dial SMTP server %s: %w", addr, err)
	}
	defer func() { _ = conn.Close() }()

	if deadline, ok := dialCtx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	stopCancel := context.AfterFunc(dialCtx, func() {
		_ = conn.SetDeadline(time.Now())
	})
	defer stopCancel()

	client, err := smtp.NewClient(conn, e.smtpServer)
	if err != nil {
		return fmt.Errorf("failed to create SMTP client: %w", err)
	}
	defer func() { _ = client.Close() }()

	if e.password != "" {
		auth := smtp.PlainAuth("", sender, e.password, e.smtpServer)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("SMTP auth failed: %w", err)
		}
	}

	if err := client.Mail(sender); err != nil {
		return fmt.Errorf("SMTP MAIL command failed: %w", err)
	}
	if err := client.Rcpt(receiver); err != nil {
		return fmt.Errorf("SMTP RCPT command failed: %w", err)
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("SMTP DATA command failed: %w", err)
	}
	if _, err := w.Write([]byte(message.String())); err != nil {
		_ = w.Close()
		return fmt.Errorf("failed to write email body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("failed to close data writer: %w", err)
	}

	_ = client.Quit()
	return nil
}
