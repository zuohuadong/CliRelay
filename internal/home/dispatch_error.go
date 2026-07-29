package home

import "errors"

// DispatchError classifies whether Home may have processed an auth dispatch request.
type DispatchError struct {
	Err       error
	Ambiguous bool
}

func (e *DispatchError) Error() string {
	if e == nil || e.Err == nil {
		return "home auth dispatch failed"
	}
	return e.Err.Error()
}

func (e *DispatchError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// NewAmbiguousDispatchError marks a post-send transport failure as requiring a client abort.
func NewAmbiguousDispatchError(err error) error {
	if err == nil {
		return nil
	}
	return &DispatchError{Err: err, Ambiguous: true}
}

// IsAmbiguousDispatchError reports whether Home may have processed the dispatch request.
func IsAmbiguousDispatchError(err error) bool {
	var dispatchErr *DispatchError
	return errors.As(err, &dispatchErr) && dispatchErr.Ambiguous
}
