package hive

import "testing"

func TestActorMemory_InitialState(t *testing.T) {
	m := newActorMemory()
	if m.Len() != 0 {
		t.Errorf("expected Len=0, got %d", m.Len())
	}
	if cap(m.entries) != actorMemoryInitCap {
		t.Errorf("expected cap=%d, got %d", actorMemoryInitCap, cap(m.entries))
	}
}

func TestActorMemory_Set_And_Get(t *testing.T) {
	m := newActorMemory()
	m.Set("token", "abc123")
	val, ok := m.Get("token")
	if !ok || val != "abc123" {
		t.Errorf("expected token=abc123, got %q ok=%v", val, ok)
	}
}

func TestActorMemory_Get_Missing_ReturnsFalse(t *testing.T) {
	m := newActorMemory()
	if _, ok := m.Get("missing"); ok {
		t.Error("expected ok=false for missing key")
	}
}

func TestActorMemory_Set_UpdateExistingKey(t *testing.T) {
	m := newActorMemory()
	m.Set("key", "first")
	m.Set("key", "second")
	val, ok := m.Get("key")
	if !ok || val != "second" {
		t.Errorf("expected second, got %q ok=%v", val, ok)
	}
	if m.Len() != 1 {
		t.Errorf("expected Len=1 after in-place update, got %d", m.Len())
	}
}

func TestActorMemory_Set_MultipleKeys(t *testing.T) {
	m := newActorMemory()
	type kv struct{ k, v string }
	pairs := []kv{{"token", "jwt"}, {"user_id", "99"}, {"session", "cafe"}}
	for _, p := range pairs {
		m.Set(p.k, p.v)
	}
	if m.Len() != len(pairs) {
		t.Fatalf("expected Len=%d, got %d", len(pairs), m.Len())
	}
	for _, p := range pairs {
		if got, ok := m.Get(p.k); !ok || got != p.v {
			t.Errorf("key %q: expected %q, got %q ok=%v", p.k, p.v, got, ok)
		}
	}
}

func TestActorMemory_Len_ReflectsEntryCount(t *testing.T) {
	m := newActorMemory()
	keys := []string{"ka", "kb", "kc", "kd", "ke"}
	for i, key := range keys {
		if m.Len() != i {
			t.Fatalf("before inserting %q: expected Len=%d, got %d", key, i, m.Len())
		}
		m.Set(key, "v")
	}
}

func TestActorMemory_Reset_ClearsEntries(t *testing.T) {
	m := newActorMemory()
	m.Set("a", "1")
	m.Set("b", "2")
	m.Reset()
	if m.Len() != 0 {
		t.Errorf("expected Len=0 after Reset, got %d", m.Len())
	}
	if _, ok := m.Get("a"); ok {
		t.Error("expected key 'a' not found after Reset")
	}
}

func TestActorMemory_Reset_RetainsCapacity(t *testing.T) {
	m := newActorMemory()
	m.Set("x", "y")
	c := cap(m.entries)
	m.Reset()
	if cap(m.entries) != c {
		t.Errorf("cap changed after Reset: before=%d after=%d", c, cap(m.entries))
	}
}

func TestActorMemory_ToMap_EmptyReturnsNil(t *testing.T) {
	m := newActorMemory()
	if got := m.ToMap(); got != nil {
		t.Errorf("expected nil for empty memory, got %v", got)
	}
}

func TestActorMemory_ToMap_ContainsAllEntries(t *testing.T) {
	m := newActorMemory()
	m.Set("token", "jwt")
	m.Set("user_id", "42")
	got := m.ToMap()
	if len(got) != 2 {
		t.Fatalf("expected 2 entries in map, got %d", len(got))
	}
	if got["token"] != "jwt" {
		t.Errorf("token: expected jwt, got %q", got["token"])
	}
	if got["user_id"] != "42" {
		t.Errorf("user_id: expected 42, got %q", got["user_id"])
	}
}

func TestActorMemory_ToMap_DoesNotAliasInternalSlice(t *testing.T) {
	m := newActorMemory()
	m.Set("k", "original")
	mp := m.ToMap()
	mp["k"] = "modified"
	val, _ := m.Get("k")
	if val != "original" {
		t.Errorf("internal state mutated by map modification: got %q", val)
	}
}

func TestActorMemory_ExceedsInitCap_GrowsCorrectly(t *testing.T) {
	m := newActorMemory()
	keys := []string{"ka", "kb", "kc", "kd", "ke", "kf", "kg", "kh", "ki", "kj", "kk", "kl"}
	n := actorMemoryInitCap + 4
	for i := 0; i < n; i++ {
		m.Set(keys[i], "v")
	}
	if m.Len() != n {
		t.Fatalf("expected Len=%d after growth, got %d", n, m.Len())
	}
	for i := 0; i < n; i++ {
		if _, ok := m.Get(keys[i]); !ok {
			t.Errorf("key %q not found after growth", keys[i])
		}
	}
}
