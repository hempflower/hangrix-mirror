# 贡献分支与评审系统设计

[← ROADMAP](../ROADMAP.md) · [agents.yml schema](./agent-config.md) · [Agent Identity](./agent-identity.md)

把当前的「文本补丁(`git format-patch` → mbox 文本 → `git am`)」贡献模型,换成 **基于贡献分支的 Merge-Request 模型**:每个 agent 把工作推到属于自己的分支(ref),这条分支就是一个独立 PR;评审与投票挂在**分支**上而不是 issue 上;贡献分支经评审后由**服务端**合入 issue 分支;所有贡献分支合入后,maintainer 再把 issue 分支合入 base。

> 本文取代 `docs/agent-config.md` 与 `docs/agent-identity.md` 里描述的 patch-first 文本补丁流程。迁移见文末「§与现状的关系」。

---

## 为什么要改

现状(`modules/agent_api` 的 `issue_patch_*` 工具 + `gitCaller.hasWriteScope`)有几处结构性问题:

1. **审批与落地不绑定。** 审批针对存储的补丁文本;真正落到 issue 分支的是特权 agent 在自己 workspace 里 `git am` 后 `git push` 的任意内容,`commit_sha` 由 agent 自报、服务端不校验。「补丁 N 应用为 commit X」之间没有结构性证据。
2. **`git am` 文本往返脆弱。** 二进制(需 `--binary`)、文件 mode、rename、合并提交、空白差异都易失败,且失败发生在人已审批之后。
3. **`base_head_sha` 记录却不 gate。** 形同装样子的乐观锁;issue 分支前移后旧补丁必然冲突,却仍显示可应用。
4. **stats 靠手写解析。** `parsePatchStats` 按行首 `+`/`-` 计数,commit message、rename、二进制全算错。
5. **粒度错位。** 一个 issue 上多个角色(server / web / runtime)并行贡献时,投票挂在 issue 上,「approve」批的是哪一份是含糊的。

新模型用 git 自身的分支/合并机制消掉这些问题,并复用平台已有的 `DiffMergeBase` / `CheckAutoMerge` / `MergeBranch` / `issue_review_vote` / `ComputeReviewStatus`。

---

## 设计原则

1. **贡献即分支。** 一个贡献 = 一条 agent 专属 ref 上的 commit,不是文本 blob。提交、修订、审查、合并全部走 git 对象,可证、可 diff、可 checkout 跑测试。
2. **agent 永不 push 受保护分支。** 贡献者只能 push 自己的命名空间 ref;issue 分支与 base 的推进一律由**服务端**完成。push 权限收敛成 per-ref ACL,而非 repo 级写权限。
3. **投票挂在贡献分支上,绑定 commit SHA。** 评审语义是「这份贡献能否进 issue 分支」;改版(新 push)使旧审批自动作废。
4. **两级闸门。** 第一级:贡献分支 → issue 分支(由该分支的投票 + 可合并性驱动)。第二级:issue 分支 → base(`issue_merge`,maintainer 拍板)。
5. **合入顺序由 agent 决定。** 系统不做自动 merge-queue;只在 apply 时用 `CheckAutoMerge` 兜底暴露冲突。
6. **最大化复用。** 评审投票、闸门计算、diff、可合并性检查、行级评论、分支保护都复用现有件。

---

## 核心模型

```
issue #N  (base = main, branch = issue-N)
  ├── 贡献分支  issue-N/server     ← server agent push
  │     reviews(挂这条分支) + mergeable + 第一级闸门
  ├── 贡献分支  issue-N/web        ← web agent push
  └── 贡献分支  issue-N/runtime    ← runtime agent push
        ↑ 经评审 → 服务端 merge 进 issue-N(server 算 commit_sha)

所有贡献分支合入 issue-N 后:
  issue-N  ──issue_merge(第二级,maintainer)──▶ main
```

每条贡献分支是独立 PR;issue 是这些 PR 的聚合视图。

---

## Ref 命名与 per-ref ACL

