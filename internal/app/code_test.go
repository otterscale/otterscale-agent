package app

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/otterscale/otterscale-agent/internal/core"
)

func TestK8sErrorToConnectError_DomainErrors(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode connect.Code
	}{
		{
			name:     "ErrClusterNotFound",
			err:      &core.ErrClusterNotFound{Cluster: "test"},
			wantCode: connect.CodeNotFound,
		},
		{
			name:     "ErrNotReady",
			err:      &core.ErrNotReady{Subsystem: "chisel"},
			wantCode: connect.CodeUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := k8sErrorToConnectError(tt.err)
			var connectErr *connect.Error
			if !errors.As(got, &connectErr) {
				t.Fatalf("expected *connect.Error, got %T", got)
			}
			if connectErr.Code() != tt.wantCode {
				t.Errorf("expected code %v, got %v", tt.wantCode, connectErr.Code())
			}
		})
	}
}

func TestK8sErrorToConnectError_K8sStatusErrors(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode connect.Code
	}{
		{
			name:     "Unauthorized",
			err:      apierrors.NewUnauthorized("test"),
			wantCode: connect.CodeUnauthenticated,
		},
		{
			name:     "Forbidden",
			err:      apierrors.NewForbidden(schema.GroupResource{}, "test", errors.New("denied")),
			wantCode: connect.CodePermissionDenied,
		},
		{
			name:     "NotFound",
			err:      apierrors.NewNotFound(schema.GroupResource{}, "test"),
			wantCode: connect.CodeNotFound,
		},
		{
			name:     "AlreadyExists",
			err:      apierrors.NewAlreadyExists(schema.GroupResource{}, "test"),
			wantCode: connect.CodeAlreadyExists,
		},
		{
			name:     "Conflict",
			err:      apierrors.NewConflict(schema.GroupResource{}, "test", errors.New("conflict")),
			wantCode: connect.CodeFailedPrecondition,
		},
		{
			name:     "Gone",
			err:      apierrors.NewGone("test"),
			wantCode: connect.CodeNotFound,
		},
		{
			name:     "Invalid",
			err:      apierrors.NewInvalid(schema.GroupKind{}, "test", nil),
			wantCode: connect.CodeInvalidArgument,
		},
		{
			name:     "ServerTimeout",
			err:      apierrors.NewServerTimeout(schema.GroupResource{}, "test", 0),
			wantCode: connect.CodeDeadlineExceeded,
		},
		{
			name:     "TooManyRequests",
			err:      apierrors.NewTooManyRequests("test", 0),
			wantCode: connect.CodeResourceExhausted,
		},
		{
			name:     "BadRequest",
			err:      apierrors.NewBadRequest("test"),
			wantCode: connect.CodeInvalidArgument,
		},
		{
			name:     "MethodNotAllowed",
			err:      apierrors.NewMethodNotSupported(schema.GroupResource{}, "test"),
			wantCode: connect.CodeUnimplemented,
		},
		{
			name:     "InternalError",
			err:      apierrors.NewInternalError(errors.New("test")),
			wantCode: connect.CodeInternal,
		},
		{
			name:     "ServiceUnavailable",
			err:      apierrors.NewServiceUnavailable("test"),
			wantCode: connect.CodeUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := k8sErrorToConnectError(tt.err)
			var connectErr *connect.Error
			if !errors.As(got, &connectErr) {
				t.Fatalf("expected *connect.Error, got %T", got)
			}
			if connectErr.Code() != tt.wantCode {
				t.Errorf("expected code %v, got %v", tt.wantCode, connectErr.Code())
			}
		})
	}
}

func TestK8sErrorToConnectError_UnknownError(t *testing.T) {
	got := k8sErrorToConnectError(errors.New("random error"))
	var connectErr *connect.Error
	if !errors.As(got, &connectErr) {
		t.Fatalf("expected *connect.Error, got %T", got)
	}
	if connectErr.Code() != connect.CodeInternal {
		t.Errorf("expected CodeInternal for unknown error, got %v", connectErr.Code())
	}
}

func TestK8sErrorToConnectError_UnmappedReason(t *testing.T) {
	// Create a StatusError with an unmapped reason.
	err := &apierrors.StatusError{
		ErrStatus: metav1.Status{
			Reason: metav1.StatusReason("CustomReason"),
		},
	}
	got := k8sErrorToConnectError(err)
	var connectErr *connect.Error
	if !errors.As(got, &connectErr) {
		t.Fatalf("expected *connect.Error, got %T", got)
	}
	if connectErr.Code() != connect.CodeInternal {
		t.Errorf("expected CodeInternal for unmapped reason, got %v", connectErr.Code())
	}
}

func TestStatusReasonToConnectCode_Completeness(t *testing.T) {
	// Verify the map has a reasonable number of entries.
	if len(statusReasonToConnectCode) < 10 {
		t.Errorf("expected at least 10 status reason mappings, got %d", len(statusReasonToConnectCode))
	}
}
