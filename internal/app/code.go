package app

import (
	"errors"

	"connectrpc.com/connect"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// k8sErrorToConnectError converts a Kubernetes API error into a
// ConnectRPC error with a semantically equivalent code. If the error
// does not carry an APIStatus or the reason is unmapped, it falls back
// to connect.CodeInternal.
func k8sErrorToConnectError(err error) error {
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
