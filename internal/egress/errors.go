package egress

import (
	"errors"
	"fmt"
)

var (
	ErrEgressRequired       = errors.New("egress_required")
	ErrNodeNotFound         = errors.New("egress_node_not_found")
	ErrEndpointNotFound     = errors.New("egress_not_found")
	ErrEndpointDisabled     = errors.New("egress_disabled")
	ErrEndpointInvalid      = errors.New("egress_invalid")
	ErrEndpointInUse        = errors.New("egress_endpoint_in_use")
	ErrStoreLocked          = errors.New("egress_store_locked")
	ErrRevisionConflict     = errors.New("egress_revision_conflict")
	ErrBindingConflict      = errors.New("egress_binding_conflict")
	ErrConfirmationRequired = errors.New("egress_confirmation_required")
	ErrCheckInProgress      = errors.New("egress_check_in_progress")
)

type Error struct {
	Code    string
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return e.Code
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Cause }

func (e *Error) StatusCode() int { return 503 }

func RuntimeError(err error) error {
	if err == nil {
		return nil
	}
	var existing *Error
	if errors.As(err, &existing) {
		return err
	}
	return &Error{Code: ErrorCode(err), Message: err.Error(), Cause: err}
}

func ErrorCode(err error) string {
	var target *Error
	if errors.As(err, &target) && target.Code != "" {
		return target.Code
	}
	switch {
	case errors.Is(err, ErrEgressRequired):
		return "egress_required"
	case errors.Is(err, ErrEndpointNotFound):
		return "egress_not_found"
	case errors.Is(err, ErrEndpointDisabled):
		return "egress_disabled"
	case errors.Is(err, ErrEndpointInvalid):
		return "egress_invalid"
	case errors.Is(err, ErrEndpointInUse):
		return "egress_endpoint_in_use"
	case errors.Is(err, ErrNodeNotFound):
		return "egress_node_not_found"
	case errors.Is(err, ErrStoreLocked):
		return "egress_store_locked"
	case errors.Is(err, ErrRevisionConflict):
		return "egress_revision_conflict"
	case errors.Is(err, ErrBindingConflict):
		return "egress_binding_conflict"
	case errors.Is(err, ErrConfirmationRequired):
		return "egress_confirmation_required"
	case errors.Is(err, ErrCheckInProgress):
		return "egress_check_in_progress"
	default:
		return "egress_error"
	}
}