- 命名空间:`refs/heads/issue-<N>/<role-key>`(每个角色在一个 issue 上一条主贡献分支;需要并行实验可扩展为 `issue-<N>/<role>/<slug>`)。
- **per-ref ACL** 在 receive-pack 内强制(`modules/repo/handler` 的 `gitReceivePack` 已用 `parseReceivePackRefs` 解析出 ref 更新命令):
  - session caller 只能更新**自己**命名空间下的 ref(`issue-<sess.IssueNumber>/<sess.RoleKey>...`)。
  - 对 issue 分支、base、其它角色的 ref 的更新 → 拒绝(违反 ACL,返回 4xx/pre-receive reject)。
  - 人类 caller(cookie/pat/password)按现有 `canAccessRepo` + 分支保护规则。
- 这取代当前 `hasWriteScope` 里「session 凭 `issue_patch_apply` 能力放行 repo 级写」的临时门禁:**没有任何 agent 需要对受保护分支的 push 权限**,issue 分支推进改为服务端操作。

> 现有的 `SyncProtectionRules` + pre-receive hook 继续生效(保护 base / issue 分支);per-ref ACL 是在它之上、面向 session 的命名空间限制。

---

## 数据模型(替代 `patch_submissions` / `issue_patch_files`)

```
contributions
  id            BIGSERIAL
  repo_id       BIGINT
  issue_id      BIGINT
  session_id    BIGINT        -- 产生它的 agent session
  agent_role    TEXT          -- 角色 key(= 命名空间)
  ref_name      TEXT          -- refs/heads/issue-N/<role>
  head_sha      TEXT          -- 当前贡献 head
  base_sha      TEXT          -- 分出点(merge-base 用)
  title         TEXT
  description   TEXT
  status        TEXT          -- 见状态机
  mergeable     BOOL          -- 对当前 issue head 的缓存结果(CheckAutoMerge)
  changed_paths TEXT[]        -- 真实 diff 算出(喂 path 过滤)
  files/additions/deletions   -- 真实 diff 算出
  merged_commit_sha TEXT      -- 服务端合入后,server 计算
  merged_at     TIMESTAMPTZ
  created_at / updated_at
```

投票:复用 `issue_events` 的 `review_vote`,但 payload 增加 `contribution_id` 与 `reviewed_sha`(取代现状只记 issue 级 `HeadSHA`)。`ComputeReviewStatus` 改成对**单条 contribution 的事件 + 当前 head_sha** 计算,只采纳 `reviewed_sha == head_sha` 的票,其余视为 stale。

---

## 生命周期 / 状态机(每条贡献分支)

```
        push 到自己的 ref
            │
            ▼
         open ──request_changes──▶ changes_requested
          │  ▲                          │
          │  └────── push 新 commit ◀───┘  (SHA 变,旧 approve 作废,回到审查)
          │
   第一级闸门通过 + contribution_apply
          │
          ▼
        merged        (服务端已合入 issue 分支)

  open / changes_requested ──owner 关闭──▶ closed
```

- 投票绑定 `head_sha`;新 push 改变 `head_sha` → 历史 approve 自动 stale(GitHub 式 dismissal)→ reviewer 重新被唤醒。
- `merged` 是终态;`closed` 由 owner(同 role)主动放弃。

---

## 评审

- **唤醒**:贡献分支 push 后,服务端从真实 diff 算 `changed_paths`,触发评审事件(复用 `commit.pushed` 语义,target 为贡献分支),按 agents.yml 各 reviewer 角色的 `paths` / `paths_ignore` 唤醒——比现状更准,因为 paths 来自真实 git diff 而非手写解析。
- **看什么**:右侧展示 `DiffMergeBase(base, 贡献分支)` 的真实 diff;reviewer 可只读 fetch 这条 ref,真正 checkout 跑测试 / 复现。
- **投票**:`issue_review_vote`(approve / request_changes / abstain)**不变**,但落到 `contribution_id` + `reviewed_sha`。
- **行级评论**:`issue_comment` 带 `file_path` + `line`,锚点落在贡献 commit 的真实树上。
- **禁止自审**:贡献者(同 `agent_role` / `session`)不能 approve 自己的分支。
- **语义沟通**:reviewer 投的是「这份贡献能否进 **issue 分支**」(不是 issue→base)。需在 reviewer prompt 与本文档显式说明,避免 agent 沿用旧语义。【决策 4】

---

## 闸门

**第一级 —— 贡献分支 → issue 分支:**

```
可应用 = ComputeReviewStatus(contribution).approved  (审批策略复用现状)
        ∧ CheckAutoMerge(贡献分支, 当前 issue head) 可合并
        ∧ 最新被审批的 reviewed_sha == contribution.head_sha
```

