package deliver

import (
	"context"
	"log/slog"

	"github.com/zapsaang/push-transfer/internal/model"
)

// ConsoleDeliver logs delivered messages to slog.
type ConsoleDeliver struct {
	logger *slog.Logger
}

// NewConsoleDeliver creates a ConsoleDeliver instance.
func NewConsoleDeliver(logger ...*slog.Logger) *ConsoleDeliver {
	var l *slog.Logger
	if len(logger) > 0 && logger[0] != nil {
		l = logger[0]
	} else {
		l = slog.Default()
	}
	return &ConsoleDeliver{logger: l}
}

// Name returns the identifier for ConsoleDeliver.
func (c *ConsoleDeliver) Name() string {
	return "console"
}

// Send logs msg details using slog without panic.
func (c *ConsoleDeliver) Send(_ context.Context, msg *model.Message) error {
	if msg == nil {
		return nil
	}
	l := c.logger
	if l == nil {
		l = slog.Default()
	}
	l.Info("console deliver",
		"msg_type", msg.MsgType,
		"title", msg.Title,
		"content", msg.Content,
		"captcha", msg.CaptchaCode,
		"emergency", msg.Emergency,
		"text", msg.GenComplexText(),
	)
	return nil
}
