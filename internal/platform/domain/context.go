package domain

import "context"

type requestKey struct{}
type RequestMetadata struct {
	RequestID, Subject, Action                string
	ObjectHash, BeforeRevision, AfterRevision string
	Scope                                     Scope
	Command                                   bool
}

func WithRequest(ctx context.Context, request, subject string) context.Context {
	v := RequestFrom(ctx)
	v.RequestID = request
	v.Subject = subject
	return context.WithValue(ctx, requestKey{}, v)
}
func RequestFrom(ctx context.Context) RequestMetadata {
	v, _ := ctx.Value(requestKey{}).(RequestMetadata)
	return v
}
func WithScope(ctx context.Context, s Scope) context.Context {
	v := RequestFrom(ctx)
	v.Scope = s
	return context.WithValue(ctx, requestKey{}, v)
}
func WithOperation(ctx context.Context, action string, command bool) context.Context {
	v := RequestFrom(ctx)
	v.Action = action
	v.Command = command
	return context.WithValue(ctx, requestKey{}, v)
}

func WithCommandEvidence(ctx context.Context, hash, before, after string) context.Context {
	v := RequestFrom(ctx)
	v.ObjectHash = hash
	v.BeforeRevision = before
	v.AfterRevision = after
	return context.WithValue(ctx, requestKey{}, v)
}