**第二级 —— issue 分支 → base:** 现有 `issue_merge`,maintainer 拍板(可再走一道 review)。

---

## 合入(服务端 apply)

新工具 `contribution_apply`(maintainer 能力门禁),无 agent git push:

1. 校验第一级闸门。
2. 服务端在 bare 仓库 / 受控 worktree 用 `MergeBranch`(已在 `issue_merge` 用过)把贡献分支合入 issue 分支——fast-forward 优先,否则 merge commit。
3. **`commit_sha` 由服务端计算**,写回 `contributions.merged_commit_sha`,更新 issue head,落时间线事件。
4. 失败(冲突)→ 标记该贡献 not-mergeable,要求贡献者 rebase 后重推(产生新 SHA,重审)。

**合入顺序由 agent 决定**【决策 2】:系统不排序;maintainer / 贡献者自行决定先合哪条。先合的进去后,其余贡献对新 issue head 若冲突 → `mergeable=false` → 自动 stale → 由 agent 处理。

---

## issue 分支与 base 冲突时的处理

当 `issue_mergeable` 返回 `mode="conflicted"` 时，issue 分支无法直接合入 base。此时 **agent 不应尝试直接 push `issue/<n>` 分支**（`issue/<n>` 为服务端管理分支，agent 无 push 权限）。正确的修复路径是：

1. 基于最新的 `origin/issue/<n>` 在本地解决与 `base_branch` 的冲突。
2. 将结果 push 到一条**新的 contribution 分支**：`refs/heads/issue-<n>/<role>/<slug>`。
3. 通过 contribution 评审 / `contribution_apply` 流程将该分支合入 issue 分支。
4. 合入后重新运行 `issue_mergeable` / `issue_merge`。

这一流程与 per-ref ACL（agent 只能 push 自己命名空间的 ref）和服务端管理 issue 分支的约束保持一致。

---

## issue「完成」的定义【决策 1】

**所有贡献分支都已审批并合入 issue 分支** ⇒ issue 视为内容就绪;随后 maintainer 走第二级 `issue_merge` 把 issue 分支合入 base 做最终拍板。issue 级不再单独承载投票,只做贡献分支的 rollup。

---

## UI【决策 3】

issue 详情下新增「贡献 / 分支」视图,**左右布局**:

```
┌───────────────────────┬─────────────────────────────────────────┐
│ 分支列表(左)          │ 选中分支(右)                              │
│                       │  ┌ Diff │ Reviews │ Comments │ Checks ┐   │
│ ● issue-N/server      │  │                                    │   │
│   ✔ approved · ✔ merg │  │  (Diff: 真实 unified diff)           │   │
│ ● issue-N/web         │  │  (Reviews: 各 reviewer 投票 + SHA)   │   │
│   ⟳ changes_requested │  │  (Comments: 行级 / 顶层评论)         │   │
│ ● issue-N/runtime     │  │  (Checks: CI,M8 接入)               │   │
│   ⧗ open · ✖ conflict │  └────────────────────────────────────┘   │
└───────────────────────┴─────────────────────────────────────────┘
```

- 左:分支列表,每条带 角色 / status / mergeable / head SHA(短)。
- 右:选中分支的改动 + 评审,tab 分页 `[Diff] [Reviews] [Comments] [Checks]`。

---

### Push 响应中的 contribution 提示

自 contribution-branch 模型上线起，`git push` 成功后，如果本次 push 的 ref 属于贡献分支命名空间（`refs/heads/issue-<N>/<role>/<slug>`），服务端会在 push 响应的 sideband 流中注入 `remote:` 提示行，包含：

- 机器可提取的 `contribution_id`（例如 `contribution_id: 42`）
- 人/agent 可读的下一步操作提示（可直接用 `contribution_set_meta` 设置标题描述；用 `contribution_read` 查看 metadata 和 review 状态，再 fetch 贡献分支到本地自行查看 diff；无需再调 `contribution_list` 来获取 ID）

这意味着 agent 不需要在 push 之后额外调用 `contribution_list` 工具来发现 contribution ID —— push 终端的 `remote:` 输出就是第一手来源。`contribution_list` 退化为「查看某个 issue 下所有 contribution」的补充查询工具。

