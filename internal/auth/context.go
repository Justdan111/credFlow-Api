package auth

import "context"

// ctxKey is unexported so no other package can collide with our keys.
// Using a typed key (not a string) is required by the context package docs.
type ctxKey int

const (
	ctxKeyUserID ctxKey = iota
	ctxKeyBusinessID
	ctxKeyRole
)

func WithUserContext(ctx context.Context, userID, businessID, role string) context.Context {
	ctx = context.WithValue(ctx, ctxKeyUserID, userID)
	ctx = context.WithValue(ctx, ctxKeyBusinessID, businessID)
	ctx = context.WithValue(ctx, ctxKeyRole, role)
	return ctx
}

func UserIDFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ctxKeyUserID).(string)
	return v, ok && v != ""
}

func BusinessIDFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ctxKeyBusinessID).(string)
	return v, ok && v != ""
}

func RoleFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ctxKeyRole).(string)
	return v, ok && v != ""
}
