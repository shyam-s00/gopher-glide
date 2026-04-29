package snap

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ── Options ───────────────────────────────────────────────────────────────────

// PruneOptions controls which snapshots SelectForPrune marks as deletion
// candidates. All active filters are OR-combined — a snapshot is a candidate
// if it matches any one of them.
//
// At least one filter must be active; validation is left to the caller.
type PruneOptions struct {
	// KeepLast retains the N most-recent snapshots and marks all older ones as
	// candidates. Zero means this filter is inactive.
	KeepLast int

	// OlderThan marks snapshots whose Date is before (now - OlderThan) as
	// candidates. Zero means this filter is inactive.
	OlderThan time.Duration

	// Tag marks every snapshot whose Tag exactly matches this value as a
	// candidate. Empty string means this filter is inactive.
	Tag string

	// IDs is an explicit set of 1-based snapshot IDs to delete.
	// Empty slice means this filter is inactive.
	// This is the hook used by the JetBrains Snap Explorer tool window to
	// pass the user's multi-selection directly to the CLI.
	IDs []int

	// Now is the reference time used for OlderThan comparisons.
	// When zero, time.Now() is used. Exposed for deterministic testing.
	Now time.Time
}

// ── Candidate ─────────────────────────────────────────────────────────────────

// PruneCandidate pairs a SnapInfo with the human-readable reason it was
// selected for deletion.
type PruneCandidate struct {
	SnapInfo
	Reason string `json:"reason"`
}

// ── Report ────────────────────────────────────────────────────────────────────

// PruneReport is the stable JSON contract emitted by `gg snap prune
// --reporter json`. The JetBrains plugin parses this to:
//   - show a preview in the Snap Explorer tool window (dry_run: true)
//   - confirm deletions were applied (dry_run: false, deleted count)
type PruneReport struct {
	DryRun     bool               `json:"dry_run"`
	SnapDir    string             `json:"snap_dir"`
	Candidates []PruneCandidateJS `json:"candidates"`
	Deleted    int                `json:"deleted"`
	Errors     []string           `json:"errors"`
}

// PruneCandidateJS is the JSON-serializable representation of a single
// candidate used inside PruneReport.
type PruneCandidateJS struct {
	ID     int    `json:"id"`
	Tag    string `json:"tag"`
	Date   string `json:"date"` // RFC 3339, UTC
	File   string `json:"file"`
	Reason string `json:"reason"`
}

