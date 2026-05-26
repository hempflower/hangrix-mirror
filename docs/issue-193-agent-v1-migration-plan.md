# Agent platform-tool migration plan: legacy RPC -> `/api/v1`

## Goal

Migrate `apps/hangrix-agent` from the deprecated RPC-style platform tool transport (`POST /api/agent/tools/{name}`) to the existing GitHub-style REST surface under `/api/v1`, without changing the LLM-visible tool catalogue or the runner/session bootstrap contract.

## Data model

No persistent data-model change is required.

Entities already exist and stay authoritative on the server side:

- **Session-scoped actor** — authenticated by `hgxs_` bearer token; server derives current repo / issue scope and permissions.
- **Issue / comment / todo / contribution / review / release / session** — unchanged resource model already exposed by `apps/hangrix/internal/modules/platform_api/handler/v1_*.go`.
- **Runner bootstrap payload** — existing `base_url` remains the single network anchor injected as `HANGRIX_PLATFORM_BASE_URL`.

Migration strategy:

- **No goose/sqlc work**.
- Keep the server-side legacy `/api/agent/tools/{name}` routes temporarily for backward compatibility during rollout.
- Agent migration is purely client-side: new HTTP request shapes, paths, methods, and response decoding.

## Domain objects / interfaces

Introduce an explicit REST request layer inside `apps/hangrix-agent/internal/tools/platform/` instead of the current generic `Call(name, args)` RPC shim.

Recommended types:

- `type Endpoint struct { Method string; PathTemplate string; QueryEncoder func(args any) url.Values; BodyEncoder func(args any) ([]byte, string, error); ResponseMode ResponseMode }`
- `type ResponseMode string`
  - `ResponseJSON`
  - `ResponseText`
  - `ResponseNoContent`
- `type Client struct { baseURL string; token string; http *http.Client }` remains the shared transport.
- `type Tool struct { name string; description string; schema map[string]any; endpoint Endpoint; client *Client }`
- `type attachmentTool struct { ... }` remains separate for multipart upload.

Core interface boundary:

- Keep the existing `local.Tool` boundary unchanged so runtime / registry wiring does not care whether a platform tool is backed by RPC or REST.
- Replace the current implicit contract of `tool name == URL suffix` with an explicit **tool-to-endpoint mapping table** in the agent.
- Parsing should remain schema-first at the agent boundary: each tool still receives raw JSON args from the LLM, but the platform layer converts them into route params / query params / request bodies before issuing HTTP.

Why explicit mapping instead of a new generic REST dispatcher stringly-typed by conventions:

- v1 routes are not uniform enough for a pure `/{resource}` convention (`issue_create` is repo-scoped, `issue_todo_update` may create or patch, `issue_read_by_number` is explicit-number while most issue tools are current-issue scoped).
- The mapping table becomes the migration inventory and prevents hidden path logic spread across many switch statements.
- It is safer for long-term evolution: future tools can add bespoke encoders without changing runtime semantics.

## API / handler design

Server-side `/api/v1` handlers already exist. The agent should target them directly.

### Tool -> v1 endpoint mapping

#### Issue / comment tools

| Tool | Method | Endpoint | Notes |
|---|---|---|---|
| `issue_read` | `GET` | `/api/v1/issues/current` | No body |
| `issue_comment_read` | `GET` | `/api/v1/issues/current/comments/{comment_id}` | `comment_id` becomes path param |
| `issue_mergeable` | `GET` | `/api/v1/issues/current/mergeability` | No body |
| `issue_todo_list` | `GET` | `/api/v1/issues/current/todos` | No body |
| `issue_children` | `GET` | `/api/v1/issues/current/children` | No body |
| `issue_read_by_number` | `GET` | `/api/v1/issues/{issue_number}` | Explicit numbered issue |
| `issue_checks` | `GET` | `/api/v1/issues/current/checks` | No body |
| `roster_list` | `GET` | `/api/v1/issues/current/sessions` | Tool name differs from resource name |
| `issue_create` | `POST` | `/api/v1/repos/{repo_id}/issues` | Agent should derive `{repo_id}` from `GET /api/v1/me` once and cache it, because the tool schema only carries `title/body/parent` |
| `issue_comment` | `POST` | `/api/v1/issues/current/comments` | JSON body unchanged |
| `issue_edit` | `PATCH` | `/api/v1/issues/current` | JSON body unchanged |
| `issue_review_vote` | `POST` | `/api/v1/issues/current/reviews` | Rename payload field `contribution_id` -> `contribution_id`; same logical content, but must match v1 request shape |
| `issue_close` | `POST` | `/api/v1/issues/current/close` | Optional JSON body `{reason}` |
| `issue_merge` | `POST` | `/api/v1/issues/current/merge` | Optional JSON body `{message}` |
| `session_recover` | `POST` | `/api/v1/issues/current/sessions/{session_id}/recover` | Path param + empty body |
| `issue_attachment_upload` | `POST` | `/api/v1/issues/current/attachments` | Multipart stays special-case |
| `issue_todo_update` | split dispatch | see below | Create vs update depends on `todo_id` |

