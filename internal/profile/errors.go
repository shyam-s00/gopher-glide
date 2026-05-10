package profile

import "errors"

// ErrProfileNotFound is returned when no profile file can be located for the
// requested name via any step of the resolution hierarchy.
var ErrProfileNotFound = errors.New("profile not found")

// ErrInvalidProfile is returned when a profile YAML file exists but cannot be
// parsed or fails basic structural validation.
var ErrInvalidProfile = errors.New("invalid profile")

// ErrBuiltInProfileConflict is returned when a file in ~/.config/gg/profiles/
// uses the same name as one of the 21 shipped built-in profiles.
var ErrBuiltInProfileConflict = errors.New("built-in profile conflict")
