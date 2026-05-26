# 长期计划 · 依赖边与计划制定（原型）

[← ROADMAP](../ROADMAP.md) · [计划视图](./plan-view.md) · [推进引擎](./plan-engine.md) · [agents.yml schema](./agent-config.md)

目标：给长期计划补上**「制定」**的两块缺件——issue 之间的**依赖边**（让 tree 升级成 DAG，表达「A 做完才能做 B」），以及一个**planner role**（把一个目标 issue 拆成子 issue 树 + 依赖边）。这是三层计划系统（见 [计划视图 §定位](./plan-view.md)）的**第 1 层**。

> **本文是原型 / 草案**。决策点用【决策 N】标注，留待实现时定稿。范围只含「制定」；推进交给 [推进引擎](./plan-engine.md)，展示交给 [计划视图](./plan-view.md)。

---

## 为什么要改

[计划视图](./plan-view.md) 已确立「计划 = issue 树」（复用 `issues.parent_id`）。但树只表达**包含**，不表达**顺序**：

- 无法说「#203 cron 触发器要等 #202 串行引擎 merge 后才能开工」。
- 因此计划视图的「就绪 / 阻塞」只能退化成启发式，推进引擎也无法算出「下一个该做什么」。

依赖边就是补这条**顺序**的最小原语。有了它，「就绪前沿」(ready frontier) = 所有依赖都已 merged 的未开始 issue，变得可精确计算——这同时点亮计划视图的就绪条，也是推进引擎的派活依据。

---

## 设计原则

1. **边是 issue 之间的关系，不是新工作单元。** 沿用「计划 = issue 树」，只在 issue 之间加一种**有向边**，不引入新的实体类型。
2. **DAG，不允许环。** 插入边时即时检测可达性，拒绝成环的边（否则就绪计算会死循环 / 永远阻塞）。
3. **planner 只制定、不写码。** planner 拆解目标、建子 issue、连边、维护计划正文；具体实现交给现有 worker role（server / web / runtime）。靠 prompt + 触发器范围约束，不靠新的权限门。
4. **复用既有合并级联。** `issue_create` 已支持 `parent_number`，且**子 issue 的 base 分支自动指向父的 issue 分支**（见 [handler.go](../apps/hangrix/internal/modules/issue/handler/handler.go) `create` 路径注释）——子完成即 fast-forward 进父。planner 建树即白得这套级联，不需新机制。
5. **一处计算就绪。** `ready / blocked` 的判定落在 issue 模块 `domain/`，由 `/plan` 端点（视图）与 spawner / 引擎（推进）共享，禁止旁路 fast path。

---

## A. 依赖边

### 数据模型

issue 模块下新增一条 goose 迁移（版本号 = 现有最大值 + 1，5 位补零），**遵守「不改老迁移、向前可应用」**（见 [tech-stack §迁移工作流](./tech-stack.md) 与 ROADMAP 工程基线）：

```sql
-- +goose Up
CREATE TABLE issue_dependencies (
  id            BIGSERIAL PRIMARY KEY,
  repo_id       BIGINT NOT NULL,
  issue_id      BIGINT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,   -- 被阻塞方
  depends_on_id BIGINT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,   -- 阻塞方（前置）
  created_by    BIGINT,                                                    -- actor（planner / 人）
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT issue_deps_no_self CHECK (issue_id <> depends_on_id),
  UNIQUE (issue_id, depends_on_id)
);
CREATE INDEX idx_issue_deps_issue      ON issue_dependencies(issue_id);
CREATE INDEX idx_issue_deps_depends_on ON issue_dependencies(depends_on_id);

-- +goose Down
DROP TABLE issue_dependencies;
```

### 语义

- 边 `issue_id → depends_on_id` 表示 **issue_id 在 depends_on_id 被 merged 之前处于阻塞**。
- **blocked(I)** = 存在某条 `I → D` 且 `D.state != merged`。
- **ready(I)** = `I.state = open` 且**未启动**（无 running session / 无 in_progress todo）且**非 blocked**。
- **就绪前沿(epic E)** = E 子树中所有 ready 的叶子。

### 成环检测【决策 1】

加边 `A → B` 前，查 B 是否已（传递）依赖 A——用一条 `WITH RECURSIVE` 可达性查询。命中则拒绝（4xx + 结构化错误，复用 v1 错误通道）。**决策**：环检测放服务层（不是 DB 触发器），因为要返回 agent 可读的结构化拒绝理由。

### 平台工具（agent 用）

挂到 platform_api registry（[api.go](../apps/hangrix/internal/modules/platform_api/service/api.go)），并入 `tools` 白名单规则：

| 工具 | 权限 | 作用 |
| --- | --- | --- |
| `issue_depends_add(issue_number, depends_on_number)` | write | 加一条依赖边（含环检测） |
| `issue_depends_remove(issue_number, depends_on_number)` | write | 删边 |
| `issue_deps_read(issue_number)` | read | 返回 `{ depends_on[], blocked_by[](未满足的), blocks[] }` |

