package snap

import "encoding/json"

const (
	StabilityStable   = "STABLE"   // field present in ≥ 95 % of samples
	StabilityVolatile = "VOLATILE" // field present in ≥ 50 % of samples
	StabilityRare     = "RARE"     // field present in <  50 % of samples
)

// Classification thresholds — not exported so callers use the string constants.
const (
	stableThreshold   = 0.95
	volatileThreshold = 0.50
)

// InferSchema takes a slice of raw JSON response bodies and returns a
// SchemaSnapshot describing the union structure observed across all samples.
//
// Processing pipeline for each body:
//  1. Unmarshal (non-JSON or empty bodies are silently skipped)
//  2. walkValue → flat map of dot-separated paths → JSON type name
//  3. schemaMerger.observe → accumulate per-field presence + type votes
//  4. schemaMerger.finalize → compute the presence fraction + stability label
//
// Returns nil when there are no valid samples.
func InferSchema(bodies [][]byte) *SchemaSnapshot {
	if len(bodies) == 0 {
		return nil
	}

	m := newSchemaMerger()
	for _, body := range bodies {
		if len(body) == 0 {
			continue
		}
		var v any
		if err := json.Unmarshal(body, &v); err != nil {
			continue // skip malformed JSON silently
		}
		fields := extractFields(v)
		if len(fields) > 0 {
			m.observe(fields)
		}
	}

	return m.finalize()
}

// extractFields walks a decoded JSON value and returns a flat map of
// dot-separated field paths → JSON type names.
//
// Type names follow JSON spec vocabulary:
//
//	"string" | "number" | "boolean" | "null" | "object" | "array"
//
// Nested object fields use dot-notation: "user.address.city"
// Array element fields use bracket-dot notation: "items[].id"
func extractFields(root any) map[string]string {
	out := make(map[string]string)
	walkValue("", root, out)
	return out
}

func walkValue(path string, v any, out map[string]string) {
	switch val := v.(type) {

	case map[string]interface{}:
		// Record the object type for non-root paths.
		if path != "" {
			out[path] = "object"
		}
		for k, child := range val {
			childPath := k
			if path != "" {
				childPath = path + "." + k
			}
			walkValue(childPath, child, out)
		}

	case []interface{}:
		out[path] = "array"
		// Infer element structure from the first element when it is an object.
		// This handles the common case of paginated list responses.
		if len(val) > 0 {
			if obj, ok := val[0].(map[string]interface{}); ok {
				for k, child := range obj {
					walkValue(path+"[]."+k, child, out)
				}
			}
		}

	case string:
		out[path] = "string"

	case float64:
		// All JSON numbers decode to float64 in Go's encoding/json.
		out[path] = "number"

	case bool:
		out[path] = "boolean"

	case nil:
		out[path] = "null"
	}
}

// fieldObs tracks per-field observations across N samples.
type fieldObs struct {
	typeVotes map[string]int // type name → vote count
	seenCount int            // number of samples in which this field appeared
}

// schemaMerger accumulates field observations from multiple JSON samples and
// produces a final SchemaSnapshot once all samples have been ingested.
type schemaMerger struct {
	fields      map[string]*fieldObs
	sampleCount int // total number of samples observed (including those with no valid fields)
}

func newSchemaMerger() *schemaMerger {
	return &schemaMerger{fields: make(map[string]*fieldObs)}
}

// observe records one sample's field map into the merger.
func (m *schemaMerger) observe(fields map[string]string) {
	m.sampleCount++
	for path, typ := range fields {
		obs, ok := m.fields[path]
		if !ok {
			obs = &fieldObs{typeVotes: make(map[string]int)}
			m.fields[path] = obs
		}
		obs.seenCount++
		obs.typeVotes[typ]++
	}
}

// Computes the SchemaSnapshot from all accumulated observations.
// Returns nil when no samples were observed or no fields were extracted.
func (m *schemaMerger) finalize() *SchemaSnapshot {
	if m.sampleCount == 0 || len(m.fields) == 0 {
		return nil
	}

	snap := &SchemaSnapshot{
		Fields: make(map[string]FieldSchema, len(m.fields)),
	}
	for path, obs := range m.fields {
		presence := float64(obs.seenCount) / float64(m.sampleCount)
		snap.Fields[path] = FieldSchema{
			Type:      dominantType(obs.typeVotes),
			Presence:  presence,
			Stability: scoreStability(presence),
		}
	}
	return snap
}

// dominantType returns the most frequently observed type for a field.
// Ties are broken by a deterministic type-rank ordering, so the output is
// stable across Go map-iteration orders:
//
//	null < boolean < number < string < array < object
func dominantType(votes map[string]int) string {
	best := ""
	bestCount := -1
	for typ, count := range votes {
		if count > bestCount || (count == bestCount && typeRank[typ] > typeRank[best]) {
			best = typ
			bestCount = count
		}
	}
	return best
}

// typeRank gives a deterministic ordering for tie-breaking in dominantType.
var typeRank = map[string]int{
	"null":    1,
	"boolean": 2,
	"number":  3,
	"string":  4,
	"array":   5,
	"object":  6,
}

// scoreStability maps a presence fraction to a stability label.
func scoreStability(presence float64) string {
	switch {
	case presence >= stableThreshold:
		return StabilityStable
	case presence >= volatileThreshold:
		return StabilityVolatile
	default:
		return StabilityRare
	}
}
