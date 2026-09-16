package middleware

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Justdan111/credflow-api/internal/auth"
	"github.com/Justdan111/credflow-api/pkg/ratelimit"
	"github.com/Justdan111/credflow-api/pkg/response"
)

// ClientIP returns the address a rate limit should be keyed on.
//
// SECURITY: chi's RealIP middleware overwrites r.RemoteAddr from X-Real-IP or
// X-Forwarded-For with no validation whatsoever. If it is mounted and nothing
// strips those headers at the edge, an attacker sends a different value on each
// request and every IP-based limit evaporates.
//
// So RealIP is mounted only when TRUST_PROXY_HEADERS says a load balancer that
// overwrites those headers sits in front. Otherwise RemoteAddr is the true TCP
// peer and this simply strips the port. Defaulting to untrusted means a
// misconfigured deployment is merely inaccurate about client IPs, rather than
// silently unprotected.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// No port, e.g. a unix socket or an already-cleaned address.
		return r.RemoteAddr
	}
	return host
}

// RateLimitRule is one bucket applied to a route.
type RateLimitRule struct {
	// Name distinguishes rules so two limits on the same endpoint do not share
	// a bucket.
	Name   string
	Limit  int
	Window time.Duration
	// KeyFunc derives the bucket key. Returning "" skips the rule, which lets
	// an email-keyed rule stand down when the body carries no email.
	KeyFunc func(r *http.Request) string
}

// ByIP keys on the client address.
func ByIP(r *http.Request) string { return "ip:" + ClientIP(r) }

// ByEmailField keys on an email in the JSON body.
//
// Reading the body here would consume it before the handler runs, so it is read
// and then restored. Bodies reaching these routes are already capped by
// BodyLimit, so buffering is bounded.
//
// The address is lower-cased and trimmed, so "A@x.test" and "a@x.test " share
// one bucket instead of each getting a full allowance.
func ByEmailField(r *http.Request) string {
	email := extractEmail(r)
	if email == "" {
		return ""
	}
	return "email:" + email
}

// ByIPAndEmail keys on both together.
//
// This is what makes the limits safe on carrier-grade NAT, which is widespread
// on African mobile networks: two subscribers sharing one public address get
// separate buckets because the email differs, so one person's failed logins
// cannot lock out the other. It falls back to IP alone when no email is present.
func ByIPAndEmail(r *http.Request) string {
	email := extractEmail(r)
	if email == "" {
		return "ip:" + ClientIP(r)
	}
	return "ip+email:" + ClientIP(r) + "|" + email
}

// ByUser keys on the authenticated user id, falling back to IP when no token
// has been verified yet.
//
// User rather than IP for the same reason login keys on IP + email: carrier-
// grade NAT is widespread on African mobile networks, so an IP-keyed limit on
// authenticated traffic would let one busy colleague throttle everybody sharing
// the office connection. The user id is also the thing worth capping — it is
// what a stolen token impersonates.
//
// Must be chained AFTER RequireAuth, which is what puts the id in context.
func ByUser(r *http.Request) string {
	if userID, ok := auth.UserIDFromContext(r.Context()); ok {
		return "user:" + userID
	}
	return "ip:" + ClientIP(r)
}

// ByBusiness keys on the tenant, for limits that protect a shared resource
// rather than one person — invitations, which send mail on somebody's behalf.
func ByBusiness(r *http.Request) string {
	if businessID, ok := auth.BusinessIDFromContext(r.Context()); ok {
		return "business:" + businessID
	}
	return "ip:" + ClientIP(r)
}

// ByMutatingMethod applies a rule only to requests that change state, so reads
// and writes can carry different ceilings on the same route group.
func ByMutatingMethod(key func(*http.Request) string) func(*http.Request) string {
	return func(r *http.Request) string {
		switch r.Method {
		case http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete:
			return key(r)
		default:
			// An empty key skips the rule entirely.
			return ""
		}
	}
}

func extractEmail(r *http.Request) string {
	if r.Body == nil {
		return ""
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return ""
	}
	// Restore the body for the handler.
	r.Body = io.NopCloser(strings.NewReader(string(raw)))

	var payload struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(payload.Email))
}

// RateLimit applies every rule to a route. All must pass.
func RateLimit(limiter ratelimit.Limiter, rules ...RateLimitRule) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, rule := range rules {
				key := rule.KeyFunc(r)
				if key == "" {
					continue
				}

				res, err := limiter.Allow(r.Context(), rule.Name+":"+key, rule.Limit, rule.Window)
				if err != nil {
					// A limiter failure must not take the endpoint down; log
					// nothing sensitive and let the request through.
					continue
				}
				if !res.Allowed {
					retry := int(res.RetryAfter.Seconds())
					if retry < 1 {
						retry = 1
					}
					w.Header().Set("Retry-After", strconv.Itoa(retry))
					// The message never reveals whether the account exists, so
					// the limiter cannot become the enumeration oracle that
					// forgot-password is deliberately not.
					response.Fail(w, http.StatusTooManyRequests,
						"too many requests, please try again later")
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
