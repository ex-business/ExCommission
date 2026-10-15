package domain

import "context"

type requestKey struct{}
type RequestMetadata struct{ RequestID, Subject string }

func WithRequest(ctx context.Context, request, subject string) context.Context {
	return context.WithValue(ctx, requestKey{}, RequestMetadata{RequestID: request, Subject: subject})
}
func RequestFrom(ctx context.Context) RequestMetadata {
	value, _ := ctx.Value(requestKey{}).(RequestMetadata)
	return value
}
