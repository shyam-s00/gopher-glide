package profile

import "errors"

// ErrProfileNotFound is returned when no profile file can be located for the
// requested name via any step of the resolution hierarchy.
var ErrProfileNotFound = errors.New("profile not found")

// ErrInvalidProfile is returned when a profile YAML file exists but cannot be
// parsed or fails basic structural validation.
var ErrInvalidProfile = errors.New("invalid profile")
