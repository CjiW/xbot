package ctxkeys

import "context"

type key string

const (
	permControlEnabledKey key = "perm_control_enabled"
	chatIDKey             key = "chat_id"
	senderIDKey           key = "sender_id"
)

func WithPermControlEnabled(ctx context.Context, enabled bool) context.Context {
	return context.WithValue(ctx, permControlEnabledKey, enabled)
}

func PermControlEnabledFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	enabled, _ := ctx.Value(permControlEnabledKey).(bool)
	return enabled
}

func WithApprovalTarget(ctx context.Context, chatID, senderID string) context.Context {
	ctx = context.WithValue(ctx, chatIDKey, chatID)
	ctx = context.WithValue(ctx, senderIDKey, senderID)
	return ctx
}

func ApprovalTargetFromContext(ctx context.Context) (chatID, senderID string) {
	if ctx == nil {
		return "", ""
	}
	if v := ctx.Value(chatIDKey); v != nil {
		if s, ok := v.(string); ok {
			chatID = s
		}
	}
	if v := ctx.Value(senderIDKey); v != nil {
		if s, ok := v.(string); ok {
			senderID = s
		}
	}
	return chatID, senderID
}
