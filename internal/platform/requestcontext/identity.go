package requestcontext

import (
	"context"

	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/telegramauth"
)

type administratorKey struct{}

type MemberIdentity struct {
	Telegram telegramauth.Identity
	IsAdmin  bool
}
type memberKey struct{}

func WithMember(ctx context.Context, identity MemberIdentity) context.Context {
	return context.WithValue(ctx, memberKey{}, identity)
}
func Member(ctx context.Context) (MemberIdentity, bool) {
	v, ok := ctx.Value(memberKey{}).(MemberIdentity)
	return v, ok
}

func WithAdministrator(ctx context.Context, identity telegramauth.Identity) context.Context {
	return context.WithValue(ctx, administratorKey{}, identity)
}

func Administrator(ctx context.Context) (telegramauth.Identity, bool) {
	identity, ok := ctx.Value(administratorKey{}).(telegramauth.Identity)
	return identity, ok
}
