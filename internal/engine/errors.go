package engine

import "errors"

var (
	ErrHttpError  = errors.New("http request failed")
	ErrNoRequests = errors.New("no request specs provided")
)
