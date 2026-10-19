// Package main provides the entry point for the push-transfer gateway daemon.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/zapsaang/push-transfer/internal/config"
	"github.com/zapsaang/push-transfer/internal/deliver"
	"github.com/zapsaang/push-transfer/internal/idempotency"
	"github.com/zapsaang/push-transfer/internal/ingress/imap"
	"github.com/zapsaang/push-transfer/internal/ingress/webhook"
	"github.com/zapsaang/push-transfer/internal/model"
	"github.com/zapsaang/push-transfer/internal/pipeline"
)

// Version can be overwritten during build via -ldflags="-X main.Version=...".
var Version = "1.0.0"

func main() {
	if err := execute(); err != nil {
		slog.Error("application error", "error", err)
		os.Exit(1)
	}
}

func execute() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	if err := run(ctx, os.Args[1:], os.Stdout, nil); err != nil && !isCleanExit(err) {
		return err
	}
	return nil
}

// isCleanExit reports whether err represents a successful, user-requested termination
// (currently -h/--help) rather than an application failure.
func isCleanExit(err error) bool {
	return errors.Is(err, flag.ErrHelp)
}

type runModes struct {
	Webhook bool
	IMAP    bool
}

// resolveRunModes is the single definition of what each RUN_MODE requires. It runs before any
// resource is constructed, so an unusable mode declaration always fails the startup gate first.
func resolveRunModes(cfg *config.Config) (runModes, error) {
	if cfg == nil {
		return runModes{}, errors.New("environment variable RUN_MODE is required: use one of webhook, imap, both")
	}
	switch cfg.RunMode {
	case config.RunModeWebhook:
		if !cfg.Auth.HasWebhookAuth() {
			return runModes{}, errors.New("environment variable RUN_MODE=webhook requires AUTH_CREDENTIALS with at least one usable credential")
		}
	case config.RunModeIMAP:
		if !cfg.IMAP.Enabled() {
			return runModes{}, errors.New("environment variable RUN_MODE=imap requires EMAIL_TRIGGER_LOGIN_EMAIL and EMAIL_TRIGGER_PASSWORD")
		}
		if cfg.Auth.HasWebhookAuth() {
			return runModes{}, errors.New("environment variable AUTH_CREDENTIALS is not used with RUN_MODE=imap; remove it or set RUN_MODE=both")
		}
	case config.RunModeBoth:
		if !cfg.Auth.HasWebhookAuth() {
			return runModes{}, errors.New("environment variable RUN_MODE=both requires AUTH_CREDENTIALS with at least one usable credential")
		}
		if !cfg.IMAP.Enabled() {
			return runModes{}, errors.New("environment variable RUN_MODE=both requires EMAIL_TRIGGER_LOGIN_EMAIL and EMAIL_TRIGGER_PASSWORD")
		}
	default:
		return runModes{}, errors.New("environment variable RUN_MODE is required: use one of webhook, imap, both")
	}
	return runModes{Webhook: cfg.RunMode.StartsWebhook(), IMAP: cfg.RunMode.StartsIMAP()}, nil
}

type processor interface {
	Process(msg *model.Message) (bool, error)
}

type deliverer interface {
	Deliver(ctx context.Context, msg *model.Message) error
}

type idempotencyChecker interface {
	CheckAndRecord(key string) bool
}

func newIMAPHandler(pipe processor, dispatcher deliverer, idempotencyChecker idempotencyChecker, logger *slog.Logger) imap.MessageHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context, msg *model.Message) error {
		if idempotencyChecker != nil && msg.IdempotencyKey != "" {
			if !idempotencyChecker.CheckAndRecord(msg.IdempotencyKey) {
				logger.Info("imap message ignored due to duplicate idempotency key",
					"idempotency_key", msg.IdempotencyKey,
					"title", msg.Title,
				)
				return nil
			}
		}

		pass, err := pipe.Process(msg)
		if err != nil {
			logger.Error("imap message processing error", "error", err, "title", msg.Title)
			return err
		}
		if !pass {
			logger.Info("imap message rejected by filter", "title", msg.Title)
			return nil
		}
		return dispatcher.Deliver(ctx, msg)
	}
}

