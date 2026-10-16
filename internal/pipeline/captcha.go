package pipeline

import (
	"time"

	"github.com/zapsaang/push-transfer/internal/model"
)

// CaptchaStrategy extracts verification codes from message content.
// It prioritizes capture group 1 (e.g. `(\d{4,6})`) if present,
// and falls back to full match if no sub-group exists.
type CaptchaStrategy struct {
	engines []*SafeRegexEngine
}

// NewCaptchaStrategy creates a new CaptchaStrategy with compiled SafeRegexEngine instances.
func NewCaptchaStrategy(patterns []string, timeout time.Duration) (*CaptchaStrategy, error) {
	engines := make([]*SafeRegexEngine, 0, len(patterns))
	for _, p := range patterns {
		engine, err := NewSafeRegexEngine(p, timeout)
		if err != nil {
			return nil, err
		}
		engines = append(engines, engine)
	}
	return &CaptchaStrategy{engines: engines}, nil
}

// Process searches for captcha patterns in the message content.
// On match, it sets msg.CaptchaCode and returns true.
func (c *CaptchaStrategy) Process(msg *model.Message) (bool, error) {
	if msg == nil || msg.Content == "" {
		return true, nil
	}

	for _, engine := range c.engines {
		match, err := engine.FindStringMatch(msg.Content)
		if err != nil {
			return false, err
		}
		if match != nil {
			var code string
			// Prioritize group 1 if present
			if grp := match.GroupByNumber(1); grp != nil && len(grp.Captures) > 0 {
				code = grp.String()
			} else {
				code = match.String()
			}
			msg.SetCaptcha(code)
			return true, nil
		}
	}

	return true, nil
}
