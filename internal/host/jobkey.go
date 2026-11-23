package host

import "context"

// jobKeyContext is the context key of the job key.
type jobKeyContext struct{}

// WithJobKey returns a context that carries the key of the call that submits a host job. A caller that runs again after a crash
// gives the same key, and the provider then finds the job that the first call started. An empty key means no key.
func WithJobKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, jobKeyContext{}, key)
}

// JobKey returns the key that WithJobKey put in ctx, or an empty text when it holds none.
func JobKey(ctx context.Context) string {
	// A context value is untyped, and a context with no key gives nil, which reads as the empty key.
	key, found := ctx.Value(jobKeyContext{}).(string)
	if !found {
		return ""
	}

	return key
}