> 仅 contribution 分支 push 会触发此提示。普通分支 push、tag push、被 ACL 拒绝的 push 等不会产生 contribution 提示。当 contribution 同步未产出记录时（例如 push 成功但 DB 写入失败），push 仍保持原有成功/失败语义，不会因为提示缺失而额外失败，也不会返回错误 ID。

---

## 工具变化(`modules/agent_api`)

| 现状工具 | 新模型 |
| --- | --- |
| `issue_patch_submit` | **移除** —— push 到自己命名空间 ref 即隐式创建/更新 contribution;title/description 由 `contribution_set_meta`(或贡献分支上的约定 commit / PR-body 评论)提供 |
| `issue_patch_list` | `contribution_list` —— 列出 issue 下各贡献,带 status / mergeable / head_sha / role。默认仅返回非终态贡献(pending/approved/rejected);传 `include_closed=true` 可包含已关闭贡献,传 `include_merged=true` 可包含已合并贡献 |
| `issue_patch_read` | `contribution_read` —— 返回 contribution 元数据 + review 状态 + `checkout_hint` 提示（引导 agent fetch 贡献分支到本地自行查看 diff，不再内联 diff） |
| `issue_patch_apply` | `contribution_apply` —— 服务端合入(第一级闸门),不再发 trigger 等回调 |
| `issue_patch_apply_result` | **移除** —— 服务端自己 apply,无需 agent 回报 commit_sha |
| `issue_patch_reject` | `contribution_request_changes` / `contribution_close` |
| `issue_patch_withdraw` | `contribution_close`(owner) |
| `issue_review_vote` | **不变**,target 改为 contribution |

---

## 复用 vs 新增

| 直接复用 | 需新增 / 改 |
| --- | --- |
| `issue_review_vote`、`ComputeReviewStatus`(改成 per-contribution + 按 SHA 作废) | receive-pack 的 **per-ref ACL** |
| `DiffMergeBase`、`CheckAutoMerge`、`MergeBranch`(服务端 apply) | `contributions` 实体 + 状态机(替代 `patch_submissions`/`issue_patch_files`) |
| agents.yml `paths`/`paths_ignore` 路径过滤、reviewer 触发器 | `review_vote` 增加 `contribution_id` + `reviewed_sha` |
| `issue_comment` 行级评论(`file_path`+`line`)、分支保护 / pre-receive | 服务端 `contribution_apply`(算 commit_sha,无 agent push) |
| `issue_merge`(第二级闸门) | 工具收敛 `issue_patch_*` → `contribution_*` |
| | 新 UI「贡献/分支」视图 + reviewer prompt 语义更新 |

---

## 与现状的关系 / 迁移

- **取代 patch-first 文本补丁模型。** `issue_patch_*` 工具与 `patch_submissions` / `issue_patch_files` 表退役;`gitCaller.hasWriteScope` 里基于 `issue_patch_apply` 的 session 写门禁,被 per-ref ACL(只能写自己命名空间)取代——因为 issue 分支推进改为服务端操作,agent 不再 push 受保护分支。
- **迁移可分阶段、独立上线:**
  1. **per-ref ACL**:在 receive-pack 加命名空间校验,放开 session push 到自己的 `issue-N/<role>` ref(其余拒绝)。
  2. **contribution 实体 + 服务端 apply**:push 隐式建/更新 contribution;`contribution_apply` 服务端合入(可与旧 patch 工具并存一段时间)。
  3. **评审重指向**:`review_vote` 落到 contribution,`ComputeReviewStatus` 改 per-contribution + SHA 作废。
  4. **UI** 左右布局视图。
  5. 移除 `issue_patch_*` 与相关表。

---

## 范围外(v1 不做)

- 自动 merge-queue / 自动 rebase(顺序由 agent 决定,仅 mergeable 兜底)。
- 跨贡献分支的依赖声明。
- CI checks 接入(占位 tab,等 M8)。
- 一个角色在同一 issue 上的多并行实验分支(命名预留 `/<slug>`,工具/UI 暂按单分支)。

---

## 决策记录

1. **issue「完成」= 所有贡献分支已审批并合入**,再由 maintainer 走 issue→base。
2. **合入顺序由 agent 决定**,系统不自动排序,仅 apply 时 mergeability 兜底。
3. **UI 左右布局**:左分支列表,右改动+评审(tab)。
4. **投票语义 = 该贡献能否进 issue 分支**,需在 reviewer prompt / 文档显式说明。
