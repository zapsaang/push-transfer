// Package pipeline implements regex-driven message processing strategies including filtering, captcha extraction, and emergency detection.
package pipeline

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/zapsaang/push-transfer/internal/config"
	"github.com/zapsaang/push-transfer/internal/model"
)

// Strategy defines the interface for individual processing stages in the pipeline.
type Strategy interface {
	Process(msg *model.Message) (bool, error)
}

// Pipeline coordinates message processing stages based on msg.MsgType, enforcing
// Fail-Closed rejection for unsupported or empty message types.
type Pipeline struct {
	filter    Strategy
	captcha   Strategy
	emergency Strategy
	logger    *slog.Logger
}

// NewPipeline creates a Pipeline with injected strategies and an optional logger.
func NewPipeline(filter, captcha, emergency Strategy, loggers ...*slog.Logger) *Pipeline {
	var logger *slog.Logger
	if len(loggers) > 0 && loggers[0] != nil {
		logger = loggers[0]
	} else {
		logger = slog.Default()
	}
	return &Pipeline{
		filter:    filter,
		captcha:   captcha,
		emergency: emergency,
		logger:    logger,
	}
}

// NewPipelineFromRules initializes strategies from application RulesConfig.
func NewPipelineFromRules(rules config.RulesConfig, timeout time.Duration, loggers ...*slog.Logger) (*Pipeline, error) {
	filter, err := NewFilterStrategy(rules.FilterWhite, rules.FilterBlack, timeout)
	if err != nil {
		return nil, err
	}

	captcha, err := NewCaptchaStrategy(rules.Captcha, timeout)
	if err != nil {
		return nil, err
	}

	emergency, err := NewEmergencyStrategy(rules.Emergency, timeout)
	if err != nil {
		return nil, err
	}

	return NewPipeline(filter, captcha, emergency, loggers...), nil
}

// Process runs the strategies mapped to the message type.
// - MsgTypeCall: bypasses strategies and passes through unconditionally.
// - MsgTypeText, MsgTypeAppNotification: runs filter -> captcha -> emergency.
// If filter blocks the message, it returns (false, nil) immediately.
// Any other unhandled message type is rejected with an error (Fail-Closed).
func (p *Pipeline) Process(msg *model.Message) (bool, error) {
	if msg == nil {
		return true, nil
	}

	switch msg.MsgType {
	case model.MsgTypeCall:
		// Calls carry no content to filter: the chain for "call" is empty.
		return true, nil

	case model.MsgTypeText, model.MsgTypeAppNotification:
		// 1. Filter strategy (blocking stage)
		if p.filter != nil {
			pass, err := p.filter.Process(msg)
			if err != nil {
				p.logger.Error("pipeline: filter strategy failed", "error", err, "title", msg.Title)
				return false, err
			}
			if !pass {
				p.logger.Info("pipeline: message rejected by filter strategy", "title", msg.Title)
				return false, nil
			}
			p.logger.Debug("pipeline: message passed filter stage", "title", msg.Title)
		}

		// 2. Captcha strategy (enrichment stage)
		if p.captcha != nil {
			_, err := p.captcha.Process(msg)
			if err != nil {
				p.logger.Warn("pipeline: captcha strategy failed", "error", err, "title", msg.Title)
			} else if msg.CaptchaCode != "" {
				p.logger.Debug("pipeline: captcha extracted", "title", msg.Title, "code", msg.CaptchaCode, "time_sensitive", msg.TimeSensitive)
			}
		}

		// 3. Emergency strategy (enrichment stage)
		if p.emergency != nil {
			_, err := p.emergency.Process(msg)
			if err != nil {
				p.logger.Warn("pipeline: emergency strategy failed", "error", err, "title", msg.Title)
			} else if msg.Emergency {
				p.logger.Debug("pipeline: emergency marked", "title", msg.Title)
			}
		}

		p.logger.Debug("pipeline: message processing complete",
			"title", msg.Title,
			"msg_type", string(msg.MsgType),
			"captcha", msg.CaptchaCode,
			"time_sensitive", msg.TimeSensitive,
			"emergency", msg.Emergency,
		)
		return true, nil

	default:
		// Fail-Closed: unhandled message types are rejected with error (ADR-05)
		p.logger.Warn("pipeline: unhandled message type rejected", "msg_type", string(msg.MsgType))
		return false, fmt.Errorf("unsupported message type: %s", msg.MsgType)
	}
}

// Execute is an alias for Process kept for the legacy entry-point naming.
func (p *Pipeline) Execute(msg *model.Message) (bool, error) {
	return p.Process(msg)
}
