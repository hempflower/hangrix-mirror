package agentsconfig

import "strings"

// ParseMentions extracts every `@agent-<role-key>` token from a markdown
// comment body, skipping fenced code blocks, indented code blocks, inline
// code spans, and quote blocks. The returned slice contains each matched
// role key once, in first-occurrence order. Tokens that don't satisfy the
// role-key grammar (`^[a-z][a-z0-9-]{0,38}$`) are dropped silently —
// callers treat unknown tokens as "not a real mention" rather than a
// configuration error.
//
// The skip rules mirror docs/agent-config.md §"Mention 协议":
//
//   - Fenced code: lines between paired ``` (or ~~~) fences are ignored.
//     A bare opening fence with no closing fence treats the rest of the
//     body as code, matching common markdown renderer behaviour.
//   - Indented code: lines starting with at least four spaces (or a tab)
//     are ignored. This is the CommonMark indented-code-block rule; we
//     don't try to be perfect (it intersects with list items), only
//     conservative.
//   - Quote blocks: lines whose first non-space char is `>` are ignored.
//     This is a one-line check; nested or lazy-continuation quotes are
//     intentionally treated as ordinary text for now.
//   - Inline code spans: any backtick-delimited run on an otherwise
//     eligible line is excised before matching.
//
// We deliberately keep the parser simple: a small state machine over
// lines plus an inline-code stripper. Full CommonMark fidelity is not
// required — the cost of a missed mention is "the user re-pings", the
// cost of a false positive is "an agent wakes up unnecessarily" — both
// recoverable, neither catastrophic.
func ParseMentions(body string) []string {
	if body == "" {
		return nil
	}
	const prefix = "@agent-"
	seen := make(map[string]struct{})
	out := make([]string, 0, 4)

	var (
		inFence   bool
		fenceMark byte
	)
	for _, line := range strings.Split(body, "\n") {
		// Fenced-code block tracking. The opening fence may be ``` or
		// ~~~; we record which one so a stray ``` inside a ~~~ block
		// doesn't close it. Strip trailing whitespace then check the
		// first non-whitespace run for a fence.
		trimmed := strings.TrimLeft(line, " ")
		if isFenceLine(trimmed) {
			mark := trimmed[0]
			if !inFence {
				inFence = true
				fenceMark = mark
				continue
			}
			if mark == fenceMark {
				inFence = false
				fenceMark = 0
			}
			continue
		}
		if inFence {
			continue
		}
		// Indented code: 4+ leading spaces or a tab as the first char.
		if strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "    ") {
			continue
		}
		// Quote block.
		if strings.HasPrefix(strings.TrimLeft(line, " "), ">") {
			continue
		}
		// Excise inline code spans before scanning for @agent-.
		scrubbed := stripInlineCode(line)
		i := 0
		for {
			idx := strings.Index(scrubbed[i:], prefix)
			if idx < 0 {
				break
			}
			start := i + idx + len(prefix)
			// Boundary check before @ — must be start-of-string or a
			// non-role-key char so we don't capture `email@agent-...`
			// or `foo@agent-...` as a mention. Mention chips conform to
			// the same shape as inline-link plain text in markdown.
			before := i + idx
			if before > 0 {
				prev := scrubbed[before-1]
				if isRoleKeyChar(prev) || prev == '@' {
					i = before + 1
					continue
				}
			}
			end := start
			for end < len(scrubbed) && isRoleKeyChar(scrubbed[end]) {
				end++
			}
			key := scrubbed[start:end]
			if isValidRoleKey(key) {
				if _, dup := seen[key]; !dup {
					seen[key] = struct{}{}
					out = append(out, key)
				}
			}
			i = end
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// HasBacktickWrappedMention reports whether body contains an @agent-<key>
// token that is wrapped in an inline code span (backticks). This is a
// hard check used by the issue_comment tool to reject comments whose
// mentions would be invisible to the parser because they are inside
// backtick-delimited runs. The check follows the same fencing / indent /
// quote skip rules as ParseMentions so a mention inside a fenced code
// block that is also inside backticks does not trigger a false positive.
func HasBacktickWrappedMention(body string) bool {
	if body == "" {
		return false
	}
	var (
		inFence   bool
		fenceMark byte
	)
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimLeft(line, " ")
		if isFenceLine(trimmed) {
			mark := trimmed[0]
			if !inFence {
				inFence = true
				fenceMark = mark
				continue
			}
			if mark == fenceMark {
				inFence = false
				fenceMark = 0
			}
			continue
		}
		if inFence {
			continue
		}
		if strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "    ") {
			continue
		}
		if strings.HasPrefix(strings.TrimLeft(line, " "), ">") {
			continue
		}
		if hasBacktickWrappedMentionInLine(line) {
			return true
		}
	}
	return false
}