// run executes the push-transfer service with the provided context, CLI arguments,
// output writer, optional config lookup function, and optional ready channel.
func run(ctx context.Context, args []string, stdout io.Writer, lookup func(string) string, ready ...chan<- string) error {
	fs := flag.NewFlagSet("push-transfer", flag.ContinueOnError)
	fs.SetOutput(stdout)
	var showVersion bool
	fs.BoolVar(&showVersion, "version", false, "print version and exit")
	fs.BoolVar(&showVersion, "v", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if showVersion {
		_, _ = fmt.Fprintf(stdout, "push-transfer version %s\n", Version)
		return nil
	}

	// 1. Initialize structured logging
	var levelVar slog.LevelVar
	logger := slog.New(slog.NewTextHandler(stdout, &slog.HandlerOptions{
		Level: &levelVar,
	}))

	logger.Info("starting push-transfer gateway", "version", Version)

	// 2. Load configuration
	var (
		cfg *config.Config
		err error
	)
	if lookup != nil {
		cfg, err = config.LoadFromLookup(lookup)
	} else {
		cfg, err = config.Load()
	}
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	levelVar.Set(cfg.LogLevel)

	if cfg.TimeLocation != nil {
		logger.Info("configured timezone", "timezone", cfg.Timezone)
	}

	// 3. Evaluate run modes before constructing anything. This is the startup contract (see
	// AGENTS.md contract 3): a missing RUN_MODE, or a mode whose prerequisites are not met, must
	// fail fail-fast before the pipeline, HTTP clients and delivery targets are built, so the
	// operator never sees "registered deliver target" for a process that is about to exit.
	modes, err := resolveRunModes(cfg)
	if err != nil {
		return err
	}

	if !modes.IMAP && (cfg.IMAP.Enabled() || cfg.IMAP.HasPartialConfig()) {
		logger.Warn("IMAP configuration is present but ignored: RUN_MODE=webhook does not start the IMAP listener; use RUN_MODE=both")
	}

	logger.Info("resolved run mode", "run_mode", string(cfg.RunMode), "webhook", modes.Webhook, "imap", modes.IMAP)

	// 4. Build processing pipeline
	pipe, err := pipeline.NewPipelineFromRules(cfg.Rules, 500*time.Millisecond, logger)
	if err != nil {
		return fmt.Errorf("failed to initialize processing pipeline: %w", err)
	}

	// 5. Build delivery dispatcher and register all enabled targets
	dispatcher := buildDispatcher(cfg, logger)

	// 6. Setup root context and cancellation
	rootCtx, rootCancel := context.WithCancel(ctx)
	defer rootCancel()

	// Setup shared idempotency cache
	idempotencyCache := idempotency.NewMemoryCache(rootCtx, cfg.Idempotency.TTL, cfg.Idempotency.MaxKeys)
	defer idempotencyCache.Close()

	var wg sync.WaitGroup

	// 7. Start IMAP listener if enabled
	if modes.IMAP {
		imapHandler := newIMAPHandler(pipe, dispatcher, idempotencyCache, logger)

		imapListener := imap.NewListener(imap.ListenerOptions{
			IMAPServer:   cfg.IMAP.IMAPServer,
			IMAPPort:     cfg.IMAP.IMAPPort,
			LoginEmail:   cfg.IMAP.LoginEmail,
			Password:     cfg.IMAP.Password,
			Mailbox:      cfg.IMAP.Mailbox,
			Handler:      imapHandler,
			TimeLocation: cfg.TimeLocation,
			Logger:       logger,
		})

		wg.Add(1)
		go func() {
			defer wg.Done()
			logger.Info("starting IMAP email trigger listener",
				"server", cfg.IMAP.IMAPServer,
				"port", cfg.IMAP.IMAPPort,
				"email", cfg.IMAP.LoginEmail,
				"mailbox", cfg.IMAP.Mailbox,
			)
			if err := imapListener.Start(rootCtx); err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("IMAP listener stopped with error", "error", err)
			}
			logger.Info("IMAP listener stopped")
		}()
	}

	// 8. Start HTTP Webhook server if enabled
	var httpServer *http.Server
	if modes.Webhook {
		creds := cfg.Auth.Credentials
		credNames := make([]string, len(creds))
		for i, c := range creds {
			credNames[i] = c.Name
		}
		logger.Info("configured webhook credentials", "count", len(creds), "names", credNames)

		webhookHandler := webhook.NewHandler(webhook.HandlerOptions{
			Credentials:        creds,
			TrustedProxies:     cfg.Server.TrustedProxies,
			Processor:          pipe,
			Deliverer:          dispatcher,
			IdempotencyChecker: idempotencyCache,
			TimeLocation:       cfg.TimeLocation,
			Logger:             logger,
		})

		mux := http.NewServeMux()
		mux.Handle("/webhook", webhookHandler)
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		})

		addr := fmt.Sprintf(":%d", cfg.Server.Port)
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("failed to bind HTTP server on %s: %w", addr, err)
		}

		httpServer = newHTTPServer(mux, logger)

		if len(ready) > 0 && ready[0] != nil {
			ready[0] <- listener.Addr().String()
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			logger.Info("starting HTTP webhook server", "addr", listener.Addr().String())
			if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("HTTP server error", "error", err)
				rootCancel()
			}
			logger.Info("HTTP webhook server stopped")
		}()
	}

	// 9. Await context cancellation / shutdown
	<-rootCtx.Done()
	logger.Info("service stopping due to context cancellation")

	// 10. Graceful shutdown sequence
	if httpServer != nil {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		logger.Info("shutting down HTTP server with 10s timeout budget...")
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			logger.Error("HTTP server shutdown error", "error", err)
		} else {
			logger.Info("HTTP server gracefully stopped")
		}
	}

	wg.Wait()
	logger.Info("push-transfer stopped cleanly")
	return nil
}

