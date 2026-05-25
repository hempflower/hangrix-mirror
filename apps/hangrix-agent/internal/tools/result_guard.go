package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// defaultResultBudgetBytes is the maximum JSON-encoded byte length a tool
// result may occupy before the guard truncates it and writes the full
// content to a temp file.  64 KiB keeps the LLM context window manageable
// while still fitting typical bash / webfetch outputs without hitting the
// guard during routine use.
const defaultResultBudgetBytes = 64 << 10 // 64 KiB

// guardResult ensures a tool result's JSON payload does not exceed the
// configured budget.  When it does:
//
//  1. The primary content (largest string value in the result) is
//     written in full to a temp file.
//  2. Large string fields are trimmed until the remaining payload fits.
//  3. The modified result carries "truncated":true, a human-readable
//     "truncation_notice" and the "output_file" path so the LLM knows
//     where to find the complete data.
//
// When the result already carries an "output_file" key whose value is
// a non-empty string (e.g. bash background/promoted results), the guard
// reuses that path — the per-job log already holds the full content and we
// do not create a second file.
//
// guardResult handles JSON objects, arrays, and strings.  Booleans,
// numbers, and null are inherently bounded and pass through unchanged.
func guardResult(raw json.RawMessage) json.RawMessage {
	if len(raw) <= defaultResultBudgetBytes {
		return raw
	}

	// Peek at the first non-whitespace byte to determine shape without
	// fully decoding.
	trimmed := bytes.TrimLeft(raw, " \t\r\n")
	if len(trimmed) == 0 {
		return raw
	}
	switch trimmed[0] {
	case '{':
		return guardObject(raw)
	case '[':
		return guardArray(raw)
	case '"':
		return guardString(raw)
	default:
		// null, true, false, number — inherently bounded.
		return raw
	}
}

// guardObject handles JSON objects (the common case for tool results).
func guardObject(raw json.RawMessage) json.RawMessage {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil || len(m) == 0 {
		return raw
	}

	// 1. Determine the output file path.
	outPath, fromValue := existingOutputFile(m)
	if !fromValue {
		// Write the primary content (largest string field anywhere in the
		// tree) to a temp file so the LLM can read the full text.
		_, primaryValue := largestStringField(m)
		if primaryValue == "" {
			return raw // no string fields to write — return as-is
		}
		f, err := os.CreateTemp("", "hangrix-result-*.txt")
		if err != nil {
			return raw
		}
		if _, err := f.WriteString(primaryValue); err != nil {
			f.Close()
			return raw
		}
		f.Close()
		outPath = f.Name()
	}

	// 2. Truncate string fields until the payload fits the budget.
	truncateStringFields(m, defaultResultBudgetBytes)

	// 3. Stamp truncation metadata.
	m["truncated"] = true
	m["truncation_notice"] = fmt.Sprintf(
		"Tool output exceeded the %d-byte result budget. The complete output has been written to %s — use the 'read' tool to retrieve it.",
		defaultResultBudgetBytes, outPath,
	)
	m["output_file"] = outPath

	// 4. Re-marshal.
	out, err := json.Marshal(m)
	if err != nil {
		// Degrade gracefully: return a synthetic error envelope.
		return json.RawMessage(fmt.Sprintf(
			`{"truncated":true,"truncation_notice":"Tool output exceeded budget; re-serialisation failed. Full content at %s","output_file":%q}`,
			outPath, outPath,
		))
	}
	return out
}

// guardArray handles JSON arrays by recursively truncating strings nested
// inside elements (objects, sub-arrays, bare strings).
func guardArray(raw json.RawMessage) json.RawMessage {
	var arr []any
	if err := json.Unmarshal(raw, &arr); err != nil || len(arr) == 0 {
		return raw
	}

	// Walk the array to find the largest string for the output file.
	_, primaryValue := largestStringInSlice(arr)
	if primaryValue == "" {
		return raw
	}

	f, err := os.CreateTemp("", "hangrix-result-*.txt")
	if err != nil {
		return raw
	}
	if _, err := f.WriteString(primaryValue); err != nil {
		f.Close()
		return raw
	}
	f.Close()
	outPath := f.Name()

	// Truncate strings recursively inside the array.
	truncateStringsInSlice(arr, defaultResultBudgetBytes)

	// Wrap in an object envelope so we can stamp metadata.
	envelope := map[string]any{
		"value":       arr,
		"truncated":   true,
		"output_file": outPath,
		"truncation_notice": fmt.Sprintf(
			"Tool output exceeded the %d-byte result budget. The complete output has been written to %s — use the 'read' tool to retrieve it.",
			defaultResultBudgetBytes, outPath,
		),
	}
	out, err := json.Marshal(envelope)
	if err != nil {
		return json.RawMessage(fmt.Sprintf(
			`{"truncated":true,"truncation_notice":"Tool output exceeded budget; re-serialisation failed. Full content at %s","output_file":%q}`,
			outPath, outPath,
		))
	}
	return out
}

// guardString handles bare JSON string values that exceed the budget.
func guardString(raw json.RawMessage) json.RawMessage {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil || len(s) < defaultResultBudgetBytes {
		return raw
	}

	f, err := os.CreateTemp("", "hangrix-result-*.txt")
	if err != nil {
		return raw
	}
	if _, err := f.WriteString(s); err != nil {
		f.Close()
		return raw
	}
	f.Close()
	outPath := f.Name()

	// Wrap in an object envelope with a truncated preview.
	envelope := map[string]any{
		"value":       s[:min(4096, len(s))] + "\n\n[... result truncated; read output_file for full content ...]",
		"output_file": outPath,
		"truncated":   true,
		"truncation_notice": fmt.Sprintf(
			"Tool output exceeded the %d-byte result budget. The complete output has been written to %s — use the 'read' tool to retrieve it.",
			defaultResultBudgetBytes, outPath,
		),
	}
	out, err := json.Marshal(envelope)
	if err != nil {
		return json.RawMessage(fmt.Sprintf(
			`{"truncated":true,"truncation_notice":"Tool output exceeded budget; re-serialisation failed. Full content at %s","output_file":%q}`,
			outPath, outPath,
		))
	}
	return out
}