`issue_todo_update` must branch:

- `todo_id == 0` or omitted -> `POST /api/v1/issues/current/todos` with body `{content,status,position}`
- `todo_id != 0` -> `PATCH /api/v1/issues/current/todos/{todo_id}` with body `{status,content}`

#### Contribution tools

| Tool | Method | Endpoint | Notes |
|---|---|---|---|
| `contribution_list` | `GET` | `/api/v1/issues/current/contributions?include_closed=...&include_merged=...` | Query params instead of JSON body |
| `contribution_read` | `GET` | `/api/v1/issues/current/contributions/{id}` | Path param |
| `contribution_set_meta` | `PATCH` | `/api/v1/issues/current/contributions/{id}` | JSON body `{title,description}` |
| `contribution_apply` | `POST` | `/api/v1/issues/current/contributions/{id}/apply` | Optional `{message}` |
| `contribution_close` | `POST` | `/api/v1/issues/current/contributions/{id}/close` | Optional `{reason}` |

#### Release tools

| Tool | Method | Endpoint | Notes |
|---|---|---|---|
| `release_create` | `POST` | `/api/v1/releases` | JSON body `{tag_name,title,notes}` |
| `release_upload_asset` | `POST` | `/api/v1/releases/{release_id}/assets` | JSON body unchanged |
| `release_publish` | `POST` | `/api/v1/releases/{release_id}/publish` | Empty body |
| `release_update` | `PATCH` | `/api/v1/releases/{release_id}` | JSON body `{title,notes,tag_name}` |
| `release_delete` | `DELETE` | `/api/v1/releases/{release_id}` | No response body on success |

### Response handling changes

The current agent expects legacy responses shaped as:

```json
{ "is_error": false, "text": "..." }
```

The v1 surface instead returns ordinary REST responses:

- `200/201` with JSON bodies for most success cases
- `204` for delete success
- `4xx/5xx` with structured error bodies

So the new client layer should:

- treat **2xx** as success
- decode JSON body when present
- return `map[string]any{"ok": true}` (or similar minimal sentinel) for `204` so the tool call still yields a structured result
- treat **4xx** as tool-visible failures, not transport failures
- preserve server validation detail when available

Important behavioral change: legacy soft failures rode on `200 + is_error=true`; v1 expresses permission/validation/not-found as real HTTP status codes. The agent should surface these to the LLM as structured tool-call errors rather than collapsing everything into opaque Go errors.

Recommended normalization helper:

- Parse non-2xx body as `{error, code, details}` when available.
- Return a structured object like:
  - `{"is_error": true, "status": 422, "error": "title is required", "details": [...]}`
- This preserves the current runtime expectation that a tool can fail semantically without aborting the whole LLM turn.

## Business logic

### 1. Replace generic RPC call path with endpoint-aware execution

Current audit of `apps/hangrix-agent/internal/tools/platform/platform.go`:

- `Client.Call()` hardcodes `POST <base>/<tool-name>` and always sends JSON args.
- `Tool.Call()` assumes the legacy `{is_error,text}` envelope.
- `attachmentTool.Call()` special-cases multipart, but still posts to `/<tool-name>` and expects the same envelope.
- `All()` stores only descriptor metadata; it has no per-tool transport metadata.

This is exactly the wrong abstraction for v1 because method, path, query encoding, and success body shape now vary per tool.