// newHTTPServer builds the HTTP server with defensive timeouts and routes the
// net/http server's own diagnostics (accept errors, handler panics) through the
// injected structured logger instead of the standard library's default logger.
func newHTTPServer(handler http.Handler, logger *slog.Logger) *http.Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}
}

// buildDispatcher instantiates and registers enabled message delivery targets.
// If no external delivery targets (Bark, Telegram, Email) are configured,
// it falls back to ConsoleDeliver to enable console logging.
func buildDispatcher(cfg *config.Config, loggers ...*slog.Logger) *deliver.Dispatcher {
	var logger *slog.Logger
	if len(loggers) > 0 && loggers[0] != nil {
		logger = loggers[0]
	} else {
		logger = slog.Default()
	}

	if cfg.Crypto != nil && cfg.Crypto.DeprecatedIVConfigured {
		logger.Warn("AES_128_ECB_IV is deprecated and ignored; random IV is generated per message")
	}

	var targets []deliver.Target
	httpClient := deliver.NewDefaultHTTPClient(5 * time.Second)

	// 1. Bark Deliver
	if cfg.Bark != nil && (len(cfg.Bark.DeviceKeys) > 0 || len(cfg.Bark.URLs) > 0) {
		var cryptoKey []byte
		if cfg.Crypto != nil {
			cryptoKey = cfg.Crypto.Key
		}
		barkDeliver := deliver.NewBarkDeliver(deliver.BarkOptions{
			HTTPClient: httpClient,
			PostURL:    cfg.Bark.PostURL,
			DeviceKeys: cfg.Bark.DeviceKeys,
			URLs:       cfg.Bark.URLs,
			CryptoKey:  cryptoKey,
			Logger:     logger,
		})
		targets = append(targets, barkDeliver)
		logger.Info("registered deliver target", "name", "bark", "device_keys", len(cfg.Bark.DeviceKeys), "urls", len(cfg.Bark.URLs))
	}

	// 2. Telegram Deliver
	if cfg.Telegram != nil {
		if cfg.Telegram.HasPartialConfig() {
			logger.Warn("partial telegram configuration detected; telegram target disabled")
		} else if cfg.Telegram.Enabled() {
			tgDeliver := deliver.NewTelegramDeliver(deliver.TelegramOptions{
				HTTPClient: httpClient,
				BaseURL:    cfg.Telegram.BaseURL,
				BotToken:   cfg.Telegram.BotToken,
				ChatID:     cfg.Telegram.ChatID,
				Logger:     logger,
			})
			targets = append(targets, tgDeliver)
			logger.Info("registered deliver target", "name", "telegram", "chat_id", cfg.Telegram.ChatID)
		}
	}

	// 3. Email Deliver
	if cfg.Email != nil {
		if cfg.Email.HasPartialConfig() {
			logger.Warn("partial email configuration detected; email target disabled")
		} else if cfg.Email.Enabled() {
			emailDeliver := deliver.NewEmailDeliver(deliver.EmailOptions{
				SenderEmail:   cfg.Email.SenderEmail,
				ReceiverEmail: cfg.Email.ReceiverEmail,
				Password:      cfg.Email.Password,
				SMTPServer:    cfg.Email.SMTPServer,
				SMTPPort:      cfg.Email.SMTPPort,
				Logger:        logger,
			})
			targets = append(targets, emailDeliver)
			logger.Info("registered deliver target", "name", "email", "smtp_server", cfg.Email.SMTPServer, "port", cfg.Email.SMTPPort)
		}
	}

	// 4. Fallback to Console Deliver if no external targets configured
	if len(targets) == 0 {
		targets = append(targets, deliver.NewConsoleDeliver(logger))
		logger.Info("registered deliver target", "name", "console")
	}

	return deliver.NewDispatcher(deliver.DispatcherOptions{
		Targets: targets,
		Timeout: 5 * time.Second,
		Logger:  logger,
	})
}
