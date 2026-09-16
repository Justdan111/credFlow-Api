package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Justdan111/credflow-api/internal/auth"
)

func authedRequest(method, userID, businessID string) *http.Request {
	req := httptest.NewRequest(method, "/api/customers", nil)
	req.RemoteAddr = "203.0.113.9:44321"
	if userID == "" {
		return req
	}
	ctx := auth.WithUserContext(context.Background(), userID, businessID, auth.RoleOwner)
	return req.WithContext(ctx)
}

func TestByUser_keysOnTheUserNotTheAddress(t *testing.T) {
	// The reason this matters: on carrier-grade NAT, widespread on African
	// mobile networks, colleagues share one public address. An IP key would let
	// one busy person throttle the whole office.
	first := ByUser(authedRequest(http.MethodGet, "user-a", "biz-1"))
	second := ByUser(authedRequest(http.MethodGet, "user-b", "biz-1"))

	if first == second {
		t.Fatalf("two users behind one address share a bucket: %q", first)
	}
	if first != "user:user-a" {
		t.Errorf("key: got %q, want %q", first, "user:user-a")
	}
}

func TestByUser_fallsBackToIPWhenUnauthenticated(t *testing.T) {
	// Before RequireAuth has run there is no user to key on, and returning an
	// empty key would silently disable the rule.
	got := ByUser(authedRequest(http.MethodGet, "", ""))
	if got != "ip:203.0.113.9" {
		t.Errorf("key: got %q, want %q", got, "ip:203.0.113.9")
	}
}

func TestByBusiness_keysOnTheTenant(t *testing.T) {
	first := ByBusiness(authedRequest(http.MethodPost, "user-a", "biz-1"))
	second := ByBusiness(authedRequest(http.MethodPost, "user-b", "biz-1"))

	// Invitations send mail on the business's behalf, so two admins in one
	// business must share the quota that protects the sending reputation.
	if first != second {
		t.Errorf("colleagues should share the business bucket: %q vs %q", first, second)
	}
	if other := ByBusiness(authedRequest(http.MethodPost, "user-c", "biz-2")); other == first {
		t.Error("separate tenants must not share a bucket")
	}
}

func TestByMutatingMethod(t *testing.T) {
	key := ByMutatingMethod(ByUser)

	mutating := []string{http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete}
	for _, method := range mutating {
		if got := key(authedRequest(method, "user-a", "biz-1")); got == "" {
			t.Errorf("%s should be counted against the write limit", method)
		}
	}

	// An empty key skips the rule, so reads never consume the write allowance.
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		if got := key(authedRequest(method, "user-a", "biz-1")); got != "" {
			t.Errorf("%s should not consume the write limit, got key %q", method, got)
		}
	}
}
