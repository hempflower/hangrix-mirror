# 长期计划 · 推进引擎（原型）

[← ROADMAP](../ROADMAP.md) · [计划视图](./plan-view.md) · [依赖边与计划制定](./plan-dependencies.md) · [agents.yml schema](./agent-config.md)

目标：给长期计划补上**「推进」**——一个主动驱动器，能看一眼计划、挑出**就绪前沿**、把下一批工作派出去，并在计划完成 / 卡住 / 漂移时持续运转。这是三层计划系统（见 [计划视图 §定位](./plan-view.md)）的**第 2 层**，依赖 [第 1 层依赖边](./plan-dependencies.md) 提供的就绪计算。

> **本文是原型 / 草案**。决策点用【决策 N】标注。它假定 [依赖边](./plan-dependencies.md) 已落地（`domain.ReadyState` 可算就绪前沿）。

---

## 为什么要改

当前 agent 是**纯反应式**的：spawner 只在 `issue.opened` / `issue.comment` / `commit.pushed` / `issue.closed` 等事件上唤醒 role（见 [triggers.go](../apps/hangrix/internal/agentsconfig/triggers.go) 与 [spawner](../apps/hangrix/internal/modules/agent_session/service/spawner.go)），session 绑死单个 issue 生命周期。**没有任何东西会主动地：子任务完成后挑出下一个该做的、派活、并在没人推一把时自己往前滚。**长期计划即使被 planner 拆好了，也只能靠人一个个去开 issue / 戳 agent。

推进引擎补的就是这条**自驱的闭环**。

---

## 设计原则

1. **派活确定化，LLM 只用于干活与重规划。** 引擎本身是**服务端确定性组件**，不是 agent——它从依赖边算就绪前沿并直接派 worker session。planner LLM 只在「制定 / 重规划 / 周期复盘」时跑。这样**便宜、可预测、不 thrash**。
2. **事件驱动为主，定时兜底为辅。** 子完成事件推动快速前进；低频 cron 兜住「没人完成任何事 → 永远不前进」的卡死，以及长周期计划的漂移复盘。
3. **可见 / 可停 / 有预算。** 呼应 ROADMAP 原则 5——所有自动派活落审计、计划视图可见；每个计划有并发上限 + 自动步数 / 成本预算 + 一键 pause。安全靠事后约束，不做事前门禁，但**钱必须有闸**（每步都 spawn session 烧 token）。
4. **复用 spawner 派活。** 引擎不另起一套调度，而是调 `spawner.OnTrigger` 投递新触发器——审计、容器、token、历史回放全部复用。

---

## 核心抉择：引擎驱动 vs planner 驱动【决策 1】

| | 引擎驱动（**推荐**） | planner 驱动 |
| --- | --- | --- |
| 派活由谁决定 | 服务端确定性算就绪前沿 | 每步唤醒 planner LLM 重算 |
| 成本 | 低（派活零 LLM） | 高（每步一次 LLM） |
| 可预测性 | 高、确定 | 低、可能 thrash |
| 计划适应性 | 靠 planner 在「重规划 / cron 复盘」时调 | 随时可调 |

**推荐引擎驱动**：派活走确定性引擎；planner LLM 只在 (a) 初次制定（第 1 层）、(b) 收到重规划请求、(c) 低频 cron 复盘 时运行。两者职责清爽分离。

---

## 触发与信号

新增两个触发器（加到 [triggers.go](../apps/hangrix/internal/agentsconfig/triggers.go) 的常量 + `validTriggers` 映射——那是单一来源，漏加会**静默丢事件**）：

| 触发器 | 何时 fire | 谁消费 |
| --- | --- | --- |
| `issue.child_closed` | 某子 issue merged / closed 时，向**父 issue** 投递（cause = 该子 issue） | 引擎（重算父的前沿） |
| `issue.ready` | 引擎判定某 issue **可开工**时（建时无依赖、或最后一个前置 merged） | worker role（server / web / runtime …） |

【决策 2】`issue.ready` vs「spawner 加就绪门」二选一：
- **方案 A（推荐）**：引入 `issue.ready` 作为「这个 issue 现在可干活」的显式信号，worker 订阅它而非裸 `issue.opened`。概念干净：`issue.opened` = 它存在了；`issue.ready` = 去干它。独立（非 epic）issue 在 open 时引擎立即 fire `issue.ready`（无依赖），行为与现状一致。
- **方案 B**：worker 仍订阅 `issue.opened`，但 spawner 加「就绪门」——派活前若 issue 被阻塞则不派、记为 blocked，前置 merged 后引擎再补派。改动小但 worker 触发语义变隐晦。

> ⚠️ 若任何**带 CHECK 约束的列**（如 `workflow_runs.event_name`）要纳入新事件名，必须走**向前迁移**补 CHECK，别原地改老迁移——否则会重蹈「push_tag 被 CHECK 静默拒绝」的覆辙。issue 侧 `issue_events.kind` 是 TEXT 无 CHECK，不受此限。

---

## 推进闭环（事件驱动）

