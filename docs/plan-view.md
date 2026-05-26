# 长期计划视图设计

[← ROADMAP](../ROADMAP.md) · [依赖边与计划制定](./plan-dependencies.md) · [推进引擎](./plan-engine.md) · [agents.yml schema](./agent-config.md) · [贡献分支](./contribution-branches.md)

目标：给 host 仓库一套**制定 + 推进长期计划**的能力，落在 M9「围绕 AI 重塑 issue 体验」里。当前 agent 是纯反应式的——只在 `issue.comment` / `commit.pushed` / `issue.closed` 等事件上被 spawner 唤醒，且 session 绑死单个 issue 生命周期；系统里**没有任何东西会主动看一眼计划、挑出下一个该做的事、派活**。本文设计的是这套能力中**用户能看见、能据此操盘的那一层——计划视图**，主布局为「大纲树 + 就绪条」。

> 本文只覆盖**视图层**（下文「三层分解」中的第 3 层）。依赖边（第 1 层）与推进引擎（第 2 层）作为相邻工作在本文中只给接口钩子，不展开。

---

## 定位：计划是 issue 的一个切面

ROADMAP 设计原则 1 写死了「任何代码变更都必须有一个 issue 承载，没有游离实体」，且明确「不做独立的 PR / review / discussion 实体，它们是 issue 的不同切面」。**长期计划沿用同一逻辑**：

- 一个 epic / 里程碑**就是一个 issue**，它的步骤就是它的**子 issue**（`issues.parent_id` / `parent_number` 已存在，见 [00002_add_issue_parent.sql](../apps/hangrix/internal/modules/issue/infra/migrations/00002_add_issue_parent.sql)）。
- **不新建** `plans` / `goals` / `milestones` 表去和 issue 打架——那等于违背原则 1。issue 已有的评论、agent session、审计、merge 状态机全部免费复用。

当前缺口分两块，对应两个不同问题：

1. **制定**：issue 树只表达**包含**关系，不表达**顺序 / 依赖**——没有「A 做完才能做 B」这种边。长期计划的本质是 DAG，而现状只有 tree。
2. **推进**：没有主动驱动器；计划即使写出来，也只能靠人一个个去开 issue / 戳 agent。

完整能力按从小到大切成三层，可独立交付又互相点亮：

| 层 | 内容 | 状态 |
| --- | --- | --- |
| 第 1 层 · 制定 | `issue_dependencies` 依赖边表 + planner role 拆解目标 | 原型 → [依赖边与计划制定](./plan-dependencies.md) |
| 第 2 层 · 推进 | `issue.child_closed` 触发器 + 就绪前沿派活 + cron 兜底 + 预算/并发/pause 闸 | 原型 → [推进引擎](./plan-engine.md) |
| **第 3 层 · 可见** | **本文** —— 计划视图：大纲树 + 进度卷起 + 就绪条 | 本设计 |

视图先行是有意为之：它不阻塞在依赖系统上（基于今天的数据即可渲染，见 §就绪/阻塞与优雅降级），同时它是另外两层的**验收界面**——就绪/阻塞芯片是第 1 层的可见回报，header 的暂停钮是第 2 层「可见 / 可停」的落点。

---

## 设计原则

1. **不新增实体、不新增迁移（phase 0）。** epic = 有子 issue 的 issue；进度全部从既有 `state` / `review_status` / `todo_summary` 推导。依赖边是第 1 层的事，本文只在 DTO 里留位。
2. **优雅降级。** 视图在「今天的数据」上就完整可用（树 + 状态徽章 + 每节点 todo 进度）；依赖边落地后**自动点亮**就绪前沿与 `⛔ blocked by` 标记，无需改视图骨架。
3. **服务端预聚合，杜绝 N+1。** 子树 + 每节点进度由一个新只读端点一把返回。详情页是 5s 轮询，若让 SPA 对每个子 issue 单独拉 `review_status` / `todos` 会又慢又抖。
4. **复用既有视觉与组件，不引新形态。** 沿用现有徽章配色（open=emerald / merged=violet / closed=slate / in-progress=amber）、lucide 图标集、`ActorBadge`、shadcn-vue 的 `Tabs` / `Card` / `Badge`。**不引图布局库**（这正是没选 DAG 主布局的原因——守 tech-stack「克制 + 收敛」）。
5. **不开 UI-only fast path。** 计算 `review_status` / `todo_summary` 的逻辑抽到 issue 模块 `domain/` 共享，detail 与 plan 两个端点都调它（工程基线明确禁止绕过 domain 的 agent-only / UI-only 路径）。

---

## 与现有系统的边界

### 复用哪些现有能力

