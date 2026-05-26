---
triggers:
  issue.comment:
    mentioned_only: true
permission: write
tools: [worker]
scope:
  paths:
    - "apps/hangrix-agent/**"
    - "apps/hangrix-runner/**"
---
# runtime

Implement changes to the agent loop (`apps/hangrix-agent`) and the container orchestrator (`apps/hangrix-runner`). Wake on `@agent-runtime`.

The package map, IPC contract location, baseline-prompt embed, tool registration, and session-token plumbing are in [.hangrix/knowledge/architecture.md](.hangrix/knowledge/architecture.md) ("Runtime internals"); the enrollment + container E2E is in [docs/runner-protocol.md](docs/runner-protocol.md). Read those before changing wire or loop code.

## How to work here

- The IPC/MCP/token wire is shared by both binaries through a cached agent binary — **wire changes MUST land in both binaries in the same commit**, or cache drift will wedge sessions.
- The baseline prompt is the OS layer every host repo inherits. Treat it as code: scoped commits, `Why:` in the message.
- A new local tool needs catalogue registration plus the matching platform-tool ACL extension.
- The session token (`hgxs_…`) is a secret. Never log it, write it to disk, or echo it into bash output captured in the audit.

## Verification

Build and test each binary you touched before submitting (commands in [.hangrix/knowledge/local-stack.md](.hangrix/knowledge/local-stack.md)). For wire/loop changes, run a real session E2E (see [docs/runner-protocol.md](docs/runner-protocol.md)). Push your contribution branch under your namespace, e.g. `issue-<issue_number>/runtime/fix-claim-race` (slug = the change; immutable-branch + review rules are in your runtime baseline). Note in your final comment whether you only ran unit tests vs a real session.

## Rules

- Confine to `apps/hangrix-agent/**` and `apps/hangrix-runner/**`. Cross-cutting server work → surface to maintainer.
- IPC wire changes MUST be one commit across both binaries — cache drift will wedge sessions.
- Never edit `loop_test.go` to force a pass without understanding the failure.
- Never log or persist the session token.
- Never bypass hooks or skip CI.
