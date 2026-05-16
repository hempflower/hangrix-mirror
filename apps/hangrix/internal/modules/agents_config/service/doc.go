// Package service decodes the M7a agent / host yaml configs into the
// sibling domain types and enforces every invariant principle 7
// requires. Pure functions today — no DB, no HTTP, no IPC; consumers
// (M7a Phase 2 dispatcher, M7a admin handlers) will graft on later.
//
// Layering:
//
//   - yaml.v3 is imported here and ONLY here. domain/ stays free of
//     wire-format quirks (custom unmarshalers, alias trees, …).
//   - Each parse function decodes into a private wire struct, validates
//     it against the rules in docs/agent-config.md, and produces a
//     pristine domain value. The split is deliberate: the wire shape
//     mirrors YAML exactly (with `yaml:"..."` tags) while the domain
//     shape mirrors the runtime contract (lock-file keys, normalised
//     enums, parsed AgentRef).
//   - KnownFields(true) is set on every decoder. Unknown keys are an
//     error, not a warning — schema-strictness is the whole point of
//     this module.
//
// What lives elsewhere:
//
//   - File I/O (reading agent.yml / agents.yml / agents.lock from disk
//     or a git tree) belongs to the caller. These functions take []byte.
//   - Lock-file resolution (turning `@v1.2.3` into a sha) belongs in
//     M7a Phase 2; parse_lock.go marks the seam with a TODO.
package service
