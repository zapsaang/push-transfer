package pipeline

import (
	"time"

	"github.com/zapsaang/push-transfer/internal/model"
)

// FilterStrategy implements message filtering with whitelist-first priority over blacklist.
type FilterStrategy struct {
	whiteEngines []*SafeRegexEngine
	blackEngines []*SafeRegexEngine
}

// NewFilterStrategy compiles whitelist and blacklist patterns into SafeRegexEngine instances.
func NewFilterStrategy(whitePatterns, blackPatterns []string, timeout time.Duration) (*FilterStrategy, error) {
	white := make([]*SafeRegexEngine, 0, len(whitePatterns))
	for _, p := range whitePatterns {
		engine, err := NewSafeRegexEngine(p, timeout)
		if err != nil {
			return nil, err
		}
		white = append(white, engine)
	}

	black := make([]*SafeRegexEngine, 0, len(blackPatterns))
	for _, p := range blackPatterns {
		engine, err := NewSafeRegexEngine(p, timeout)
		if err != nil {
			return nil, err
		}
		black = append(black, engine)
	}

	return &FilterStrategy{
		whiteEngines: white,
		blackEngines: black,
	}, nil
}

// Process filters the message based on configured rules.
// 1. If content is empty, passes.
// 2. If any whitelist pattern matches, passes immediately (whitelist priority).
// 3. If any blacklist pattern matches, rejects (returns false).
// 4. If neither matches, passes by default.
func (f *FilterStrategy) Process(msg *model.Message) (bool, error) {
	if msg == nil || msg.Content == "" {
		return true, nil
	}

	for _, engine := range f.whiteEngines {
		matched, err := engine.MatchString(msg.Content)
		if err != nil {
			return false, err
		}
		if matched {
			return true, nil
		}
	}

	for _, engine := range f.blackEngines {
		matched, err := engine.MatchString(msg.Content)
		if err != nil {
			return false, err
		}
		if matched {
			return false, nil
		}
	}

	return true, nil
}