### HTTP 端点（人类 UI parity，与工具同走 domain）

```
GET    /api/repos/{o}/{n}/issues/{number}/dependencies
POST   /api/repos/{o}/{n}/issues/{number}/dependencies      { depends_on: <number> }
DELETE /api/repos/{o}/{n}/issues/{number}/dependencies/{depends_on}
```

### 就绪计算落点

issue `domain/` 加 `ReadyState(ctx, repoID, number)` 与子树批量版（供 `/plan` 端点一次算完整棵树）。[计划视图](./plan-view.md) 的 `GET .../plan` 端点此前 `depends_on=[] / blocked=false / ready=启发式`，本层落地后这些字段转**精确**——视图骨架不变（这正是视图层「优雅降级」设计的兑现）。

---

## B. planner role

### 角色定位

新增 role `planner`，配 `.hangrix/agents/planner.md`（front matter + body-as-prompt，遵守 [per-role md 约定](./agent-config.md)）。它**不写业务代码**——职责是把目标拆成可执行的 issue DAG。

### 触发与启用【决策 2】

- **v0：mention 驱动。** `triggers: { issue.comment: { mentioned_only: true } }`——用户 `@agent-planner 把这个需求拆成计划` 才唤醒。显式、零 epic 检测 schema（计划视图已用「有子 issue」判定 epic）。
- 备选：给 issue 加 `epic` 标签 / `kind` 字段做自动触发。v0 不做，避免过早加 schema。

### 行为（prompt 职责）

planner 被唤醒后：

1. **澄清范围**：必要时在 issue 里追问，把模糊目标收敛成可拆解的范围。
2. **拆解**：用 `issue_create(parent_number=<epic>)` 建子 issue（自动挂树 + base 指向父分支）。可多层（子 epic）。
3. **连边**：用 `issue_depends_add` 把步骤间顺序连成 DAG。
4. **写计划正文**：把计划落进 epic 的 body——一份**结构化 TODO**（复用 `issue_todo_*`），每项链到一个子 issue。这就是计划视图渲染的源数据之一。
5. **维护**：子完成 / 范围变化时回来更新（见 [推进引擎](./plan-engine.md) 的 `issue.child_closed` 唤醒）。

### 工具白名单

```yaml
# agents.yml tools 规则
planner:
  - issue_read
  - issue_read_by_number
  - issue_children
  - issue_create
  - issue_edit
  - issue_todo_*
  - issue_depends_*
  - issue_deps_read
  - roster_list
  - contribution_read   # 只读了解现状
```

`permission: write`（建 issue / 改 body / 连边），但**不给** contribution 写 / merge / 代码改动工具——拆解与实现分离。

### 幂等 / 重规划

重复唤醒不能重复建子 issue。planner prompt 要求**先 `issue_children` 读现状再 diff**：只补缺失的步骤、只连缺失的边。【决策 3】是否要服务端兜底去重（如 `issue_create` 带 `idempotency_key`）留待实现观察 planner 行为后再定。

---

## 与现有系统的边界

### 复用

- `issues.parent_id` / `parent_number` 树（[00002_add_issue_parent.sql](../apps/hangrix/internal/modules/issue/infra/migrations/00002_add_issue_parent.sql)）。
- `issue_create` 的 `parent_number` 支持 + 子 base→父分支级联（[handler.go](../apps/hangrix/internal/modules/issue/handler/handler.go)、[api.go](../apps/hangrix/internal/modules/platform_api/service/api.go)）。
- platform_api 工具 registry、`tools` 白名单规则、per-role md、v1 结构化错误通道。

### 不做

- **不做自动 epic 检测**（v0 mention 驱动）。
- **不做边的权重 / 类型**（只有「blocks」一种语义；不区分 hard/soft）。
- **不做推进 / 派活**——那是 [推进引擎](./plan-engine.md)。
- **不做跨仓库依赖**（issue 按 repo 编号，边约束同 repo）。

---

## 给下游两层的接口

- **计划视图**：`/plan` 端点的 `depends_on / blocked / ready / 就绪前沿` 从启发式转精确。
- **推进引擎**：消费 `domain.ReadyState` 算出的就绪前沿来派活；消费 planner 写好的子 issue 树作为推进对象。

---

## 不在本设计里的事

- **推进引擎**（`issue.child_closed`、就绪前沿派活、cron 兜底、预算 / 并发 / pause）——见 [推进引擎](./plan-engine.md)。
- **计划视图 UI**——见 [计划视图](./plan-view.md)。
- **边的可视化 DAG 图**——计划视图 phase 0 不做图布局。
- **soft 依赖 / 优先级 / 估时**——超出最小原语，按需再加。