// hasBacktickWrappedMentionInLine scans a single line for inline code
// spans (backtick-delimited runs) whose content contains "@agent-".
// The backtick-matching logic mirrors stripInlineCode: runs of 1+
// backticks are matched against a closing run of equal length.
func hasBacktickWrappedMentionInLine(line string) bool {
	if !strings.ContainsRune(line, '`') {
		return false
	}
	i := 0
	for i < len(line) {
		if line[i] != '`' {
			i++
			continue
		}
		run := 1
		for i+run < len(line) && line[i+run] == '`' {
			run++
		}
		j := i + run
		closeStart := -1
		for j < len(line) {
			if line[j] != '`' {
				j++
				continue
			}
			cr := 1
			for j+cr < len(line) && line[j+cr] == '`' {
				cr++
			}
			if cr == run {
				closeStart = j
				break
			}
			j += cr
		}
		if closeStart < 0 {
			i += run
			continue
		}
		content := line[i+run : closeStart]
		if strings.Contains(content, "@agent-") {
			return true
		}
		i = closeStart + run
	}
	return false
}

func isFenceLine(line string) bool {
	if len(line) < 3 {
		return false
	}
	mark := line[0]
	if mark != '`' && mark != '~' {
		return false
	}
	for i := 0; i < 3; i++ {
		if line[i] != mark {
			return false
		}
	}
	// CommonMark requires the rest of the line to contain no further
	// occurrences of the fence char (apart from optional info-string
	// after a space). We don't enforce that strictly — the worst case
	// is treating a stray ```bash on a content line as a fence, which
	// at worst masks one mention.
	return true
}

func isRoleKeyChar(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z':
		return true
	case b >= '0' && b <= '9':
		return true
	case b == '-':
		return true
	}
	return false
}

// stripInlineCode removes everything between matched single-backtick runs.
// Markdown inline code uses runs of 1-or-more backticks as delimiters; the
// shortest match must agree on count. We handle 1 and multi-tick runs by
// matching identical-length runs. Unbalanced runs leave the text alone.
func stripInlineCode(line string) string {
	if !strings.ContainsRune(line, '`') {
		return line
	}
	var b strings.Builder
	b.Grow(len(line))
	i := 0
	for i < len(line) {
		if line[i] != '`' {
			b.WriteByte(line[i])
			i++
			continue
		}
		// Count opening run.
		run := 1
		for i+run < len(line) && line[i+run] == '`' {
			run++
		}
		// Find a matching closing run of the same length.
		j := i + run
		closeStart := -1
		for j < len(line) {
			if line[j] != '`' {
				j++
				continue
			}
			cr := 1
			for j+cr < len(line) && line[j+cr] == '`' {
				cr++
			}
			if cr == run {
				closeStart = j
				break
			}
			j += cr
		}
		if closeStart < 0 {
			// No match — keep the run literal, scan past it.
			for k := 0; k < run; k++ {
				b.WriteByte('`')
			}
			i += run
			continue
		}
		// Skip from opening tick through closing tick.
		i = closeStart + run
	}
	return b.String()
}
