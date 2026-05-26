// Package domain declares the agent HTTP API contract: the cross-module
// dependencies the platform business logic consumes and the small result
// envelope its operations return.
//
// The platform_api module sits above the issue / repo / runner / git
// modules. It does NOT own its own persistence — every operation calls
// into existing domain interfaces. The split is deliberate: when the same
// "merge an issue" action is reachable both from the web UI (issue
// handler) and the agent (v1 REST API), only one piece of code should do
// the work.
package domain

// Result is what a platform operation emits. Text is the textual
// representation returned to the caller; IsError surfaces "the operation
// ran but the outcome was a structured failure" — distinct from a
// Go-level error which collapses the whole call.
//
// Operations that produce structured data marshal it into Text as JSON.
type Result struct {
	Text    string
	IsError bool
}
