package hive

import "errors"

var (
	// ErrNotImplemented is returned by methods that are not yet wired up.
	ErrNotImplemented = errors.New("hive: not implemented")

	// ErrNoRequests is returned by RunStages when the spec list is empty.
	ErrNoRequests = errors.New("hive: no request specs provided")

	// ErrNoStages is returned by RunStages when the config has no stages.
	ErrNoStages = errors.New("hive: no stages configured")

	// ErrHttpError is recorded when a response status code is ≥ 400.
	ErrHttpError = errors.New("hive: http request failed")

	// ErrExtractionFailed is recorded when a @gg-export directive cannot
	// extract a value from the response body (bad path, no regex match, etc.).
	ErrExtractionFailed = errors.New("hive: variable extraction failed")
)
