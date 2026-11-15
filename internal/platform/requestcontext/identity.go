package requestcontext

import (
	"context"

	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/telegramauth"
)

type administratorKey struct{}

func WithAdministrator(ctx context.Context, identity telegramauth.Identity) context.Context {
	return context.WithValue(ctx, administratorKey{}, identity)
}

func Administrator(ctx context.Context) (telegramauth.Identity, bool) {
	identity, ok := ctx.Value(administratorKey{}).(telegramauth.Identity)
	return identity, ok
}