### 2. Add endpoint metadata beside each descriptor

`All()` should become the single source of truth for:

- tool schema exposed to the LLM
- endpoint method/path mapping
- optional argument transformation rules
- optional response normalization mode

That keeps migration localized to the platform tools package.

### 3. Add small argument decoders, not one giant reflection system

Use targeted request builders per irregular tool:

- path-param tools: decode only required IDs, inject into template
- query-param tools: decode booleans and omit false defaults unless explicitly set
- split tools (`issue_todo_update`): inspect `todo_id` and choose create vs update endpoint
- repo-scoped tools (`issue_create`): fetch/cached actor context once

Avoid a heavy generic “OpenAPI client” layer; the tool set is small and stable.

### 4. Cache `/api/v1/me` for repo-scoped calls

`issue_create` is the only current tool whose v1 route needs `repoID` while the tool schema intentionally does not ask the LLM for it. The client should therefore expose:

- `func (c *Client) Me(ctx) (*MeResponse, error)`
- lazy in-memory cache of the actor/repo identity for the process lifetime

This avoids teaching the model about repo IDs and avoids runner/bootstrap changes.

### 5. Keep retry semantics at the HTTP transport boundary

Retain the current 3-attempt retry policy for:

- transport errors
- `5xx`

Do **not** retry `4xx`.

### 6. Preserve multipart upload containment logic

`issue_attachment_upload` already contains important workspace-path validation and symlink containment logic. Keep that logic intact; only switch the target path and response decoding to the v1 REST shape.

## Frontend architecture

No frontend or Nuxt changes are required. This migration only changes the agent-side caller in `apps/hangrix-agent` and, at most, comments/docs in `apps/hangrix-runner`.

## Middleware / component system

No new middleware is required.

Cross-cutting client concerns in the agent should be centralized in `platform.Client`:

- bearer auth header
- JSON vs multipart encoding
- retry/backoff
- REST error decoding
- optional `me` identity cache

Do not push v1 path knowledge into runtime loop, registry, or unrelated tool packages.

## Runner changes needed

### Required

No runner protocol or bootstrap payload change is required.

Reason:

- `docs/runner-protocol.md` already defines `base_url` / `HANGRIX_PLATFORM_BASE_URL` as the single anchor.
- The new v1 routes live under the same origin.
- Runner already injects only the origin/base, not full tool URLs.

### Recommended cleanup

Update comments/docs in runner and agent config that still say tools live under `/api/agent/tools/<name>`:

- `apps/hangrix-agent/internal/config/config.go`
- `apps/hangrix-runner/internal/loop/session.go`
- `docs/runner-protocol.md`

Recommended wording:

- `HANGRIX_PLATFORM_BASE_URL` is the unified origin for both LLM proxy and platform REST API.
- Agent derives `/api/llm/v1/...` and `/api/v1/...` paths from it.

### Explicitly not needed

- no new bootstrap fields
- no runner-side endpoint dispatch
- no additional env vars
- no task payload changes

If the platform later wants version negotiation, add a discovery/capabilities endpoint in a separate issue; do not overload this migration with bootstrap versioning.

## Acceptance criteria

1. `apps/hangrix-agent` no longer constructs or calls `/api/agent/tools/{name}` anywhere in production code.
2. Every existing platform tool in `platform.All()` has an explicit v1 mapping, and `issue_todo_update` correctly selects create vs patch by `todo_id`.
3. Tool failures caused by permission or validation errors are surfaced to the LLM as structured semantic errors from v1 `4xx` responses rather than as opaque transport failures.
4. `issue_create` succeeds without changing the LLM-visible tool schema, by resolving repo scope internally from authenticated actor context.
5. Runner bootstrap/task env contract remains unchanged: existing `base_url` / `HANGRIX_PLATFORM_BASE_URL` is sufficient for the migrated agent.

## Out of scope

- Removing the legacy server-side `/api/agent/tools/{name}` routes in this issue.
- Changing tool schemas exposed to the LLM.
- Introducing OpenAPI codegen or a generic SDK shared between runner, agent, and server.
- Adding API version negotiation/discovery to runner bootstrap.
- Any web UI or database changes.
