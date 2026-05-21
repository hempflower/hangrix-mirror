package tools

import (
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
//  1. The primary content (largest string value in the result map) is
//     written in full to a temp file.
//  2. Large string fields are trimmed until the remaining payload fits.
//  3. The modified result carries "truncated":true, a human-readable
//     "truncation_notice" and the "output_file" path so the LLM knows
//     where to find the complete data.
//
// When the result map already carries an "output_file" key whose value is
// a non-empty string (e.g. bash background/promoted results), the guard
// reuses that path — the per-job log already holds the full content and we
// do not create a second file.
//
// When the raw JSON is not a JSON object, the guard returns the original
// bytes unchanged — scalar/null/array results are inherently bounded.
func guardResult(raw json.RawMessage) json.RawMessage {
	if len(raw) <= defaultResultBudgetBytes {
		return raw
	}

	// We can only guard JSON objects (maps).  Scalars, null, and arrays
	// are returned as-is — their size is inherently bounded by their
	// data model (e.g. a bash foreground result is always an object).
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil || len(m) == 0 {
		return raw
	}

	// 1. Determine the output file path.
	outPath, fromValue := existingOutputFile(m)
	if !fromValue {
		// Write the primary content (largest string field) to a temp file
		// so the LLM can read the full text without JSON noise.
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

// largestStringField returns the key and value of the longest string
// field in m.  Used to pick which content to write to the guard's temp
// file when the tool does not provide its own output_file.
func largestStringField(m map[string]any) (string, string) {
	var bestKey, bestVal string
	var bestLen int
	for k, v := range m {
		if s, ok := v.(string); ok && len(s) > bestLen {
			bestKey = k
			bestVal = s
			bestLen = len(s)
		}
	}
	return bestKey, bestVal
}

// truncateStringFields iteratively shortens string values in m, largest
// first, until json.Marshal(m) fits within budget bytes.  Each halving
// step appends a short truncation marker so the LLM can still see the
// leading portion of the content.
func truncateStringFields(m map[string]any, budget int) {
	// Collect all string fields for sorting.
	type field struct {
		key string
	}
	var fields []field
	for k, v := range m {
		if _, ok := v.(string); ok {
			fields = append(fields, field{key: k})
		}
	}
	if len(fields) == 0 {
		return
	}

	// sortByLength is a helper used inside the loop.
	sortByLength := func() {
		sort.Slice(fields, func(i, j int) bool {
			si, _ := m[fields[i].key].(string)
			sj, _ := m[fields[j].key].(string)
			return len(si) > len(sj)
		})
	}

	// The truncation marker appended to each trimmed field.
	const marker = "\n\n[... result truncated; read output_file for full content ...]"
	markerLen := len(marker)

	// Iteratively halve the longest string field until the payload fits.
	for i := 0; i < 20; i++ { // safety cap — should converge in a few iterations
		raw, err := json.Marshal(m)
		if err != nil || len(raw) <= budget {
			return
		}

		sortByLength()
		trimmed := false
		for _, f := range fields {
			s := m[f.key].(string)
			if len(s) <= 256 {
				continue
			}
			// Halve the field, ensuring we leave at least 256 chars of
			// visible content plus the marker.
			target := len(s) / 2
			if target < 256 {
				target = 256
			}
			if target+markerLen < len(s) {
				m[f.key] = s[:target] + marker
			} else {
				// Field is already small enough — set to minimal.
				m[f.key] = s[:256] + marker
			}
			trimmed = true
			break // one field per iteration to avoid aggressive over-truncation
		}
		if !trimmed {
			return // nothing left to trim
		}
	}
}

// GuardResultForTest is the testable entry point.
func GuardResultForTest(raw json.RawMessage) json.RawMessage {
	return guardResult(raw)
}
