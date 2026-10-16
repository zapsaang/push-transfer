package pipeline

import (
	"time"

	"github.com/zapsaang/push-transfer/internal/model"
)

// EmergencyStrategy marks messages as urgent emergencies upon pattern match.
type EmergencyStrategy struct {
	engines []*SafeRegexEngine
}

// NewEmergencyStrategy compiles emergency patterns into SafeRegexEngine instances.
func NewEmergencyStrategy(patterns []string, timeout time.Duration) (*EmergencyStrategy, error) {
	engines := make([]*SafeRegexEngine, 0, len(patterns))
	for _, p := range patterns {
		engine, err := NewSafeRegexEngine(p, timeout)
		if err != nil {
			return nil, err
		}
		engines = append(engines, engine)
	}
	return &EmergencyStrategy{engines: engines}, nil
}

// Process evaluates whether the message matches any emergency patterns.
// On match, msg.Emergency is set to true.
func (e *EmergencyStrategy) Process(msg *model.Message) (bool, error) {
	if msg == nil || msg.Content == "" {
		return true, nil
	}

	for _, engine := range e.engines {
		matched, err := engine.MatchString(msg.Content)
		if err != nil {
			return false, err
		}
		if matched {
			msg.SetEmergency(true)
			return true, nil
		}
	}

	return true, nil
}
