package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/llm"
)

// research is the parent-agent's "dispatch parallel investigations" tool.
//
// Each invocation fans out one or more read-only sub-agents that share the
// parent's working tree on disk but otherwise run isolated LLM conversations
// with a fixed, restricted tool catalogue (read / glob / grep / webfetch).
// The parent gets back one summary per task, in task order. Sub-agents
// cannot mutate state, cannot call platform tools, and cannot recursively
// spawn further research calls.
//
// The tool deliberately lives in-process: a research goroutine talks to the
// same `/api/llm/v1/responses` proxy the parent uses, with the same session
// token. No runner-side fan-out, no new platform endpoint, no DB row. That
// keeps the surface area of "what a fork is" small enough to reason about
// in one file.

const (
	// researchMaxTasks caps the array length per call. The ceiling protects
	// LLM spend (an N-way fan-out is N× the cost of one investigation) and
	// keeps the parent's tool_call result small enough to forward through
	// the IPC layer without truncation pressure.
	researchMaxTasks = 10

	// researchDefaultMaxSteps is the per-sub-agent LLM round-trip budget when
	// the caller doesn't specify one. 64 covers most read-grep-read cycles
	// with room to spare; deeper investigations should pass an explicit
	// max_steps.
	researchDefaultMaxSteps = 64

	// researchMaxStepsCap is the hard ceiling on max_steps. Above this the
	// tool refuses up-front rather than letting a runaway prompt eat the
	// parent's wall-clock budget unnoticed.
	researchMaxStepsCap = 9999
)

// researchChildSystemPrompt is the operating contract every sub-agent
// receives as its `instructions`. Hard-coded (not caller-overridable) so the
// parent cannot accidentally widen the sub-agent's role — the tool's
// read-only contract is enforced by the catalogue too, but a prompt that
// matches the catalogue keeps the LLM's behaviour aligned with reality.
const researchChildSystemPrompt = `You are a focused investigation sub-agent dispatched by a parent engineering agent. Your job is to research one specific question against the working tree at /workspace, then return a single final assistant message that summarizes what you found.

Rules:
- Your final message — the one with no tool calls — is the entire response your parent will receive. Make it useful: state the conclusion clearly, cite file paths and line numbers, and note any assumptions you had to make.
- You CANNOT write files, edit files, run shell commands, call platform tools, or modify any state. Your tool catalogue is strictly read-only (read, glob, grep, webfetch).
- You CANNOT ask the parent clarifying questions. If the prompt is ambiguous, choose the most reasonable interpretation and call it out in your summary.
- You CANNOT spawn further sub-agents. Do not call a tool named "research".
- Be efficient: every LLM round-trip is budgeted. When you have enough to answer, stop calling tools and write your summary.`

// researchArgs is the JSON shape the LLM sends to the tool.
type researchArgs struct {
	Tasks []researchTask `json:"tasks"`
	Model string         `json:"model,omitempty"`
}

type researchTask struct {
	Prompt   string `json:"prompt"`
	MaxSteps int    `json:"max_steps,omitempty"`
}

// researchResult is one entry in the returned `results` array. Keeping the
// shape narrow (outcome / summary / steps_used / error) means the parent's
// LLM can consume the JSON without inventing parsing rules; everything
// useful is at the top level.
type researchResult struct {
	Outcome   string `json:"outcome"`         // "ok" | "step_limit" | "error"
	Summary   string `json:"summary"`         // last assistant text the child emitted
	StepsUsed int    `json:"steps_used"`      // LLM round-trips this child consumed
	Error     string `json:"error,omitempty"` // populated only when Outcome == "error"
}

// researchTool wires a parent-supplied LLM client and default model into the
// `research` Tool surface. The client itself is goroutine-safe (it wraps a
// stock *http.Client) so every sub-agent on one Call shares it.
type researchTool struct {
	client       *llm.Client
	defaultModel string
}

// NewResearchTool is exported so tools/module.go can wire it next to the
// other locals once it has resolved the LLM client and the agent's default
// model from config. A nil client makes the tool refuse every call — that's
// what happens in offline tests where local.All() is used without a wired
// research dep.
func NewResearchTool(client *llm.Client, defaultModel string) Tool {
	return &researchTool{client: client, defaultModel: defaultModel}
}

func (t *researchTool) Name() string { return "research" }

func (t *researchTool) Description() string {
	return strings.Join([]string{
		"Dispatch up to 10 read-only investigation sub-agents IN PARALLEL. Each sub-agent runs its own LLM conversation against the same /workspace tree with a strictly read-only catalogue (read, glob, grep, webfetch) and returns one final summary message.",
		"Use when you have several INDEPENDENT investigation questions that don't depend on each other's answers — exploring unrelated modules, checking several hypotheses, or comparing config files concurrently. The wall-clock win is the slowest sub-agent, not the sum of all of them; the context win is that only summaries (not full transcripts) come back to you.",
		"DO NOT use for a single question (just call read/grep yourself), for anything that needs to write/commit/comment/run commands (sub-agents can't), for sequentially dependent steps (run them serially in your own turn), or as a way to get a 'second opinion' on one prompt.",
		"Returns: {results: [{outcome, summary, steps_used}, ...]} in task order. outcome is 'ok' (finished with a summary), 'step_limit' (budget exhausted mid-flight; summary is the last text seen), or 'error' (transport/internal failure).",
	}, " ")
}

