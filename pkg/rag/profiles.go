package rag

// FixedProfiles returns immutable MVP retrieval profiles so behavior stays
// auditable and reproducible until profile governance is expanded.
func FixedProfiles() map[string]FixedProfile {
	return map[string]FixedProfile{
		"default_research": {
			ID:                  "default_research",
			DefaultMode:         ModeHybrid,
			BM25TopN:            120,
			SemanticTopN:        120,
			WeightBM25:          0.60,
			WeightCosine:        0.35,
			WeightFreshness:     0.05,
			WeightMetadataBoost: 0.00,
			PerSourceCap:        3,
		},
		"decisions_recent": {
			ID:                  "decisions_recent",
			DefaultMode:         ModeHybrid,
			BM25TopN:            150,
			SemanticTopN:        80,
			WeightBM25:          0.65,
			WeightCosine:        0.20,
			WeightFreshness:     0.15,
			WeightMetadataBoost: 0.10,
			PerSourceCap:        4,
			PreferNotesPolicy:   true,
		},
		"templates_lookup": {
			ID:                  "templates_lookup",
			DefaultMode:         ModeKeywordOnly,
			BM25TopN:            200,
			SemanticTopN:        0,
			WeightBM25:          0.90,
			WeightCosine:        0.00,
			WeightFreshness:     0.00,
			WeightMetadataBoost: 0.10,
			PerSourceCap:        5,
		},
	}
}

// ResolveProfile enforces deterministic fallback order to avoid silent behavior
// drift when callers pass unknown profile IDs.
func ResolveProfile(profileID, defaultProfileID string) FixedProfile {
	profiles := FixedProfiles()
	if p, ok := profiles[profileID]; ok {
		return p
	}
	if p, ok := profiles[defaultProfileID]; ok {
		return p
	}
	return profiles["default_research"]
}
