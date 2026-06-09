package hive

// actorMemoryInitCap is the pre-allocated capacity for an Actor's variable
// store. For typical journeys (≤8 extracted variables) the slice never needs
// to grow beyond this, avoiding all dynamic reallocation after creation.
const actorMemoryInitCap = 8

// memEntry holds one key/value slot inside an ActorMemory.
type memEntry struct {
	Key, Value string
}

// ActorMemory is the private, lock-free variable store for a single Actor.
//
// Design rationale:
//
//	Concurrency: an Actor goroutine executes a linear journey sequentially —
//	there is never concurrent access to its variables. A mutex would be pure
//	overhead.
//
//	Data structure: a pre-allocated flat slice outperforms map[string]string
//	for the small variable sets (≤8 entries) typical in load-test journeys.
//
//	  map[string]string – each lookup hashes the key and may follow 1-2
//	    pointer indirections into a bucket array. At 50 000+ RPS the GC
//	    pressure from bucket allocations is measurable.
//
//	  []memEntry (flat slice) – 8 entries fit in ≤128 bytes (≤2 cache lines).
//	    A linear scan touches contiguous memory and is branch-prediction-
//	    friendly. For n ≤ 8 the scan is consistently faster than hashing.
type ActorMemory struct {
	entries []memEntry
}

// newActorMemory returns a fresh ActorMemory pre-allocated to actorMemoryInitCap.
func newActorMemory() ActorMemory {
	return ActorMemory{entries: make([]memEntry, 0, actorMemoryInitCap)}
}

// Set stores or updates the value for key. If key already exists it is
// overwritten in-place (no extra allocation). If not, a new entry is appended.
func (m *ActorMemory) Set(key, value string) {
	for i := range m.entries {
		if m.entries[i].Key == key {
			m.entries[i].Value = value
			return
		}
	}
	m.entries = append(m.entries, memEntry{Key: key, Value: value})
}

// Get returns the stored value and true if key is present, or ("", false).
func (m *ActorMemory) Get(key string) (string, bool) {
	for _, e := range m.entries {
		if e.Key == key {
			return e.Value, true
		}
	}
	return "", false
}

// Len returns the number of stored variable entries.
func (m *ActorMemory) Len() int { return len(m.entries) }

// Reset clears all entries while retaining the underlying slice capacity so
// the memory can be reused across repeated journey executions without
// triggering a new heap allocation.
func (m *ActorMemory) Reset() { m.entries = m.entries[:0] }

// ToMap converts the flat-slice entries to a map[string]string for passing to
// RequestSpec.ToHTTPRequest. Returns nil when the store is empty so callers
// can skip the substitution loop entirely (the nil-map guard in resolve.go).
func (m *ActorMemory) ToMap() map[string]string {
	if len(m.entries) == 0 {
		return nil
	}
	out := make(map[string]string, len(m.entries))
	for _, e := range m.entries {
		out[e.Key] = e.Value
	}
	return out
}
