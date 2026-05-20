package agentsconfig

// NormalizeHostConfig fills in defaults for any block that was absent
// from the yaml. Call it after ParseHostConfig so code paths that
// return un-normalized configs (tests, round-trip) can still see the
// difference between "user wrote nothing" and "normalizer filled it".
func NormalizeHostConfig(cfg *HostConfig) {
	if cfg.Issues == nil {
		cfg.Issues = &IssuesConfig{DeleteBranchOnMerge: true}
	}
}
