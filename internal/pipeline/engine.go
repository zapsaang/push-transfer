package pipeline

import (
	"time"

	"github.com/dlclark/regexp2"
)

// DefaultMatchTimeout defines the fallback maximum execution duration for regex matches
// to prevent ReDoS (Regular Expression Denial of Service) hangs.
const DefaultMatchTimeout = 500 * time.Millisecond

// SafeRegexEngine wraps a compiled regexp2 pattern with a bounded match timeout.
//
// A *regexp2.Regexp is documented as safe for concurrent use ("A Regexp struct is safe
// to use across goroutines") and keeps its per-match state in an internal runner pool,
// so a single compiled instance is shared by the whole pipeline. No extra
// synchronisation (or recompilation per match) is needed, and a Match returned to a
// caller is never recycled underneath it.
type SafeRegexEngine struct {
	re      *regexp2.Regexp
	pattern string
	timeout time.Duration
}

// NewSafeRegexEngine compiles a pattern using regexp2 and configures match timeout.
// If timeout <= 0, DefaultMatchTimeout (500ms) is applied.
func NewSafeRegexEngine(pattern string, timeout time.Duration) (*SafeRegexEngine, error) {
	if timeout <= 0 {
		timeout = DefaultMatchTimeout
	}

	re, err := regexp2.Compile(pattern, regexp2.None)
	if err != nil {
		return nil, err
	}
	re.MatchTimeout = timeout

	return &SafeRegexEngine{
		re:      re,
		pattern: pattern,
		timeout: timeout,
	}, nil
}

// MatchString returns whether the pattern matches the input string.
func (e *SafeRegexEngine) MatchString(s string) (bool, error) {
	return e.re.MatchString(s)
}

// FindStringMatch returns the first Match for the input string.
func (e *SafeRegexEngine) FindStringMatch(s string) (*regexp2.Match, error) {
	return e.re.FindStringMatch(s)
}

// Pattern returns the underlying regex pattern string.
func (e *SafeRegexEngine) Pattern() string {
	return e.pattern
}

// Timeout returns the configured execution timeout for this engine.
func (e *SafeRegexEngine) Timeout() time.Duration {
	return e.timeout
}
