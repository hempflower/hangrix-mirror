# Mention formatting gotcha

`@agent-<role-key>` mentions **MUST** appear as plain prose in issue comments — never wrap them in any kind of code formatting. The mention parser intentionally ignores tokens that appear inside:

- Fenced code blocks (` ``` ` / `~~~`)
- Indented code blocks (4+ leading spaces or a tab)
- Inline code spans (`` `@agent-foo` ``)
- Blockquotes (`> @agent-foo`)

A mention written inside backticks, e.g. `` `@agent-web` ``, is rendered as text only and **will not wake** the target role. If you need to *talk about* the mention syntax without triggering it, code-wrap it on purpose; otherwise leave `@agent-<role-key>` as bare prose.

This is enforced by the platform's comment parser, not by a prompt. The baseline runtime prompt also includes this rule under "Don't wrap mentions in code formatting."
