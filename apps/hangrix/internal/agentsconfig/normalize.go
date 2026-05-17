package agentsconfig

// NormalizeHostConfig is currently a no-op. The schema used to default
// `mention_by` here; that field has been removed (any mention with a
// matching trigger now wakes the role). The function is kept so
// existing call sites that follow the parse → normalize convention
// don't need to be touched; a future schema-level default would land
// here as well.
func NormalizeHostConfig(cfg *HostConfig) {
	_ = cfg
}