- **层级数据**：`issues.parent_id` / `parent_number` 与 `GET .../issues/{number}/children` 已存在（[handler.go](../apps/hangrix/internal/modules/issue/handler/handler.go)）。
- **节点进度来源**：`state`、`review_status`（服务端已算的评审裁决）、`todo_summary`（服务端从 body 解析的勾选项）——这三者今天只内嵌在「详情响应」里，本文把它们提到子树聚合里复用。
- **前端框架**：Nuxt 4 SPA + shadcn-vue + Tailwind v4；issue 详情页 [issues/[number].vue](../apps/web/app/pages/[owner]/[name]/issues/[number].vue) 已是 tab 结构（conversation / commits / diff / contributions / agents）。
- **刷新与深链**：复用详情页的 5s 轮询 + 页面隐藏暂停，以及 `?tab=` query 深链约定。

### 明确不新增 / 不依赖的东西

- **不新增表、不写迁移**（phase 0）。
- **不依赖第 1 层依赖边**——没有它时就绪条退化成启发式（见下），视图照常工作。
- **不引图可视化库**（vue-flow 等）；DAG 形态留作未来增强，不进 phase 0。
- **不做拖拽改状态**——状态来自 issue 真实状态机，不在视图里旁路修改。

---

## 视图骨架

挂在 epic issue 详情页的新 tab `plan`，仅当该 issue 有子 issue 时出现。

```
┌─ Plan ─────────────────────────────────────────────┐
│ M8 CI/Workflow 子系统                                │  ← header
│ ▓▓▓▓▓░░░ 5/8 merged   ·  ▶ 就绪: #203 #207          │
│ [□ 隐藏已完成]                    (将来这里挂 ⏸暂停)  │
├──────────────────────────────────────────────────────┤
│ ├ #201 schema            server  ● merged           │  ← tree body
│ ├ #202 串行执行引擎       runtime ◐ 评审中 1/2✓       │
│ │  └ #210 step driver    runtime ● merged           │
│ ├ #203 cron 触发器        server  ○ open   ▶就绪      │
│ └ #204 workflow UI        web     ○ open   ⛔#202     │
└──────────────────────────────────────────────────────┘
```

三段：

- **header**：epic 标题 + 进度条（`5/8 merged`，悬停展开 merged/评审中/在做/open/closed 明细）+ 就绪条（就绪前沿的可点 issue 芯片）。右侧预留第 2 层的「自动推进：开 / 暂停 + 预算/已用 + ⏸」位置，phase 0 不实现。
- **工具条**：`隐藏已完成`（折叠 merged 子树降噪）等过滤开关。
- **大纲树**：见下。

---

## 节点行结构

从左到右：

```
[折叠角标(有孩子时)] [状态字形] [#号链接] [标题] [role/ActorBadge] [state徽章] [review/todo 迷你进度] [就绪/blocked 芯片]
```

- 缩进按 depth 加 `padding-left`；连接线用 CSS 边框模拟 `├ └`，不引库。
- **非叶子节点**（子 epic）行不显单一状态，而是显**它自己子树的迷你进度条**（递归卷起）。
- role 归属用 `ActorBadge`（[ActorBadge.vue](../apps/web/app/components/ActorBadge.vue)）显示，与全站归属展示一致。
- 点击 `#号` / 行 → 跳该 issue 详情；折叠角标 → 展开/收起子树。

---

## 节点状态推导

按优先级取第一个命中，配色字形全部沿用既有方案：

| 显示态 | 判定 | 字形 / 色 |
| --- | --- | --- |
| 已完成 | `state = merged` | `GitMerge` · violet |
| 已放弃 | `state = closed`（未合并） | `Lock` · slate，**置灰 + 删除线** |
| 评审中 | open 且 `review_status.verdict = pending` 且有票 / 有 contribution | `CircleDot` · amber + `1/2✓` |
| 在做 | open 且有 running session 或 `todo.in_progress > 0` | `CircleDot` · amber |
| 待开始 | open 且未启动 | `Circle` · slate |

附加芯片（与上面叠加，依赖第 1 层）：

| 芯片 | 判定 |
| --- | --- |
| `▶ 就绪` | 叶子 + 未启动 + **所有依赖已 merged** | emerald |
| `⛔ #X` | 任一依赖未 merged | red |

---

## 进度卷起语义【决策 1】

数字含义必须写死，否则「5/8」含糊：

- **分母** = 子树叶子数 − 已关闭未合并的叶子；**分子** = 已 merged 叶子。→ `5/8 merged`。
- 已放弃（closed 未合并）的叶子**不进分母**，旁列置灰展示——避免「永远到不了 100%」。
- 子 epic 的进度 = 它自己子树的同口径卷起，逐层向上合并。

这套口径是**可调参数**，本文先定此默认；若后续想按 todo 粒度加权，改聚合函数即可，DTO 不变。

---

## 就绪 / 阻塞与优雅降级

