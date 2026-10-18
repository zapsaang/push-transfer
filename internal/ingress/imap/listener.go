// Package imap implements IMAP email polling and IDLE event listening for push-transfer.
package imap

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"fmt"
	"log/slog"
	"math/big"
	"mime"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/charset"
	"github.com/emersion/go-message/mail"
	"github.com/zapsaang/push-transfer/internal/model"
)

// CalculateBackoff calculates exponential backoff with +/-20% jitter.
// t = min(max, min * 2^attempt) +/- jitter
func CalculateBackoff(attempt int, minBackoff, maxBackoff time.Duration) time.Duration {
	if minBackoff <= 0 {
		minBackoff = 1 * time.Second
	}
	if maxBackoff < minBackoff {
		maxBackoff = minBackoff
	}

	factor := 1 << attempt
	if attempt > 30 || factor <= 0 {
		factor = 1 << 30
	}

	dur := minBackoff * time.Duration(factor)
	if dur > maxBackoff || dur <= 0 {
		dur = maxBackoff
	}

	// Jitter +/- 20%
	n, err := rand.Int(rand.Reader, big.NewInt(1000))
	if err == nil {
		jitterFrac := 0.2
		val := (float64(n.Int64())/1000.0)*2*jitterFrac - jitterFrac
		dur = time.Duration(float64(dur) * (1.0 + val))
	}

	if dur > maxBackoff {
		dur = maxBackoff
	}
	if dur < minBackoff/2 {
		dur = minBackoff / 2
	}
	return dur
}

// MessageHandler is the callback for handling incoming trigger messages.
type MessageHandler func(ctx context.Context, msg *model.Message) error

// ListenerOptions configures the IMAP listener.
type ListenerOptions struct {
	IMAPServer        string
	IMAPPort          int
	LoginEmail        string
	Password          string
	Mailbox           string
	Handler           MessageHandler
	DialConn          func(ctx context.Context, addr string) (net.Conn, error)
	DialClient        func(ctx context.Context, addr string) (*imapclient.Client, error)
	DialTimeout       time.Duration // default: 10 * time.Second
	HeartbeatInterval time.Duration // default: 10 * time.Minute
	PollInterval      time.Duration // default: 30 * time.Second
	MinBackoff        time.Duration // default: 1 * time.Second
	MaxBackoff        time.Duration // default: 60 * time.Second
	Logger            *slog.Logger
}

// Listener monitors an IMAP inbox using IDLE or polling and dispatches matching messages.
type Listener struct {
	opts   ListenerOptions
	logger *slog.Logger
}

