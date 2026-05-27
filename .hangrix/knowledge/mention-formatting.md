# Mention formatting gotcha

`@agent-<role-key>` mentions **MUST** appear as plain prose in issue comments — never wrap them in any kind of code formatting. The mention parser intentionally ignores tokens that appear inside:

- Fenced code blocks (` ``` ` / `~~~`)
- Indented code blocks (4+ leading spaces or a tab)
- Inline code spans (`` `@agent-foo` ``)
- Blockquotes (`> @agent-foo`)

A mention written inside backticks, e.g. `` `@agent-web` ``, is rendered as text only and **will not wake** the target role. If you need to *talk about* the mention syntax without triggering it, code-wrap it on purpose; otherwise leave `@agent-<role-key>` as bare prose.

This is enforced by the platform's comment parser, not by a prompt. The baseline runtime prompt also includes this rule under "Don't wrap mentions in code formatting."

## Cross-issue mentions

`@agent-<role>` mentions are **issue-scoped** — they only wake agents on the same issue where the comment is posted. A mention in a parent issue will never reach a sub-issue's agent (or vice versa).

To nudge an agent on a directly-related issue (parent or child), use the `issue_comment_cross` tool with the target issue number. The `@agent-<role>` syntax inside `issue_comment_cross`'s body **does** wake roles on the target issue — that's the only supported cross-issue communication channel.
