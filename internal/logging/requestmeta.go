package logging

import (
	"context"
	"strings"
	"sync/atomic"
)

type endpointContextKey struct{}
type responseStatusContextKey struct{}

type responseStatusHolder struct {
	status atomic.Int64
}

func WithEndpoint(ctx context.Context, endpoint string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, endpointContextKey{}, strings.TrimSpace(endpoint))
}

func GetEndpoint(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(endpointContextKey{}).(string)
	return strings.TrimSpace(value)
}

func WithResponseStatusHolder(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, responseStatusContextKey{}, &responseStatusHolder{})
}

func SetResponseStatus(ctx context.Context, status int) {
	if ctx == nil {
		return
	}
	holder, _ := ctx.Value(responseStatusContextKey{}).(*responseStatusHolder)
	if holder == nil {
		return
	}
	holder.status.Store(int64(status))
}

func GetResponseStatus(ctx context.Context) int {
	if ctx == nil {
		return 0
	}
	holder, _ := ctx.Value(responseStatusContextKey{}).(*responseStatusHolder)
	if holder == nil {
		return 0
	}
	return int(holder.status.Load())
}
