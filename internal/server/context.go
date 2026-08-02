package server

import (
	"context"

	"zipmanga/internal/db"
)

func contextWithUser(ctx context.Context, u db.User) context.Context {
	return context.WithValue(ctx, userCtxKey, u)
}

func contextUser(ctx context.Context) (db.User, bool) {
	v := ctx.Value(userCtxKey)
	u, ok := v.(db.User)
	return u, ok
}