```
worker 把某子 issue 干完 → 该 issue merged
        │
        ▼  (issue 模块 merge 路径，紧邻现有 OnTrigger 调用，约 handler.go:1092)
  fire issue.child_closed → 父 epic
        │
        ▼  引擎接管
  1. 在 epic body 勾掉对应 TODO（issue_todo_update）
  2. domain.ReadyState 重算 epic 子树的就绪前沿
  3. 对每个「新就绪 + 未派过」的子 issue：
        检查安全闸（并发 / 预算 / pause）→ 通过则 fire issue.ready
        │
        ▼
  worker 被唤醒，在该子 issue 上干活 …（循环）
```

子 base→父分支的级联（[第 1 层 §复用](./plan-dependencies.md)）意味着**最后一个子 issue 合并即把 epic 分支推进齐**，epic 自身随后可 merge——计划自然收口。

---

## 兜底：cron / 周期扫描【决策 3】

纯事件驱动会卡死（没人完成任何事就不前进），且长周期计划需要定期复盘。兜底两件事：

1. **失速恢复**：周期性（如每小时）扫描活跃 epic，把「就绪但因当时撞并发上限 / 临时失败而没派出去」的工作补派。
2. **漂移复盘**：低频（如每日 / 每周）fire `plan.review` 唤醒 planner LLM，让它判断「优先级变了吗、计划还成立吗」并调整树 / 边。

**实现路径**：v0 用**服务端 ticker**（引擎内一个定时器扫 `plan_state.status='active'` 的 epic）——自包含、不依赖外部调度。备选：若 M8 [Workflow 系统](./workflow-system.md) 长出 `schedule` 触发器，复盘可迁过去；当前 workflow 触发器只有 push / push_tag / issue.* / dispatch，**还没有 cron**，故 v0 不依赖它。

---

## 安全闸（可见 / 可停 / 预算）

每个计划（= epic issue）一份运行态。【决策 4】用**独立小表** `plan_state`（keyed by epic issue id）承载——它是**引擎运行态 / 配置**，不是与 issue 竞争的「计划实体」，不违背「计划 = issue 树」：

```sql
plan_state (
  epic_issue_id    BIGINT PRIMARY KEY REFERENCES issues(id) ON DELETE CASCADE,
  status           TEXT NOT NULL DEFAULT 'active',   -- active | paused
  max_concurrency  INT  NOT NULL DEFAULT 1,          -- 同时在飞的子 session 上限
  auto_step_budget INT  NOT NULL DEFAULT 10,         -- 自动派活步数预算
  auto_steps_used  INT  NOT NULL DEFAULT 0,
  -- 可选：成本上限，读 LLM usage log（见 llm-proxy.md）
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
)
```

闸的判定（派活前）：

- **pause**：`status='paused'` → 不派任何活。
- **并发上限**：统计该 epic 子树中 running 的 session 数，≥ `max_concurrency` 则不派，等下次事件 / tick。
- **预算上限**：`auto_steps_used >= auto_step_budget` → 停手，在 epic 评论里请求人类续额（呼应「可停」而非「失控」）。
- **一键停**：复用现有 session 级 kill（ROADMAP 原则 5「admin 一键停某 agent」）；计划级 pause 是其聚合形态。

**可见**：每次自动派活写审计（`issue_events` / 计划事件），[计划视图](./plan-view.md) header 即可显示「自动推进：开 · 预算 3/10 · ⏸暂停」。这就是视图层预留钩子的兑现。

---

## 数据 / 代码增量

- **触发器**：`triggers.go` + `issue.child_closed`、`issue.ready`（及可选 `plan.review`）；同步更新解析器 / 派发器 / 审计 UI 的 enum 分支。
- **迁移**：新增 `plan_state` 表（向前可应用 + Down）。
- **引擎落点【决策 5】**：放新模块 `internal/modules/plan_engine/`，还是并入 `agent_session`？倾向**独立模块**（推进逻辑 + ticker + 安全闸自成一体），通过 ioc 消费 issue `domain.ReadyState` 与 `spawner`。
- **merge 路径挂钩**：在 issue 模块现有 `OnTrigger` 调用旁（merge/close 路径，约 [handler.go:1092](../apps/hangrix/internal/modules/issue/handler/handler.go)）追加：若 issue 有父，向父 fire `issue.child_closed`。

---

## 风险与边界

- **thrash / 抖动**：同一事件可能多次触发重算——引擎要对「就绪前沿派活」做去重 / debounce（per epic 串行处理一轮）。
- **成本失控**：靠 `auto_step_budget` + 成本上限硬闸；超额停手等人。
- **依赖成环**：第 1 层加边时已拒环；引擎再做一道防御性环检测，命中则告警不死循环。
- **失败处理**：worker session 失败的子 issue 不应被当作「完成」推进；引擎只在子 issue **merged** 时视为完成，failed/closed-未合并 不解锁后继（与计划视图进度口径一致）。

---

## 不在本设计里的事

- **依赖边 / planner 制定**——见 [依赖边与计划制定](./plan-dependencies.md)。
- **计划视图 UI**——见 [计划视图](./plan-view.md)。
- **跨计划全局调度 / 优先级仲裁**——v0 每个 epic 独立推进，不做跨 epic 资源竞争仲裁。
- **复盘的 LLM 编排细节**——`plan.review` 唤醒 planner 后的具体 prompt 策略交给 planner role 定义，不在引擎里写死。
