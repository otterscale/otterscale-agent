package app

import (
	"errors"

	"connectrpc.com/connect"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/otterscale/otterscale-agent/internal/core"
)

// statusReasonToConnectCode maps Kubernetes StatusReason values to
// their closest ConnectRPC error code equivalents.
var statusReasonToConnectCode = map[metav1.StatusReason]connect.Code{
	metav1.StatusReasonUnauthorized:          connect.CodeUnauthenticated,
	metav1.StatusReasonForbidden:             connect.CodePermissionDenied,
	metav1.StatusReasonNotFound:              connect.CodeNotFound,
	metav1.StatusReasonAlreadyExists:         connect.CodeAlreadyExists,
	metav1.StatusReasonConflict:              connect.CodeFailedPrecondition,
	metav1.StatusReasonGone:                  connect.CodeNotFound,
	metav1.StatusReasonInvalid:               connect.CodeInvalidArgument,
	metav1.StatusReasonServerTimeout:         connect.CodeDeadlineExceeded,
	metav1.StatusReasonStoreReadError:        connect.CodeInternal,
	metav1.StatusReasonTimeout:               connect.CodeDeadlineExceeded,
	metav1.StatusReasonTooManyRequests:       connect.CodeResourceExhausted,
	metav1.StatusReasonBadRequest:            connect.CodeInvalidArgument,
	metav1.StatusReasonMethodNotAllowed:      connect.CodeUnimplemented,
	metav1.StatusReasonNotAcceptable:         connect.CodeInvalidArgument,
	metav1.StatusReasonRequestEntityTooLarge: connect.CodeResourceExhausted,
	metav1.StatusReasonUnsupportedMediaType:  connect.CodeInvalidArgument,
	metav1.StatusReasonInternalError:         connect.CodeInternal,
	metav1.StatusReasonExpired:               connect.CodeInvalidArgument,
	metav1.StatusReasonServiceUnavailable:    connect.CodeUnavailable,
}

// k8sErrorToConnectError converts a Kubernetes API error or a domain
// error into a ConnectRPC error with a semantically equivalent code.
// Domain errors (ErrClusterNotFound, ErrNotReady) are checked first,
// then Kubernetes APIStatus errors. Unrecognised errors fall back to
// connect.CodeInternal.
func k8sErrorToConnectError(err error) error {
	// Domain error mapping.
	var clusterNotFound *core.ErrClusterNotFound
	if errors.As(err, &clusterNotFound) {
		return connect.NewError(connect.CodeNotFound, err)
	}
	var notReady *core.ErrNotReady
	if errors.As(err, &notReady) {
		return connect.NewError(connect.CodeUnavailable, err)
	}

	// Kubernetes API error mapping.
	var apiStatus apierrors.APIStatus
	if !errors.As(err, &apiStatus) {
		return connect.NewError(connect.CodeInternal, err)
	}

	code, ok := statusReasonToConnectCode[apiStatus.Status().Reason]
	if !ok {
		return connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewError(code, err)
}