func (t *researchTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"tasks": map[string]any{
				"type":        "array",
				"minItems":    1,
				"maxItems":    researchMaxTasks,
				"description": fmt.Sprintf("Independent investigation prompts to run in parallel (1..%d). Each runs an isolated read-only sub-agent.", researchMaxTasks),
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"prompt": map[string]any{
							"type":        "string",
							"description": "Focused brief for the sub-agent. State exactly what to investigate, what shape of answer you want, and any pointers (file paths, symbols, search terms).",
						},
						"max_steps": map[string]any{
							"type":        "integer",
							"description": fmt.Sprintf("Per-sub-agent LLM round-trip budget. Default %d; hard cap %d. Set lower for narrow lookups, higher for deeper investigations.", researchDefaultMaxSteps, researchMaxStepsCap),
							"minimum":     1,
							"maximum":     researchMaxStepsCap,
						},
					},
					"required": []string{"prompt"},
				},
			},
			"model": map[string]any{
				"type":        "string",
				"description": "Optional model override applied to every sub-agent on this call. Defaults to the parent session's model.",
			},
		},
		"required": []string{"tasks"},
	}
}

func (t *researchTool) Call(ctx context.Context, raw json.RawMessage) (any, error) {
	if t.client == nil {
		// Tool was registered without a wired LLM client (offline test or
		// misconfiguration). Refuse loudly rather than letting an LLM call
		// fail with a bare "missing endpoint" — the message names the
		// fix so an operator sees what to wire.
		return nil, errors.New("research: tool is not wired to an LLM client. The agent must build the registry via local.AllWithResearch(...) with a valid client; offline test setups should leave 'research' out of the catalogue.")
	}
	var a researchArgs
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	if len(a.Tasks) == 0 {
		return nil, errors.New("research: 'tasks' array is empty. research dispatches N parallel sub-agents — pass at least one task. For a single investigation, prefer calling 'read'/'grep' directly rather than spinning up a sub-agent for one prompt.")
	}
	if len(a.Tasks) > researchMaxTasks {
		return nil, fmt.Errorf("research: tasks=%d exceeds the per-call limit of %d. This ceiling guards LLM cost — split your investigation into smaller fan-outs (call research multiple times) or merge related prompts into one.", len(a.Tasks), researchMaxTasks)
	}
	model := strings.TrimSpace(a.Model)
	if model == "" {
		model = t.defaultModel
	}
	if model == "" {
		return nil, errors.New("research: no model resolved. The agent's default model is empty and the call didn't supply one — set HANGRIX_LLM_MODEL in the runner config or pass `model` in the tool args.")
	}
	for i, task := range a.Tasks {
		if strings.TrimSpace(task.Prompt) == "" {
			return nil, fmt.Errorf("research: tasks[%d].prompt is empty. Every sub-agent needs a focused brief — state what to investigate and what answer shape you want back.", i)
		}
		if task.MaxSteps < 0 {
			return nil, fmt.Errorf("research: tasks[%d].max_steps=%d must be >= 0 (use 0 or omit for the default of %d).", i, task.MaxSteps, researchDefaultMaxSteps)
		}
		if task.MaxSteps > researchMaxStepsCap {
			return nil, fmt.Errorf("research: tasks[%d].max_steps=%d exceeds the hard cap of %d. If you need a deeper investigation, narrow the prompt instead — a runaway loop usually means the brief was too vague.", i, task.MaxSteps, researchMaxStepsCap)
		}
	}

	results := make([]researchResult, len(a.Tasks))
	var wg sync.WaitGroup
	for i, task := range a.Tasks {
		i, task := i, task
		steps := task.MaxSteps
		if steps == 0 {
			steps = researchDefaultMaxSteps
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = runResearchChild(ctx, t.client, model, task.Prompt, steps)
		}()
	}
	wg.Wait()
	return map[string]any{"results": results}, nil
}

