package impersonation

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
)

const subjectHeaderName = "X-Otterscale-Subject"

// PropagateSubjectHeaderInterceptor copies the authenticated subject from ctx
// into a trusted request header. Intended for server->agent forwarding.
type PropagateSubjectHeaderInterceptor struct{}

func NewPropagateSubjectHeaderInterceptor() *PropagateSubjectHeaderInterceptor {
	return &PropagateSubjectHeaderInterceptor{}
}

func (i *PropagateSubjectHeaderInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		sub, ok := GetSubject(ctx)
		if !ok || sub == "" {
			return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("subject not found in context"))
		}
		req.Header().Set(subjectHeaderName, sub)
		return next(ctx, req)
	}
}

func (i *PropagateSubjectHeaderInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		sub, ok := GetSubject(ctx)
		if ok && sub != "" {
			conn.RequestHeader().Set(subjectHeaderName, sub)
		}
		return conn
	}
}

func (i *PropagateSubjectHeaderInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// TrustedSubjectHeaderInterceptor reads the trusted subject header and injects it
// into context. Intended for agent-side inbound requests coming from the server.
//
// IMPORTANT: only use this when the agent API is not directly exposed to end-users
// (e.g. only reachable via a loopback-only reverse tunnel on the server host).
type TrustedSubjectHeaderInterceptor struct{}

func NewTrustedSubjectHeaderInterceptor() *TrustedSubjectHeaderInterceptor {
	return &TrustedSubjectHeaderInterceptor{}
}

func (i *TrustedSubjectHeaderInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		sub := req.Header().Get(subjectHeaderName)
		if sub == "" {
			return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("missing %s header", subjectHeaderName))
		}
		return next(context.WithValue(ctx, subjectKey, sub), req)
	}
}

func (i *TrustedSubjectHeaderInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *TrustedSubjectHeaderInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		sub := conn.RequestHeader().Get(subjectHeaderName)
		if sub == "" {
			return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("missing %s header", subjectHeaderName))
		}
		return next(context.WithValue(ctx, subjectKey, sub), conn)
	}
}

var _ connect.Interceptor = (*PropagateSubjectHeaderInterceptor)(nil)
var _ connect.Interceptor = (*TrustedSubjectHeaderInterceptor)(nil)
