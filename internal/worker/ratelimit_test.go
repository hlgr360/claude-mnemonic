package worker

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPerClientRateLimitMiddleware_KeysOnPeerIP(t *testing.T) {
	h := PerClientRateLimitMiddleware(NewPerClientRateLimiter(0.001, 1))(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))

	do := func(remote, realIP string) int {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = remote
		if realIP != "" {
			req.Header.Set("X-Real-IP", realIP)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	if got := do("10.0.0.1:1111", "1.1.1.1"); got != http.StatusOK {
		t.Fatalf("first request: got %d", got)
	}
	// Same peer, new port and forged header: must share the exhausted bucket.
	if got := do("10.0.0.1:2222", "2.2.2.2"); got != http.StatusTooManyRequests {
		t.Fatalf("spoofed header/new port bypassed limit: got %d", got)
	}
	// Different peer is unaffected.
	if got := do("10.0.0.2:1111", ""); got != http.StatusOK {
		t.Fatalf("other peer: got %d", got)
	}
}
