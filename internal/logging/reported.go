package logging

import "context"

// reportedKey is the unexported context key marking an error as already
// reported (logged at Error and therefore already notified).
type reportedKey struct{}

// MarkReported returns a child of ctx marking the failure it describes as
// already reported, so a later layer does not log or notify it again. The mark
// is invisible in log output. ctx must not be nil; callers without a context
// pass context.Background().
func MarkReported(ctx context.Context) context.Context {
	return context.WithValue(ctx, reportedKey{}, true)
}

// IsReported reports whether ctx, or an ancestor, was marked by MarkReported.
func IsReported(ctx context.Context) bool {
	v, ok := ctx.Value(reportedKey{}).(bool)
	return ok && v
}
