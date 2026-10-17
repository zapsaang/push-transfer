package webhook

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zapsaang/push-transfer/internal/config"
)

func TestConstantTimeEqual(t *testing.T) {
	d1 := digestOf("hello")
	d2 := digestOf("hello")
	d3 := digestOf("world")

	if got := constantTimeEqual(d1, d2); got != 1 {
		t.Errorf("constantTimeEqual(d1, d2) = %d; want 1", got)
	}
	if got := constantTimeEqual(d1, d3); got != 0 {
		t.Errorf("constantTimeEqual(d1, d3) = %d; want 0", got)
	}

	// Single byte difference
	d4 := d1
	d4[0] ^= 0x01
	if got := constantTimeEqual(d1, d4); got != 0 {
		t.Errorf("constantTimeEqual single-bit difference = %d; want 0", got)
	}
}

func TestCredentialSet_ComparisonCountIsInputIndependent(t *testing.T) {
	// 3 credentials:
	// c1: Token (T=1, B=0)
	// c2: Basic (T=0, B=1)
	// c3: Token + Basic (T=1, B=1)
	// Total: T=2, B=2. Expected comparison count per verify = T + 2*B = 2 + 2*2 = 6.
	rawCreds := []config.Credential{
		{Name: "c1", Token: "token-1"},
		{Name: "c2", Username: "user2", Password: "pass2"},
		{Name: "c3", Token: "token-3", Username: "user3", Password: "pass3"},
	}

	set := newCredentialSet(rawCreds)
	const expectedComparisons = 6

	var callCount int
	set.eq = func(a, b digest) int {
		callCount++
		return constantTimeEqual(a, b)
	}

	testCases := []struct {
		name     string
		setupReq func(r *http.Request)
		wantAuth bool
		wantCred string
	}{
		{
			name: "hit first token (bearer)",
			setupReq: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer token-1")
			},
			wantAuth: true,
			wantCred: "c1",
		},
		{
			name: "hit basic credential",
			setupReq: func(r *http.Request) {
				r.SetBasicAuth("user2", "pass2")
			},
			wantAuth: true,
			wantCred: "c2",
		},
		{
			name: "hit last token",
			setupReq: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer token-3")
			},
			wantAuth: true,
			wantCred: "c3",
		},
		{
			name: "hit last basic",
			setupReq: func(r *http.Request) {
				r.SetBasicAuth("user3", "pass3")
			},
			wantAuth: true,
			wantCred: "c3",
		},
		{
			name: "no match wrong credentials",
			setupReq: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer wrong-token")
				r.SetBasicAuth("wrong-user", "wrong-pass")
			},
			wantAuth: false,
			wantCred: "",
		},
		{
			name:     "no auth headers provided at all",
			setupReq: func(_ *http.Request) {},
			wantAuth: false,
			wantCred: "",
		},
		{
			name: "wrong bearer token only",
			setupReq: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer invalid")
			},
			wantAuth: false,
			wantCred: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/webhook", http.NoBody)
			tc.setupReq(req)

			callCount = 0
			name, ok := set.verify(req)

			if ok != tc.wantAuth {
				t.Errorf("verify ok=%v; want %v", ok, tc.wantAuth)
			}
			if name != tc.wantCred {
				t.Errorf("verify name=%q; want %q", name, tc.wantCred)
			}
			if callCount != expectedComparisons {
				t.Errorf("expected exactly %d comparisons, got %d", expectedComparisons, callCount)
			}
		})
	}
}

func TestCredentialSet_ReportedName(t *testing.T) {
	rawCreds := []config.Credential{
		{Name: "device-alpha", Token: "tok-alpha"},
		{Name: "device-beta", Token: "tok-beta"},
	}
	set := newCredentialSet(rawCreds)

	req := httptest.NewRequest(http.MethodPost, "/webhook", http.NoBody)
	req.Header.Set("Authorization", "Bearer tok-beta")

	name, ok := set.verify(req)
	if !ok || name != "device-beta" {
		t.Errorf("expected (device-beta, true), got (%q, %v)", name, ok)
	}

	reqMiss := httptest.NewRequest(http.MethodPost, "/webhook", http.NoBody)
	reqMiss.Header.Set("Authorization", "Bearer tok-unknown")

	name, ok = set.verify(reqMiss)
	if ok || name != "" {
		t.Errorf("expected ('', false) for mismatch, got (%q, %v)", name, ok)
	}

	wsNameSet := newCredentialSet([]config.Credential{{Name: "   ", Token: "t"}})
	reqWS := httptest.NewRequest(http.MethodPost, "/webhook", http.NoBody)
	reqWS.Header.Set("Authorization", "Bearer t")
	if gotName, gotOK := wsNameSet.verify(reqWS); !gotOK || gotName != "" {
		t.Errorf("expected ('', true) for whitespace-only Name, got (%q, %v)", gotName, gotOK)
	}
}

