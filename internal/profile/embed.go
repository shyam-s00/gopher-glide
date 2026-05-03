package profile

import "embed"

// embeddedProfiles holds all 21 built-in profile YAML files compiled directly
// into the binary. They are the fallback when no local or global file is found.
//
//go:embed data/*.yaml
var embeddedProfiles embed.FS
