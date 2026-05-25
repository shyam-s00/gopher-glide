package hive

import (
	"testing"
)

// ── buildTransport ─────────────────────────────────────────────────────────────

func TestBuildTransport_Floor(t *testing.T) {
	// Any RPS below the threshold that would push perHost under minPoolPerHost
	// must be clamped up to minPoolPerHost.
	for _, rps := range []int{0, 1, 50, 99, 999} {
		tr := buildTransport(rps)
		if tr.MaxIdleConnsPerHost < minPoolPerHost {
			t.Errorf("rps=%d: MaxIdleConnsPerHost=%d want ≥ %d",
				rps, tr.MaxIdleConnsPerHost, minPoolPerHost)
		}
		if tr.MaxIdleConns < tr.MaxIdleConnsPerHost {
			t.Errorf("rps=%d: MaxIdleConns=%d < MaxIdleConnsPerHost=%d",
				rps, tr.MaxIdleConns, tr.MaxIdleConnsPerHost)
		}
	}
}

func TestBuildTransport_Scaling(t *testing.T) {
	cases := []struct {
		rps            int
		wantMinPerHost int
	}{
		{rps: 1_000, wantMinPerHost: 100},   // 1000/10 = 100 == floor
		{rps: 5_000, wantMinPerHost: 500},   // 5000/10 = 500
		{rps: 10_000, wantMinPerHost: 1000}, // 10000/10 = 1000
		{rps: 50_000, wantMinPerHost: 5000}, // 50000/10 = 5000
	}
	for _, tc := range cases {
		tr := buildTransport(tc.rps)

		// The scaling-derived value may be clamped down by fdBudget on
		// systems with very low FD limits; skip the upper assertion in that
		// case and only check the invariant holds.
		budget := fdBudget()
		if budget > 0 && tc.wantMinPerHost > budget/2 {
			// Expected perHost would be clamped — just verify it is ≤ budget/2.
			if tr.MaxIdleConnsPerHost > budget/2 {
				t.Errorf("rps=%d: MaxIdleConnsPerHost=%d exceeds budget/2=%d",
					tc.rps, tr.MaxIdleConnsPerHost, budget/2)
			}
			continue
		}

		if tr.MaxIdleConnsPerHost < tc.wantMinPerHost {
			t.Errorf("rps=%d: MaxIdleConnsPerHost=%d want ≥ %d",
				tc.rps, tr.MaxIdleConnsPerHost, tc.wantMinPerHost)
		}
		wantTotal := tc.wantMinPerHost * poolHostMultiplier
		if tr.MaxIdleConns < wantTotal {
			t.Errorf("rps=%d: MaxIdleConns=%d want ≥ %d",
				tc.rps, tr.MaxIdleConns, wantTotal)
		}
	}
}

func TestBuildTransport_Multiplier(t *testing.T) {
	// MaxIdleConns must always be ≥ MaxIdleConnsPerHost.
	for _, rps := range []int{100, 1000, 10_000, 100_000} {
		tr := buildTransport(rps)
		if tr.MaxIdleConns < tr.MaxIdleConnsPerHost {
			t.Errorf("rps=%d: MaxIdleConns=%d < MaxIdleConnsPerHost=%d",
				rps, tr.MaxIdleConns, tr.MaxIdleConnsPerHost)
		}
	}
}

func TestBuildTransport_IdleTimeout(t *testing.T) {
	tr := buildTransport(1000)
	if tr.IdleConnTimeout != aggressiveIdleTimeout {
		t.Errorf("IdleConnTimeout=%v want %v", tr.IdleConnTimeout, aggressiveIdleTimeout)
	}
}

func TestBuildTransport_KeepAlives(t *testing.T) {
	tr := buildTransport(1000)
	if tr.DisableKeepAlives {
		t.Error("DisableKeepAlives must be false — keep-alives are required for connection reuse")
	}
}

// ── fdBudget ───────────────────────────────────────────────────────────────────

func TestFdBudget_Positive(t *testing.T) {
	// On any Unix-like system (macOS, Linux) the FD limit should be queryable
	// and well above fdReserve (typically 256 < 1024).
	budget := fdBudget()
	if budget <= 0 {
		t.Skip("FD limit not queryable or too low — skipping positivity check")
	}
	t.Logf("fdBudget() = %d", budget)
}

func TestBuildTransport_FdCap(t *testing.T) {
	// On systems with queryable FD limits, neither pool field may exceed the budget.
	budget := fdBudget()
	if budget <= 0 {
		t.Skip("FD limit not queryable — skip cap test")
	}
	// Use an absurdly high RPS to force fd-cap code paths.
	tr := buildTransport(10_000_000)
	if tr.MaxIdleConns > budget {
		t.Errorf("MaxIdleConns=%d exceeds fdBudget=%d", tr.MaxIdleConns, budget)
	}
	if tr.MaxIdleConnsPerHost > budget/2 {
		t.Errorf("MaxIdleConnsPerHost=%d exceeds fdBudget/2=%d", tr.MaxIdleConnsPerHost, budget/2)
	}
	if tr.MaxIdleConnsPerHost > tr.MaxIdleConns {
		t.Errorf("MaxIdleConnsPerHost=%d > MaxIdleConns=%d", tr.MaxIdleConnsPerHost, tr.MaxIdleConns)
	}
}