// TestCredentialSet_DefensiveGuards locks the guard branches of verify: a nil/empty set or nil
// request must deny without panicking, and an unset comparer must fall back to constant-time comparison.
func TestCredentialSet_DefensiveGuards(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/webhook", http.NoBody)
	req.Header.Set("Authorization", "Bearer anything")

	var nilSet *credentialSet
	if name, ok := nilSet.verify(req); ok || name != "" {
		t.Errorf("nil set must deny, got (%q, %v)", name, ok)
	}
	if !nilSet.empty() {
		t.Error("nil set must report empty")
	}

	emptySet := newCredentialSet(nil)
	if name, ok := emptySet.verify(req); ok || name != "" {
		t.Errorf("empty set must deny, got (%q, %v)", name, ok)
	}
	if !emptySet.empty() {
		t.Error("set built from no credentials must report empty")
	}

	// A set whose comparer field was cleared must still authenticate correctly, and must deny nil *http.Request.
	fallback := newCredentialSet([]config.Credential{{Name: "fallback", Token: "tok-fallback"}})
	fallback.eq = nil
	if name, ok := fallback.verify(nil); ok || name != "" {
		t.Errorf("verify(nil) must deny without panicking, got (%q, %v)", name, ok)
	}
	reqOK := httptest.NewRequest(http.MethodPost, "/webhook", http.NoBody)
	reqOK.Header.Set("Authorization", "Bearer tok-fallback")
	if name, ok := fallback.verify(reqOK); !ok || name != "fallback" {
		t.Errorf("expected (fallback, true) with nil comparer, got (%q, %v)", name, ok)
	}
}

func TestCredentialSet_BasicPreservesExactBytes(t *testing.T) {
	set := newCredentialSet([]config.Credential{
		{Name: "basic", Username: "u", Password: " pass "},
		{Name: "basic-user", Username: " user ", Password: "p"},
	})

	reqExact := httptest.NewRequest(http.MethodPost, "/webhook", http.NoBody)
	reqExact.SetBasicAuth("u", " pass ")
	if name, ok := set.verify(reqExact); !ok || name != "basic" {
		t.Errorf("expected exact password ' pass ' to match, got (%q, %v)", name, ok)
	}

	reqTrimmed := httptest.NewRequest(http.MethodPost, "/webhook", http.NoBody)
	reqTrimmed.SetBasicAuth("u", "pass")
	if name, ok := set.verify(reqTrimmed); ok || name != "" {
		t.Errorf("expected trimmed password 'pass' not to match ' pass ', got (%q, %v)", name, ok)
	}

	reqUserExact := httptest.NewRequest(http.MethodPost, "/webhook", http.NoBody)
	reqUserExact.SetBasicAuth(" user ", "p")
	if name, ok := set.verify(reqUserExact); !ok || name != "basic-user" {
		t.Errorf("expected exact username ' user ' to match, got (%q, %v)", name, ok)
	}

	reqUserTrimmed := httptest.NewRequest(http.MethodPost, "/webhook", http.NoBody)
	reqUserTrimmed.SetBasicAuth("user", "p")
	if name, ok := set.verify(reqUserTrimmed); ok || name != "" {
		t.Errorf("expected trimmed username 'user' not to match ' user ', got (%q, %v)", name, ok)
	}
}

// TestCredentialSet_FirstMatchWins exercises the first-match audit rule. parseCredentials rejects
// duplicate factors, so this state is reachable only through programmatic construction; the
// `name == ""` guard in verify must still be a deliberate, tested rule rather than an untestable
// defensive branch.
func TestCredentialSet_FirstMatchWins(t *testing.T) {
	set := newCredentialSet([]config.Credential{
		{Name: "first", Token: "duplicate-token"},
		{Name: "second", Token: "duplicate-token"},
	})

	req := httptest.NewRequest(http.MethodPost, "/webhook", http.NoBody)
	req.Header.Set("Authorization", "Bearer duplicate-token")

	name, ok := set.verify(req)
	if !ok || name != "first" {
		t.Errorf("expected the first matching credential name to win, got (%q, %v)", name, ok)
	}
}

// TestCredentialFactorsMatchCompiler is a cross-package drift guard: config.Credential.Usable()
// and webhook.newCredentialSet must agree on every credential shape.
func TestCredentialFactorsMatchCompiler(t *testing.T) {
	samples := []config.Credential{
		{},
		{Name: "empty"},
		{Name: "ws-token", Token: "   \t\n  "},
		{Name: "ws-user", Username: "  ", Password: "secret"},
		{Name: "ws-pass", Username: "admin", Password: "  "},
		{Name: "tok", Token: "tok-1"},
		{Name: "basic", Username: "u", Password: "p"},
		{Name: "basic-padded", Username: " u ", Password: " p "},
		{Name: "dual", Token: "tok-1", Username: "u", Password: "p"},
	}

	for _, c := range samples {
		wantUsable := c.Usable()
		compiledUsable := !newCredentialSet([]config.Credential{c}).empty()
		if wantUsable != compiledUsable {
			t.Errorf("credential %+v: Usable()=%v but newCredentialSet non-empty=%v", c, wantUsable, compiledUsable)
		}
	}
}