// NewListener creates a new IMAP Listener.
func NewListener(opts ListenerOptions) *Listener {
	opts.Mailbox = strings.TrimSpace(opts.Mailbox)
	if opts.Mailbox == "" {
		opts.Mailbox = "INBOX"
	}
	if opts.DialTimeout <= 0 {
		opts.DialTimeout = 10 * time.Second
	}
	if opts.HeartbeatInterval <= 0 {
		opts.HeartbeatInterval = 10 * time.Minute
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 30 * time.Second
	}
	if opts.MinBackoff <= 0 {
		opts.MinBackoff = 1 * time.Second
	}
	if opts.MaxBackoff <= 0 {
		opts.MaxBackoff = 60 * time.Second
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Listener{
		opts:   opts,
		logger: logger,
	}
}

// Start begins listening on the IMAP server. Blocks until ctx is canceled.
func (l *Listener) Start(ctx context.Context) error {
	attempt := 0

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		sessionEstablished, err := l.connectAndListen(ctx)
		if ctx.Err() != nil {
			return nil //nolint:nilerr // expected clean termination on context cancellation
		}

		if sessionEstablished {
			attempt = 0
		}

		if err != nil {
			l.logger.Warn("imap listener error, reconnecting", "error", err, "attempt", attempt)
		}

		backoff := CalculateBackoff(attempt, l.opts.MinBackoff, l.opts.MaxBackoff)
		attempt++

		l.logger.Info("waiting before reconnecting to imap", "backoff", backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func (l *Listener) connectAndListen(ctx context.Context) (sessionEstablished bool, err error) {
	addr := fmt.Sprintf("%s:%d", l.opts.IMAPServer, l.opts.IMAPPort)

	updateCh := make(chan struct{}, 1)
	clientOpts := &imapclient.Options{
		WordDecoder: &mime.WordDecoder{CharsetReader: charset.Reader},
		UnilateralDataHandler: &imapclient.UnilateralDataHandler{
			Mailbox: func(_ *imapclient.UnilateralDataMailbox) {
				select {
				case updateCh <- struct{}{}:
				default:
				}
			},
		},
	}

	var client *imapclient.Client

	switch {
	case l.opts.DialConn != nil:
		conn, dialErr := l.opts.DialConn(ctx, addr)
		if dialErr != nil {
			return false, fmt.Errorf("dial failed: %w", dialErr)
		}
		client = imapclient.New(conn, clientOpts)
	case l.opts.DialClient != nil:
		client, err = l.opts.DialClient(ctx, addr)
		if err != nil {
			return false, fmt.Errorf("dial failed: %w", err)
		}
	default:
		tlsDialer := &tls.Dialer{
			NetDialer: &net.Dialer{Timeout: l.opts.DialTimeout},
		}
		conn, dialErr := tlsDialer.DialContext(ctx, "tcp", addr)
		if dialErr != nil {
			return false, fmt.Errorf("imap dial tls failed: %w", dialErr)
		}
		client = imapclient.New(conn, clientOpts)
	}
	defer func() { _ = client.Close() }()

	var (
		cancelMu        sync.Mutex
		forceCloseTimer *time.Timer
	)
	stopCancel := context.AfterFunc(ctx, func() {
		cancelMu.Lock()
		defer cancelMu.Unlock()
		forceCloseTimer = time.AfterFunc(150*time.Millisecond, func() {
			_ = client.Close()
		})
	})
	defer func() {
		stopCancel()
		cancelMu.Lock()
		if forceCloseTimer != nil {
			forceCloseTimer.Stop()
		}
		cancelMu.Unlock()
	}()

	handshakeTimer := time.AfterFunc(l.opts.DialTimeout, func() {
		_ = client.Close()
	})
	defer handshakeTimer.Stop()

	if err := client.Login(l.opts.LoginEmail, l.opts.Password).Wait(); err != nil {
		return false, fmt.Errorf("login failed: %w", err)
	}
	defer func() {
		if ctx.Err() == nil {
			logoutTimer := time.AfterFunc(150*time.Millisecond, func() {
				_ = client.Close()
			})
			defer logoutTimer.Stop()
			_ = client.Logout().Wait()
		}
	}()

	if _, err := client.Select(l.opts.Mailbox, nil).Wait(); err != nil {
		return false, fmt.Errorf("select mailbox failed: %w", err)
	}
	handshakeTimer.Stop()

	l.logger.Info("imap connected and mailbox selected", "server", addr, "email", l.opts.LoginEmail, "mailbox", l.opts.Mailbox)

	// Process initial unseen messages using UIDs
	hadHandlerErr, err := l.processUnseen(ctx, client)
	if err != nil {
		return false, fmt.Errorf("failed to process initial unseen messages: %w", err)
	}
	retryAttempt := 0
	if hadHandlerErr {
		retryAttempt = 1
	}

	hasIdle := client.Caps().Has(imap.CapIdle)
	if !hasIdle {
		l.logger.Info("imap server does not advertise IDLE, falling back to periodic polling", "interval", l.opts.PollInterval)
		sessionEstablished = true

		for {
			waitDur := l.opts.PollInterval
			if retryAttempt > 0 {
				retryDelay := CalculateBackoff(retryAttempt-1, l.opts.MinBackoff, l.opts.MaxBackoff)
				if retryDelay < waitDur {
					waitDur = retryDelay
				}
			}
			waitTimer := time.NewTimer(waitDur)
			select {
			case <-ctx.Done():
				waitTimer.Stop()
				return sessionEstablished, nil
			case <-waitTimer.C:
				if err := client.Noop().Wait(); err != nil {
					return sessionEstablished, fmt.Errorf("polling NOOP failed: %w", err)
				}
				hadErr, err := l.processUnseen(ctx, client)
				if err != nil {
					return sessionEstablished, fmt.Errorf("polling process unseen failed: %w", err)
				}
				if hadErr {
					retryAttempt++
				} else {
					retryAttempt = 0
				}
			}
		}
	}

	heartbeatTicker := time.NewTicker(l.opts.HeartbeatInterval)
	defer heartbeatTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return sessionEstablished, nil
		default:
		}

		idleCmd, err := client.Idle()
		if err != nil {
			// On IDLE-capable server, Idle() failure is a connection or protocol error -> trigger backoff
			return sessionEstablished, fmt.Errorf("idle command failed: %w", err)
		}
		sessionEstablished = true

		stopIdle := func() error {
			if err := idleCmd.Close(); err != nil {
				return fmt.Errorf("idle close failed: %w", err)
			}
			if err := idleCmd.Wait(); err != nil {
				return fmt.Errorf("idle wait failed: %w", err)
			}
			return nil
		}

		var (
			retryTimer *time.Timer
			retryCh    <-chan time.Time
		)
		if retryAttempt > 0 {
			retryDelay := CalculateBackoff(retryAttempt-1, l.opts.MinBackoff, l.opts.MaxBackoff)
			retryTimer = time.NewTimer(retryDelay)
			retryCh = retryTimer.C
		}
		stopRetryTimer := func() {
			if retryTimer != nil {
				if !retryTimer.Stop() {
					select {
					case <-retryTimer.C:
					default:
					}
				}
				retryTimer = nil
			}
		}

		var shouldProcess bool
		select {
		case <-ctx.Done():
			stopRetryTimer()
			_ = stopIdle()
			return sessionEstablished, nil

		case <-heartbeatTicker.C:
			stopRetryTimer()
			// Heartbeat: close IDLE, wait for OK, then send NOOP
			if err := stopIdle(); err != nil {
				return sessionEstablished, err
			}
			if err := client.Noop().Wait(); err != nil {
				return sessionEstablished, fmt.Errorf("heartbeat NOOP failed: %w", err)
			}
			shouldProcess = true

		case <-updateCh:
			stopRetryTimer()
			// Mailbox update received: close IDLE, wait for OK, then process unseen
			if err := stopIdle(); err != nil {
				return sessionEstablished, err
			}
			shouldProcess = true

		case <-retryCh:
			stopRetryTimer()
			// In-session retry backoff expired: close IDLE, wait for OK, then retry unseen
			if err := stopIdle(); err != nil {
				return sessionEstablished, err
			}
			shouldProcess = true
		}

		if shouldProcess {
			hadErr, err := l.processUnseen(ctx, client)
			if err != nil {
				return sessionEstablished, fmt.Errorf("failed to process unseen messages after update: %w", err)
			}
			if hadErr {
				retryAttempt++
			} else {
				retryAttempt = 0
			}
		}
	}
}

func (l *Listener) processUnseen(ctx context.Context, client *imapclient.Client) (hadHandlerErr bool, err error) {
	criteria := &imap.SearchCriteria{
		NotFlag: []imap.Flag{imap.FlagSeen},
	}
	searchData, err := client.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return false, fmt.Errorf("search unseen failed: %w", err)
	}

	uids := searchData.AllUIDs()
	if len(uids) == 0 {
		return false, nil
	}

	l.logger.Info("found unseen emails", "count", len(uids))

	fetchOptions := &imap.FetchOptions{
		BodySection: []*imap.FetchItemBodySection{{}},
	}

	for _, uid := range uids {
		select {
		case <-ctx.Done():
			return hadHandlerErr, nil
		default:
		}

		numSet := imap.UIDSetNum(uid)
		fetchCmd := client.Fetch(numSet, fetchOptions)
		var rawMsgBytes []byte

		for {
			msgData := fetchCmd.Next()
			if msgData == nil {
				break
			}
			buf, err := msgData.Collect()
			if err != nil {
				l.logger.Warn("fetch message data collect error", "uid", uid, "error", err)
				continue
			}
			if len(buf.BodySection) > 0 {
				rawMsgBytes = buf.BodySection[0].Bytes
			}
		}
		if err := fetchCmd.Close(); err != nil {
			l.logger.Warn("fetch command close error", "error", err)
		}

		if len(rawMsgBytes) == 0 {
			continue
		}

		var subject string
		if mr, readErr := mail.CreateReader(bytes.NewReader(rawMsgBytes)); readErr == nil {
			subject, _ = DecodeSubject(mr.Header.Get("Subject"))
			_ = mr.Close()
		}

		msg, ok, err := ParseEmail(bytes.NewReader(rawMsgBytes))
		switch {
		case err != nil:
			// Poison pill: the payload can never become a valid trigger, so it is
			// consumed to keep the queue moving instead of being retried forever.
			l.logger.Error("discarding malformed trigger email (poison pill)", "uid", uid, "subject", subject, "error", err)
		case !ok:
			// Not a trigger email at all. Leave it exactly as the operator left it:
			// marking unrelated mail as read would silently destroy mailbox state.
			l.logger.Debug("leaving non-trigger email unseen", "uid", uid, "subject", subject)
			continue
		default:
			l.logger.Debug("imap parsed trigger email",
				"uid", uid,
				"msg_type", string(msg.MsgType),
				"title", msg.Title,
				"app_name", msg.AppName,
				"time_sensitive", msg.TimeSensitive,
				"emergency", msg.Emergency,
			)
			if l.opts.Handler == nil {
				l.logger.Warn("no trigger handler configured; leaving email unseen", "uid", uid)
				continue
			}
			if handlerErr := l.opts.Handler(ctx, msg); handlerErr != nil {
				l.logger.Error("message handler failed; keeping email UNSEEN for retry", "uid", uid, "error", handlerErr)
				hadHandlerErr = true
				continue
			}
		}

		// Mark as seen using UID
		storeFlags := &imap.StoreFlags{
			Op:     imap.StoreFlagsAdd,
			Flags:  []imap.Flag{imap.FlagSeen},
			Silent: true,
		}
		if err := client.Store(numSet, storeFlags, nil).Close(); err != nil {
			l.logger.Warn("failed to mark email as seen", "uid", uid, "error", err)
		}
	}

	return hadHandlerErr, nil
}
