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
	// Total: T=2, B=2. Expected comparison count per verify = 2*T + 2*B = 2*2 + 2*2 = 8.
	rawCreds := []config.Credential{
		{Name: "c1", Token: "token-1"},
		{Name: "c2", Username: "user2", Password: "pass2"},
		{Name: "c3", Token: "token-3", Username: "user3", Password: "pass3"},
	}

	set := newCredentialSet(rawCreds)
	const expectedComparisons = 8

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
			name: "hit first token (query)",
			setupReq: func(r *http.Request) {
				q := r.URL.Query()
				q.Set("token", "token-1")
				r.URL.RawQuery = q.Encode()
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
			name: "all wrong auth types present simultaneously",
			setupReq: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer invalid")
				q := r.URL.Query()
				q.Set("token", "invalid-query")
				r.URL.RawQuery = q.Encode()
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
}

// TestCredentialSet_DefensiveGuards locks the two guard branches of verify: a nil/empty set must
// deny without panicking, and an unset comparer must fall back to constant-time comparison.
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

	// A set whose comparer field was cleared must still authenticate correctly.
	fallback := newCredentialSet([]config.Credential{{Name: "fallback", Token: "tok-fallback"}})
	fallback.eq = nil
	reqOK := httptest.NewRequest(http.MethodPost, "/webhook", http.NoBody)
	reqOK.Header.Set("Authorization", "Bearer tok-fallback")
	if name, ok := fallback.verify(reqOK); !ok || name != "fallback" {
		t.Errorf("expected (fallback, true) with nil comparer, got (%q, %v)", name, ok)
	}
}

// TestCredentialSet_MultipleMatchingEntries pins the deterministic reported identity when one
// request presents two independently valid factors. Bearer and Basic can never coexist (both
// ride the single Authorization header), so the co-present case is `?token=` + Basic.
func TestCredentialSet_MultipleMatchingEntries(t *testing.T) {
	set := newCredentialSet([]config.Credential{
		{Name: "token-entry", Token: "tok-shared"},
		{Name: "basic-entry", Username: "u", Password: "p"},
	})

	t.Run("query token plus basic reports the first matching entry", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/webhook?token=tok-shared", http.NoBody)
		req.SetBasicAuth("u", "p")

		name, ok := set.verify(req)
		if !ok {
			t.Fatal("expected authorization to succeed")
		}
		if name != "token-entry" {
			t.Errorf("expected the first matching entry name to be reported, got %q", name)
		}
	})

	t.Run("basic auth header suppresses a bearer token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/webhook", http.NoBody)
		req.Header.Set("Authorization", "Bearer tok-shared")
		req.SetBasicAuth("u", "p") // overwrites the Authorization header

		name, ok := set.verify(req)
		if !ok {
			t.Fatal("expected authorization to succeed via the basic factor")
		}
		if name != "basic-entry" {
			t.Errorf("expected basic-entry to be reported, got %q", name)
		}
	})
}