// existingOutputFile returns (path, true) when m already carries a
// non-empty "output_file" string key — meaning the tool manages its own
// output file and the guard should not create another one.
func existingOutputFile(m map[string]any) (string, bool) {
	v, ok := m["output_file"]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	if !ok || s == "" {
		return "", false
	}
	return s, true
}

// strLoc describes a mutable string somewhere in a JSON value tree
// (direct map value, array element, nested object field, etc.).
type strLoc struct {
	get func() string
	set func(string)
}

// collectStrLocs recursively walks a value tree and returns every
// mutable string location found.
func collectStrLocs(v any) []strLoc {
	switch t := v.(type) {
	case map[string]any:
		var locs []strLoc
		for k, val := range t {
			if _, ok := val.(string); ok {
				k := k
				locs = append(locs, strLoc{
					get: func() string { v, _ := t[k].(string); return v },
					set: func(nv string) { t[k] = nv },
				})
			} else {
				locs = append(locs, collectStrLocs(val)...)
			}
		}
		return locs
	case []any:
		var locs []strLoc
		for i, val := range t {
			if _, ok := val.(string); ok {
				i := i
				locs = append(locs, strLoc{
					get: func() string { v, _ := t[i].(string); return v },
					set: func(nv string) { t[i] = nv },
				})
			} else {
				locs = append(locs, collectStrLocs(val)...)
			}
		}
		return locs
	default:
		return nil
	}
}

// largestStringField returns the key and value of the longest string
// field anywhere in m (including nested arrays/objects).  Used to pick
// which content to write to the guard's temp file when the tool does not
// provide its own output_file.
func largestStringField(m map[string]any) (string, string) {
	locs := collectStrLocs(m)
	var bestKey, bestVal string
	var bestLen int
	for _, loc := range locs {
		s := loc.get()
		if len(s) > bestLen {
			bestVal = s
			bestLen = len(s)
		}
	}
	return bestKey, bestVal
}

// largestStringInSlice returns the longest string found anywhere inside
// arr (including nested maps/arrays).
func largestStringInSlice(arr []any) (string, string) {
	locs := collectStrLocs(arr)
	var bestKey, bestVal string
	var bestLen int
	for _, loc := range locs {
		s := loc.get()
		if len(s) > bestLen {
			bestVal = s
			bestLen = len(s)
		}
	}
	return bestKey, bestVal
}

// truncateStringFields iteratively shortens string values inside m
// (including nested arrays/objects), largest first, until json.Marshal(m)
// fits within budget bytes.  The floor shrinks progressively (256 → 128 →
// 64) to guarantee convergence even when many medium-length fields remain.
func truncateStringFields(m map[string]any, budget int) {
	locs := collectStrLocs(m)
	if len(locs) == 0 {
		return
	}
	truncateStrLocs(m, locs, budget)
}

// truncateStringsInSlice is the slice equivalent of truncateStringFields.
func truncateStringsInSlice(arr []any, budget int) {
	locs := collectStrLocs(arr)
	if len(locs) == 0 {
		return
	}
	truncateStrLocs(arr, locs, budget)
}

// truncateStrLocs contains the core truncation loop.  parent is the
// top-level value passed to json.Marshal for size checks.
func truncateStrLocs(parent any, locs []strLoc, budget int) {
	const marker = "\n\n[... result truncated; read output_file for full content ...]"

	// sortByLength re-orders locs so the longest string is first.
	sortByLength := func() {
		sort.Slice(locs, func(i, j int) bool {
			return len(locs[i].get()) > len(locs[j].get())
		})
	}

	// floor is the minimum chars we aim to leave before the marker.
	// Shrinks progressively when all strings are already at floor
	// and payload still exceeds budget.
	floor := 256

	for i := 0; i < 60; i++ { // generous safety cap
		raw, err := json.Marshal(parent)
		if err != nil || len(raw) <= budget {
			return
		}

		sortByLength()

		trimmed := false
		for _, loc := range locs {
			s := loc.get()
			if len(s) <= floor {
				continue
			}
			target := len(s) / 2
			if target < floor {
				target = floor
			}
			// Choose the shorter of: (target+marker) or just (target).
			// We want the marker for the LLM's benefit, but never at
			// the cost of making the string longer than it already was.
			withMarker := s[:target] + marker
			if len(withMarker) < len(s) {
				loc.set(withMarker)
			} else {
				loc.set(s[:target])
			}
			trimmed = true
		}

		if !trimmed {
			// All strings are at or below floor.  Lower it and retry.
			if floor > 32 {
				floor /= 2
				continue
			}
			// floor is at 32 — one last aggressive pass: truncate
			// every remaining string to floor.
			for _, loc := range locs {
				s := loc.get()
				if len(s) > floor {
					loc.set(s[:floor])
				}
			}
			return
		}
	}
}

// min returns the smaller of two ints.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// GuardResultForTest is the testable entry point.
func GuardResultForTest(raw json.RawMessage) json.RawMessage {
	return guardResult(raw)
}
