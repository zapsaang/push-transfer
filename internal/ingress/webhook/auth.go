// Package webhook implements the HTTP webhook server and authentication for push-transfer.
package webhook

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/zapsaang/push-transfer/internal/config"
)

type digest [sha256.Size]byte

func digestOf(s string) digest { return sha256.Sum256([]byte(s)) }

// comparer is the sole equality primitive; returning 1 (match) or 0 (mismatch),
// matching crypto/subtle.ConstantTimeCompare.
type comparer func(a, b digest) int

func constantTimeEqual(a, b digest) int {
	return subtle.ConstantTimeCompare(a[:], b[:])
}

type compiledCredential struct {
	name     string
	token    digest
	hasToken bool
	user     digest
	pass     digest
	hasBasic bool
}

type credentialSet struct {
	creds        []compiledCredential
	hasBasicAuth bool
	eq           comparer
}

func newCredentialSet(creds []config.Credential) *credentialSet {
	set := &credentialSet{
		eq: constantTimeEqual,
	}
	for _, c := range creds {
		hasToken, hasBasic := c.Factors()
		if !hasToken && !hasBasic {
			continue
		}
		cc := compiledCredential{
			name:     strings.TrimSpace(c.Name),
			hasToken: hasToken,
			hasBasic: hasBasic,
		}
		if hasToken {
			cc.token = digestOf(strings.TrimSpace(c.Token))
		}
		if hasBasic {
			cc.user = digestOf(c.Username)
			cc.pass = digestOf(c.Password)
			set.hasBasicAuth = true
		}
		set.creds = append(set.creds, cc)
	}
	return set
}

func (s *credentialSet) empty() bool {
	return s == nil || len(s.creds) == 0
}

func (s *credentialSet) verify(r *http.Request) (name string, ok bool) {
	if s == nil || len(s.creds) == 0 || r == nil {
		return "", false
	}
	eq := s.eq
	if eq == nil {
		eq = constantTimeEqual
	}

	// Fixed 3 hashes, not short-circuited by unconfigured credential types.
	bearer := digestOf(ExtractBearerToken(r))
	user, pass, _ := r.BasicAuth() // hashed even when headers are missing
	uh, ph := digestOf(user), digestOf(pass)

	allowed := 0
	for i := range s.creds { // Constant N: no break, no return
		c := &s.creds[i]
		hit := 0
		if c.hasToken {
			// Integer bitwise ops: no short-circuit evaluation
			hit |= eq(bearer, c.token)
		}
		if c.hasBasic {
			// AND within same entry: prevents cross-credential combination
			hit |= eq(uh, c.user) & eq(ph, c.pass)
		}
		if hit == 1 && name == "" {
			name = c.name
		}
		allowed |= hit
	}
	return name, allowed == 1
}

// ExtractBearerToken extracts the bearer token from the Authorization header,
// supporting multiple spaces or tab separation between "Bearer" and the token.
func ExtractBearerToken(r *http.Request) string {
	if r == nil {
		return ""
	}
	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	if authHeader == "" {
		return ""
	}

	if len(authHeader) < 6 || !strings.EqualFold(authHeader[:6], "Bearer") {
		return ""
	}

	rest := authHeader[6:]
	if rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
		return ""
	}

	return strings.TrimSpace(rest)
}
