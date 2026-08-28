// Package tracing propagates the identifiers INSTRUCTIONS.md §37 requires on
// every hot-path operation (request_id, channel_id, region, virtual_shard,
// physical_shard) through context.Context, and gives every request a stable
// request_id.
//
// This is intentionally a thin, dependency-free layer rather than a full OTel
// SDK integration: it is the seam where a real tracer (OTel/Jaeger/Tempo)
// would be wired in — Start below is where span creation would happen — but
// pulling in that dependency isn't justified until there's a real backend to
// export to (INSTRUCTIONS.md §46, avoid premature complexity).
package tracing

import (
	"context"

	"github.com/google/uuid"
)

type ctxKey int

const fieldsKey ctxKey = iota

type Fields struct {
	RequestID     string
	ChannelID     string
	Region        string
	VirtualShard  int
	PhysicalShard string
}

func NewRequestID() string {
	return uuid.NewString()
}

func WithFields(ctx context.Context, f Fields) context.Context {
	if existing, ok := ctx.Value(fieldsKey).(Fields); ok {
		if f.RequestID == "" {
			f.RequestID = existing.RequestID
		}
	}
	return context.WithValue(ctx, fieldsKey, f)
}

func FromContext(ctx context.Context) Fields {
	f, _ := ctx.Value(fieldsKey).(Fields)
	return f
}

// Start marks the seam where a real tracer would begin a span for op. It
// currently just returns ctx unchanged plus a no-op end function.
func Start(ctx context.Context, op string) (context.Context, func()) {
	return ctx, func() {}
}