// JSON serialises the report as indented JSON.
func (r PruneReport) JSON() (string, error) {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// BuildPruneReport constructs a PruneReport from a candidate slice.
// deleted and errs are the return values from Delete(); pass (0, nil) for
// dry-run reports.
func BuildPruneReport(dir string, candidates []PruneCandidate, dryRun bool, deleted int, errs []error) PruneReport {
	js := make([]PruneCandidateJS, len(candidates))
	for i, c := range candidates {
		js[i] = PruneCandidateJS{
			ID:     c.ID,
			Tag:    c.Tag,
			Date:   c.Date.UTC().Format(time.RFC3339),
			File:   c.FileName,
			Reason: c.Reason,
		}
	}
	errStrs := make([]string, len(errs))
	for i, e := range errs {
		errStrs[i] = e.Error()
	}
	return PruneReport{
		DryRun:     dryRun,
		SnapDir:    dir,
		Candidates: js,
		Deleted:    deleted,
		Errors:     errStrs,
	}
}

// ── Core logic ────────────────────────────────────────────────────────────────

// SelectForPrune evaluates infos against opts and returns a deduplicated,
// oldest-first ordered slice of deletion candidates.
//
// Filter rules:
//   - --ids:        exact ID set membership
//   - --tag:        exact tag equality
//   - --older-than: Date < (now - OlderThan)
//   - --keep-last:  every snapshot beyond the N most-recent
//
// Filter is OR-combined. A snapshot matching more than one filter appears
// only once; the reason from the first matching filter is retained.
func SelectForPrune(infos []SnapInfo, opts PruneOptions) []PruneCandidate {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}

	// keyed by file path so duplicates are impossible
	seen := make(map[string]PruneCandidate, len(infos))

	add := func(info SnapInfo, reason string) {
		if _, dup := seen[info.Path]; !dup {
			seen[info.Path] = PruneCandidate{SnapInfo: info, Reason: reason}
		}
	}

	// ── --ids ─────────────────────────────────────────────────────────────────
	if len(opts.IDs) > 0 {
		idSet := make(map[int]struct{}, len(opts.IDs))
		for _, id := range opts.IDs {
			idSet[id] = struct{}{}
		}
		for _, info := range infos {
			if _, ok := idSet[info.ID]; ok {
				add(info, fmt.Sprintf("id %d selected", info.ID))
			}
		}
	}

	// ── --tag ─────────────────────────────────────────────────────────────────
	if opts.Tag != "" {
		for _, info := range infos {
			if info.Tag == opts.Tag {
				add(info, fmt.Sprintf("tag %q matches", opts.Tag))
			}
		}
	}

	// ── --older-than ──────────────────────────────────────────────────────────
	if opts.OlderThan > 0 {
		cutoff := now.Add(-opts.OlderThan)
		for _, info := range infos {
			if info.Date.Before(cutoff) {
				add(info, fmt.Sprintf("older than %s", fmtDuration(opts.OlderThan)))
			}
		}
	}

	// ── --keep-last ───────────────────────────────────────────────────────────
	if opts.KeepLast > 0 && opts.KeepLast < len(infos) {
		// infos are sorted oldest-first by List(); copy and sort newest-first.
		// Tiebreaker: reverse-lexicographic FileName so that when two snapshots
		// share the same timestamp the one with the lexicographically greater
		// filename is consistently treated as "newer" and therefore kept.
		// This makes keep/prune decisions fully deterministic regardless of how
		// many files were created within the same second.
		sorted := make([]SnapInfo, len(infos))
		copy(sorted, infos)
		sort.SliceStable(sorted, func(i, j int) bool {
			if !sorted[i].Date.Equal(sorted[j].Date) {
				return sorted[i].Date.After(sorted[j].Date)
			}
			// Tiebreaker: greater FileName sorts first (treated as "newer").
			return sorted[i].FileName > sorted[j].FileName
		})
		for _, info := range sorted[opts.KeepLast:] {
			add(info, fmt.Sprintf("beyond keep-last %d", opts.KeepLast))
		}
	}

	// Collect and order candidates oldest-first for stable, readable output.
	// Tiebreaker: lexicographic FileName so the table/JSON output is stable
	// when multiple candidates share the same timestamp.
	out := make([]PruneCandidate, 0, len(seen))
	for _, c := range seen {
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Date.Equal(out[j].Date) {
			return out[i].Date.Before(out[j].Date)
		}
		return out[i].FileName < out[j].FileName
	})
	return out
}

// Delete removes each candidate file from disk.
// It returns the count of successfully deleted files and a slice of per-file
// errors. A single failure does not abort the remaining deletions.
func Delete(candidates []PruneCandidate) (deleted int, errs []error) {
	for _, c := range candidates {
		if err := os.Remove(c.Path); err != nil {
			errs = append(errs, fmt.Errorf("remove %q: %w", c.FileName, err))
			continue
		}
		deleted++
	}
	return
}

// ── Parsers ───────────────────────────────────────────────────────────────────

// ParseOlderThan parses a duration string for use with --older-than.
// It extends Go's standard time.ParseDuration syntax with a "d" suffix for
// days:
//
//	"30d"   → 30 × 24h
//	"720h"  → standard Go duration
//
// Returns a non-nil error when the string is malformed or the value is
// negative. Returns zero duration for an empty string (filter inactive).
func ParseOlderThan(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	if strings.HasSuffix(s, "d") {
		raw := strings.TrimSuffix(s, "d")
		days, err := strconv.Atoi(raw)
		if err != nil || days <= 0 {
			return 0, fmt.Errorf("snap: invalid --older-than %q: expected a positive integer before 'd'", s)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("snap: invalid --older-than %q: %w", s, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("snap: --older-than must be positive, got %q", s)
	}
	return d, nil
}

// ParseIDs parses a comma-separated list of positive 1-based snapshot IDs for
// use with --ids.
//
//	"1,3,5" → [1, 3, 5]
//
// Returns nil for an empty string (filter inactive). Returns a non-nil error
// when any token is not a positive integer.
func ParseIDs(s string) ([]int, error) {
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	ids := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		id, err := strconv.Atoi(p)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("snap: invalid --ids value %q: each ID must be a positive integer", p)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// ── internal helpers ──────────────────────────────────────────────────────────

// fmtDuration produces a short human-readable duration string, preferring
// "Nd" for multiples of 24 h so the log output mirrors what the user typed.
func fmtDuration(d time.Duration) string {
	if d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", int(d.Hours())/24)
	}
	return d.String()
}
