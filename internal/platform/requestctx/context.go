package requestctx

import "context"

type key int

const (
	requestIDKey key = iota
	clientIPKey
)

func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey, requestID)
}

func RequestID(ctx context.Context) string {
	requestID, _ := ctx.Value(requestIDKey).(string)
	return requestID
}

func WithClientIP(ctx context.Context, clientIP string) context.Context {
	return context.WithValue(ctx, clientIPKey, clientIP)
}

func ClientIP(ctx context.Context) string {
	clientIP, _ := ctx.Value(clientIPKey).(string)
	return clientIP
}
