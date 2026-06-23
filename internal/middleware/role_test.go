package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Justdan111/credflow-api/internal/auth"
)

func newRoleStack(allowed ...string) (http.Handler, *bool) {
	ran := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ran = true
		w.WriteHeader(http.StatusOK)
	})
	return RequireRole(allowed...)(next), &ran
}

func TestRequireRole_allowsMatchingRole(t *testing.T) {
	handler, ran := newRoleStack(auth.RoleOwner, auth.RoleAdmin)

	ctx := auth.WithUserContext(context.Background(), "u", "b", auth.RoleAdmin)
	req := httptest.NewRequest(http.MethodDelete, "/protected", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", rec.Code)
	}
	if !*ran {
		t.Fatal("next handler did not run for allowed role")
	}
}

func TestRequireRole_rejectsDisallowedRole(t *testing.T) {
	handler, ran := newRoleStack(auth.RoleOwner)

	ctx := auth.WithUserContext(context.Background(), "u", "b", auth.RoleMember)
	req := httptest.NewRequest(http.MethodDelete, "/protected", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status: got %d, want 403", rec.Code)
	}
	if *ran {
		t.Fatal("next handler ran despite disallowed role")
	}
}

func TestRequireRole_rejectsMissingContext(t *testing.T) {
	handler, ran := newRoleStack(auth.RoleOwner)

	// No WithUserContext — simulates RequireRole used without RequireAuth above it.
	req := httptest.NewRequest(http.MethodDelete, "/protected", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", rec.Code)
	}
	if *ran {
		t.Fatal("next handler ran without auth context")
	}
}