// runResearchChild is the headless replica of runtime.Loop.handleEvent,
// minus IPC and minus everything that mutates session state. It owns:
//
//   - A fresh, read-only catalogue (read / glob / grep / webfetch). Each
//     child builds its own via local.All() so the per-call ReadTracker is
//     isolated; the parent's tracker is unaffected, and concurrent children
//     don't share read history with each other.
//   - Its own message slice. No system-prompt reuse from the parent; the
//     child's contract is fixed by researchChildSystemPrompt.
//   - Its own LLM round-trip counter. We loop until the assistant returns
//     no tool calls (outcome "ok"), the budget exhausts (outcome
//     "step_limit"), or an error fires (outcome "error"). In every case
//     Summary is the latest non-empty assistant text we observed.
//
// The shared *llm.Client is goroutine-safe by construction (it wraps a
// stock *http.Client), so the sibling goroutines on one Call do not need
// any mutex around it.
func runResearchChild(ctx context.Context, client *llm.Client, model, prompt string, maxSteps int) researchResult {
	allowed := researchChildToolSet()
	catalog := make([]llm.ToolDescriptor, 0, len(allowed))
	byName := make(map[string]Tool, len(allowed))
	for _, tool := range allowed {
		catalog = append(catalog, llm.ToolDescriptor{
			Name:        tool.Name(),
			Description: tool.Description(),
			Parameters:  tool.Schema(),
		})
		byName[tool.Name()] = tool
	}

	messages := []llm.Message{
		{Role: "user", Content: prompt},
	}
	lastContent := ""

	for step := 1; step <= maxSteps; step++ {
		if err := ctx.Err(); err != nil {
			return researchResult{Outcome: "error", Summary: lastContent, StepsUsed: step - 1, Error: err.Error()}
		}
		resp, err := client.Create(ctx, &llm.CreateRequest{
			Model:        model,
			Instructions: researchChildSystemPrompt,
			Messages:     messages,
			Tools:        catalog,
		})
		if err != nil {
			return researchResult{Outcome: "error", Summary: lastContent, StepsUsed: step - 1, Error: err.Error()}
		}
		if resp.Content != "" {
			lastContent = resp.Content
		}
		// Append the assistant turn to the child's history before
		// dispatching its tool calls — providers that round-trip
		// reasoning blocks need them threaded through verbatim, same as
		// the parent loop's AppendAssistantWithReasoning path.
		messages = append(messages, llm.Message{
			Role:               "assistant",
			Content:            resp.Content,
			Reasoning:          resp.Reasoning,
			ReasoningSignature: resp.ReasoningSignature,
			ToolCalls:          resp.ToolCalls,
		})
		if len(resp.ToolCalls) == 0 {
			return researchResult{Outcome: "ok", Summary: lastContent, StepsUsed: step}
		}
		for _, call := range resp.ToolCalls {
			tool, ok := byName[call.Name]
			var output string
			if !ok {
				// Whitelist enforced here, not at the LLM layer: the child
				// has a catalogue advertising only the four read-only tools,
				// but a stale conversation or a misbehaving model could still
				// emit a call to anything. Returning a structured error gives
				// the child a chance to self-correct and try a real tool.
				output = errorJSON(fmt.Sprintf("unknown tool %q. This sub-agent's catalogue is read-only and only exposes: read, glob, grep, webfetch. Use one of those to investigate; you cannot write, edit, run shell commands, or call platform tools from here.", call.Name))
			} else {
				val, callErr := tool.Call(ctx, json.RawMessage(call.Arguments))
				switch {
				case callErr != nil:
					output = errorJSON(callErr.Error())
				case val == nil:
					output = "null"
				default:
					body, mErr := json.Marshal(val)
					if mErr != nil {
						output = errorJSON(fmt.Sprintf("marshal tool result: %s", mErr))
					} else {
						output = string(body)
					}
				}
			}
			messages = append(messages, llm.Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    output,
			})
		}
	}
	return researchResult{Outcome: "step_limit", Summary: lastContent, StepsUsed: maxSteps}
}

// researchChildToolSet returns the per-child tool catalogue. We rebuild it
// per call (not per process) so each sub-agent gets its own ReadTracker
// and its own tool instances — siblings can read overlapping paths
// concurrently without their `read` calls interfering through a shared
// tracker, and the parent's tracker stays untouched.
func researchChildToolSet() []Tool {
	whitelist := map[string]struct{}{
		"read":     {},
		"glob":     {},
		"grep":     {},
		"webfetch": {},
	}
	all := All()
	out := make([]Tool, 0, len(whitelist))
	for _, tool := range all {
		if _, ok := whitelist[tool.Name()]; ok {
			out = append(out, tool)
		}
	}
	return out
}

func errorJSON(msg string) string {
	b, err := json.Marshal(map[string]any{"error": msg})
	if err != nil {
		// json.Marshal of a string-valued map cannot fail in practice;
		// fall back to a fixed-shape envelope so the child's history
		// never goes ragged.
		return `{"error":"internal: failed to encode tool error"}`
	}
	return string(b)
}

// AllWithResearch returns the canonical local catalogue with `research`
// appended. The shared *llm.Client + default model are wired through so the
// research goroutines can talk to the LLM proxy with the parent's session
// token. Production wiring (tools/module.go) calls BuildWithResearch
// when it needs the AsyncLifecycle handle too; AllWithResearch stays as
// the slim "just the tools" convenience for older callers.
func AllWithResearch(client *llm.Client, defaultModel string) []Tool {
	return append(All(), NewResearchTool(client, defaultModel))
}

// BuildWithResearch is Build + the research tool, returned as a Bundle.
// This is the production constructor: the runtime needs the
// AsyncLifecycle handle, and the research tool must share the parent's
// LLM client. Splitting from AllWithResearch keeps tests (which use
// All / AllWithResearch and don't care about lifecycle hooks)
// untouched.
func BuildWithResearch(client *llm.Client, defaultModel string) Bundle {
	b := Build()
	b.Tools = append(b.Tools, NewResearchTool(client, defaultModel))
	return b
}
