package snap

// SnapSummary extends SnapInfo with data read from the snapshot content,
// providing the richer columns needed by `gg snap list`.
type SnapSummary struct {
	SnapInfo
	TotalRequests int64
	PeakRPS       int
	EndpointCount int
}

// ListAll enumerates every .snap file in dir (via List) and deserialises each
// one to populate the richer SnapSummary fields.
// Files that cannot be deserialised are still included with zero numeric
// fields so a single corrupt file does not break the whole listing.
func ListAll(dir string) ([]SnapSummary, error) {
	infos, err := List(dir)
	if err != nil {
		return nil, err
	}

	summaries := make([]SnapSummary, 0, len(infos))
	for _, info := range infos {
		s := SnapSummary{SnapInfo: info}
		if snap, readErr := Read(info.Path); readErr == nil {
			s.TotalRequests = snap.Meta.TotalRequests
			s.PeakRPS = snap.Meta.PeakRPS
			s.EndpointCount = len(snap.Endpoints)
		}
		summaries = append(summaries, s)
	}
	return summaries, nil
}
