// Package webhook implements the HTTP webhook server and authentication for push-transfer.
package webhook

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

// SecureCompare performs a constant-time comparison between two strings
// to protect against timing side-channel attacks.
// Inputs are hashed with SHA-256 before comparison to prevent leaking token length.
func SecureCompare(a, b string) bool {
	hA := sha256.Sum256([]byte(a))
	hB := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(hA[:], hB[:]) == 1
}

// ExtractBearerToken extracts the bearer token from the Authorization header,
// supporting multiple spaces or tab separation between "Bearer" and the token.
func ExtractBearerToken(r *http.Request) string {
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

// ExtractQueryToken returns the `token` query parameter.
//
// It exists for compatibility with the legacy clients (the removed Python
// implementation read the same parameter) and is documented in README as an
// alternative to the Authorization header. Callers must compare the result in
// constant time.
func ExtractQueryToken(r *http.Request) string {
	if r == nil || r.URL == nil {
		return ""
	}
	return strings.TrimSpace(r.URL.Query().Get("token"))
}
