package database

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
)

// QueryStats counts the SQL one request runs and the time spent in them; the
// request logger puts both on every "HTTP Request" line (db_queries, db_ms),
// so a page's cost on the real box shows in the logs.
type QueryStats struct {
	n  atomic.Int64
	ns atomic.Int64
}

func (s *QueryStats) Count() int64 { return s.n.Load() }

func (s *QueryStats) Duration() time.Duration { return time.Duration(s.ns.Load()) }

type statsKey struct{}

type startKey struct{}

// WithQueryStats returns ctx carrying fresh stats; every query run with it
// (or a context derived from it) is counted.
func WithQueryStats(ctx context.Context) (context.Context, *QueryStats) {
	s := &QueryStats{}
	return context.WithValue(ctx, statsKey{}, s), s
}

type queryTracer struct{}

func (queryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	s, _ := ctx.Value(statsKey{}).(*QueryStats)
	if s == nil {
		return ctx
	}
	s.n.Add(1)
	return context.WithValue(ctx, startKey{}, time.Now())
}

func (queryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	s, _ := ctx.Value(statsKey{}).(*QueryStats)
	start, ok := ctx.Value(startKey{}).(time.Time)
	if s != nil && ok {
		s.ns.Add(int64(time.Since(start)))
	}
}