`就绪 / 阻塞` 依赖第 1 层的 `issue_dependencies` 边。视图对此**优雅降级**：

- **依赖边落地前**：聚合端点返回 `depends_on = []`、`blocked = false`；就绪条退化成「所有未启动的 open 叶子」的**弱启发式**，header 上标注「（启发式）」。
- **依赖边落地后**：`depends_on` / `blocked` / `ready` 变精确，就绪条变真正的 ready frontier，`⛔ blocked by #X` 点亮。

两种情况下视图骨架、DTO 形状、组件都不变——只是字段从「空/启发」变「精确」。

---

## 数据契约：`GET .../issues/{number}/plan`

一个新**只读**端点，一次返回整棵子树 + 服务端预聚合：

```
GET /api/repos/{owner}/{name}/issues/{number}/plan

PlanNode {
  number, title, state, actor, agent_role,
  review_status?,            // 复用详情里同款裁决
  todo_summary?,             // { total, done, in_progress }
  depends_on: number[],      // [] 直到第 1 层落地
  blocked: bool,             // 任一依赖未 merged；落地前恒 false
  ready:   bool,             // 叶子 + 未启动 + 依赖全 merged
  children: PlanNode[]
}

PlanResp {
  root:   PlanNode,
  rollup: { total_leaves, merged, in_review, in_progress, open, closed },
  ready:  number[]           // 就绪前沿（issue 号）
}
```

**后端落点**（守模块化单体约定，见 [AGENTS.md](../AGENTS.md)）：

- 路由加在 [issue/handler/handler.go](../apps/hangrix/internal/modules/issue/handler/handler.go)，紧挨现有 `h.children`，新增 `h.plan`。
- issue `domain/` 加 `Plan(ctx, repoID, number) (PlanTree, error)`。
- 子树用**递归 CTE**（`WITH RECURSIVE` over `issues.parent_id`）在 `infra/queries.sql` 一把捞，`sqlc generate`。
- 把现内嵌在详情响应里算 `review_status` / `todo_summary` 的逻辑**抽到 domain 共享**，detail 与 plan 同源（原则 5）。
- **phase 0 零迁移**；第 1 层落地后给本端点补 `depends_on` 计算即可，DTO 已留位。

---

## 前端落点

- 新组件 `apps/web/app/components/issue/PlanView.vue`。
- [issues/[number].vue](../apps/web/app/pages/[owner]/[name]/issues/[number].vue) 的 tab 联合类型加 `'plan'`；`TabsTrigger` **仅当 `children.length > 0`** 渲染；`?tab=plan` 深链；复用 5s 轮询与页面隐藏暂停。
- i18n key 落 `issue.plan.*`（`progress` / `ready` / `blocked` / `hideDone` / `empty` 等），`en.json` + `zh-CN.json` 双份。
- 徽章 / 图标 / 配色全用既有 lucide 集与 Tailwind 方案，不引新依赖。

---

## 边界情况

- **多层 roadmap**：epic 自己也可能是别人的子 issue；plan 视图在任意层渲染「以我为根的子树」。全仓所有顶层 epic 的聚合 = 第二期的仓库级路线图页（见下）。
- **依赖成环**（边落地后）：服务端检测、忽略该边并在 header 警告。
- **超大树**：默认展开 +「隐藏已完成」折叠 merged 子树降噪；规模再大时考虑懒展开 / 虚拟列表。
- **孤儿 / 已关闭的父**：`/children` 仍正常返回，聚合不受影响。

---

## 分期

- **第一期（本文）**：epic 详情页的 `plan` tab，大纲树 + 进度卷起 + 就绪条（启发式），`GET .../plan` 只读端点，零迁移。
- **第二期**：仓库级「路线图」页——在 [RepoSidebar.vue](../apps/web/app/components/layout/RepoSidebar.vue) 加 nav 项，列出全仓所有顶层 epic 的进度卷起。这是 M9 缺的跨 issue 宏观视角。
- **点亮项（依赖第 1、2 层）**：依赖边落地 → 就绪/阻塞转精确；推进引擎落地 → header 的自动推进状态 + 预算 + ⏸ 暂停生效。

---

## 不在本设计里的事

- **不设计依赖边本身**（`issue_dependencies` 表、`issue_depends_*` 工具、planner role）——那是第 1 层，见 [依赖边与计划制定](./plan-dependencies.md)。
- **不设计推进引擎**（`issue.child_closed` 触发器、就绪前沿派活、cron 兜底、预算 / 并发 / pause 闸）——那是第 2 层，见 [推进引擎](./plan-engine.md)。
- **不做拖拽改状态 / 甘特图 / 时间线**——没有日期 / 估时数据，且状态应来自 issue 真实状态机。
- **不做 DAG 图可视化**（phase 0）——需依赖边 + 图布局库，留作未来增强。
