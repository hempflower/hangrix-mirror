# 技术选型

本文档记录 Hangrix 的技术栈和**为什么选它**。
新增依赖前先回到这里看一眼——本仓库的口味是「克制 + 收敛」，能复用现有形态就不引入新形态。

## 一句话总览

单二进制 Go 服务（`apps/hangrix`）内嵌一份 Nuxt 4 静态 SPA（`apps/web`），背后是 **Postgres + Redis**。前端走 **shadcn-vue + Tailwind v4**，表单走 **vee-validate + zod**，文案走 **@nuxtjs/i18n（默认简体中文）**。后端用 **chi** 路由 + 自研 **ioc** DI 容器，数据访问用 **sqlc** 生成的类型安全代码。

## 后端

| 选型 | 角色 | 关键说明 |
| --- | --- | --- |
| **Go 1.26** | 主语言 | 单二进制部署、强类型、标准库够强。 |
| **chi v5** | HTTP 路由 | radix 树让静态路径优先于 SPA catch-all。中间件契约即 `func(http.Handler) http.Handler`，便于自研 auth 中间件复用。 |
| **viper** | 配置加载 | YAML + env override，`API_` 前缀。env > YAML > `SetDefault`。 |
| **pkg/ioc** | 反射式 DI 容器 | 模块化单体的"胶水"。构造器约定：返回 `*Struct` 或接口；参数为 0/1 个 `*Deps` 指针。允许接口返回值是为了对接 `redis.UniversalClient` 这类第三方接口型工厂。 |
| **air** | Go 热重载 | dev 模式下监听 `cmd/`、`conf/`、`internal/`、`../../pkg`。 |
| **PostgreSQL 17** | 关系型主存 | 长寿命业务数据（用户、仓库、issue 等）。本地通过 `docker-compose.yml` 起。 |
| **pgx/v5 + pgxpool** | Postgres 驱动 | 与 sqlc 配套的官方推荐组合，性能 + 类型支持都比 `database/sql` 好。 |
| **sqlc v1.31+** | SQL → Go 代码生成 | 每个模块在 `infra/queries.sql` 写 SQL，`infra/migrations/*.sql` 记录 schema 演进，sqlc 把整个 migrations 目录串起来当作最终 schema 来生成 `infra/<name>db/` 子包。**手写 SQL 是产品决策**：ORM 在涉及复杂查询时会反过来束缚我们，sqlc 让我们保持对 SQL 的掌控。 |
| **goose v3**（库模式） | 数据库迁移 | 嵌入式调用，不依赖 CLI。`internal/database/Migrate` 是统一入口：每个模块在 `infra/migrations/*.sql` 写带 `-- +goose Up/Down` 注释的迁移，模块自己的 `New*Repo` 构造器调用 `Migrate(pool, sub, "goose_<module>", ".")`，goose 在 DB 里维护**每个模块独立的版本表**（如 `goose_user`），不串模块边界。启动时按版本顺序应用未跑过的迁移。 |
| **pgerrcode** | Postgres 错误码常量 | 用来识别唯一约束冲突等具体错误。 |
| **Redis 7 (UniversalClient)** | KV / 会话存储 | 通过 `redis.UniversalClient` 暴露：standalone、sentinel、cluster 三种模式只换配置不换依赖类型。当前承载 session（含 `user_sessions:<id>` 二级索引以便强制下线）。 |
| **golang.org/x/crypto/bcrypt** | 密码哈希 | 加盐、不可逆。 |

### 后端代码组织

模块化单体（见 [AGENTS.md](../AGENTS.md)）：

```
internal/modules/<name>/
├── domain/             # 值类型 + 接口（仅有的对外契约）
├── infra/              # 具体实现（DB / Redis / 外部 HTTP）+ sqlc 生成子包
│   ├── migrations/     # 嵌入式 goose 迁移 *.sql，启动时应用
│   ├── queries.sql     # 手写 SQL，sqlc 生成 Go
│   └── <name>db/       # sqlc 输出，DO NOT EDIT
├── handler/            # chi HTTP handler，组装 domain 接口
└── module.go           # 唯一允许 import 上述层的入口；通过 ioc 注册
```

**硬约束**：跨模块依赖只能走 ioc + 对方 `domain/` 接口，**不允许** import 其他模块的 `handler/`、`infra/`。

### 迁移工作流

新增一条 schema 变更时：

1. 在 `internal/modules/<name>/infra/migrations/` 下新建 `<next-version>_<short-name>.sql`，版本号是已有最大值 + 1，**zero-padded 5 位**（goose 按文件名前缀的数字排序）。
2. 文件内容用 goose 注释分隔上下行：
   ```sql
   -- +goose Up
   ALTER TABLE users ADD COLUMN avatar_url TEXT;

   -- +goose Down
   ALTER TABLE users DROP COLUMN avatar_url;
   ```
   多语句迁移用 `-- +goose StatementBegin` / `StatementEnd` 包裹，避免 goose 按分号粗暴切分。
3. 重跑 `sqlc generate`（sqlc 把整个 migrations 目录拼接当 schema）。
4. 改 `queries.sql` / Go 代码用新字段。
5. 重启服务，goose 自动应用 pending 迁移。

**禁止改老迁移文件**——如果改了已经跑过的迁移，goose 不会回放，DB 和代码会失同步。要修就再加一条向前的迁移。

**baseline 例外**：`00001_*.sql` 用了 `CREATE TABLE IF NOT EXISTS`，目的是让历史上由 `Exec(schemaSQL)` bootstrap 出来的 DB 平滑接入迁移系统。**后续任何迁移都不允许带 `IF [NOT] EXISTS` 守卫**——它会掩盖与预期 schema 不一致的真实问题。

### 共享基础设施

| 路径 | 提供 | 用途 |
| --- | --- | --- |
| `internal/database` | `*pgxpool.Pool` | 所有 `modules/*/infra` 的 PG 入口。 |
| `internal/kv` | `redis.UniversalClient` | 所有 KV / 会话场景共用。 |
| `internal/server` | `*chi.Mux` + `RouteProvider` 接口 | 模块通过 ToInterface 注册到 `[]RouteProvider`，Server 自动挂载。 |
| `internal/web` | `//go:embed all:dist` | 把 Nuxt 静态产物烤进二进制。 |

## 前端

| 选型 | 角色 | 关键说明 |
| --- | --- | --- |
| **Nuxt 4** | 应用框架 | 当前 `ssr: false`（SPA 模式），由 Go 二进制托管。dev 时 nitro 反向代理 `/api` → `:8080`。 |
| **Vue 3.5** | 视图层 | Nuxt 自带。 |
| **TypeScript 5.9** | 类型 | `strict: true`，运行时 typecheck 暂关（vue-tsc 与 vue-router volar 插件不匹配）。 |
| **Tailwind CSS v4** | 样式 | 通过 `@tailwindcss/vite`，CSS-first 配置在 `app/assets/css/tailwind.css`。 |
| **shadcn-vue** | UI 组件库 | `style: new-york`，`prefix: ''`。**只用 `pnpm dlx shadcn-vue@latest add <name> --yes` 加组件**，不要再跑 `init`（配置已经手工对齐）。 |
| **reka-ui** | 无样式 primitives | shadcn-vue 底层。 |
| **lucide-vue-next** | 图标库 | shadcn-vue 默认配套。 |
| **@vueuse/core** | composable 工具集 | 输入控件等组件用到。 |
| **vee-validate 4** | 表单状态 + 校验 | 所有表单走 `<Form>` + `<FormField>` 组件，避免一个组件里多个 `useForm` 互相冲突的问题（profile 页面有两个独立表单）。 |
| **zod 3.x** | Schema 校验 | 配合 `@vee-validate/zod` 的 `toTypedSchema`。**版本锁在 3.x**，因为 `@vee-validate/zod` 4.x 还不支持 zod 4。 |
| **@nuxtjs/i18n 10** | 国际化 | `defaultLocale: 'zh-CN'`，`strategy: 'no_prefix'`（不在 URL 里加 `/zh-CN/`）。语言文件在 `apps/web/i18n/locales/<code>.json`，cookie 名 `hangrix_locale`。 |

### 前端约定

- **所有用户可见文案必须走 i18n**：禁止在 .vue 文件里写硬编码中文/英文。新文案先加到 `zh-CN.json` 再加到 `en.json`，键名按 `<page>.<key>` 组织。
- **所有表单走 vee-validate + zod**：不要再用裸 `ref` + 手动校验。Schema 用 `computed()` 包一层，让校验文案随 locale 切换刷新。
- **校验文案走全局 errorMap**：`plugins/zod-i18n.client.ts` 调用 `z.setErrorMap()`，把 zod 的 issue code 映射到 `validation.*` i18n 键。**不要再在每个 `.min()/.email()` 调用里塞 message 字符串**——schema 写成纯结构，文案由 errorMap 接管。例外：当一个字段需要专属、与默认消息不同的提示时再用第二参数覆盖。
- **Profile 这类多表单页面**：用 `<Form>` 组件而不是 `useForm()`，避免多 form 冲突。

### 布局矩阵

五种 layout，按场景区分；通过 `definePageMeta({ layout: '<name>' })` 选择：

| layout | 场景 | 结构 |
| --- | --- | --- |
| **`default`** | 登录后的工作区（默认）—— 仪表盘、个人资料等普通用户视图。 | `SidebarProvider` + 可折叠 `AppSidebar`（工作区 / 账号两组）+ `SidebarInset` 内含 `AppHeader`（侧栏开关 + 面包屑 + 语言切换）。`variant="inset"` 让内容区是 card-like 容器。管理员入口通过 footer 用户 dropdown 跳转到 admin 布局。 |
| **`admin`** | `/admin/*` 管理后台页面。 | 独立的 `AdminSidebar`（盾牌 logo + Admin 徽章 + 「管理」单一分组）+ footer 顶部固定「返回工作区」按钮。视觉上和 `default` 明显区分，让 admin 始终知道自己在管控视图。`AppHeader` 共用。 |
| **`auth`** | 登录、注册、忘记密码这类窄表单页。 | 全屏居中卡片 + 右上角语言切换。**不要再用 `layout: false`**——`auth` 已经覆盖这个场景。 |
| **`blank`** | 全屏无装饰：嵌入页、向导、特殊全屏视图。 | 只有 `<slot />`，背景色继承自主题。 |
| **`marketing`** | 公共顶导航的页面（未来的落地页 / docs / 定价）。 | 顶部 `MarketingNav`（logo + 语言切换 + 登录/去 dashboard 按钮）+ 居中 `max-w-6xl` 主体。 |

错误页（404 / 500）走 `app/error.vue`，独立于 layout 系统——Nuxt 在抛 fatal error 时直接渲染它，所以不能依赖 layout slot。

`AppSidebar` / `AdminSidebar` / `AppHeader` / `MarketingNav` 都放在 `components/layout/`，而不是 `components/ui/`，避免污染 shadcn 自动生成区域。**新增普通页面**改 `AppSidebar.vue` 的 `workspaceItems` / `accountItems` computed；**新增管理页面**改 `AdminSidebar.vue` 的 `manageItems`——两套 sidebar 各管各的，不要在 page 里塞导航。

#### tailwind-merge 版本约束

`tailwind-merge` 必须 `>=3`。v2 只认 Tailwind v3 的 `!`-前缀 important 语法（`!p-2`），而本项目用 Tailwind v4 的 `!`-后缀（`p-2!`）；v2 无法识别冲突，shadcn-vue sidebar 的 cva 里 `group-data-[collapsible=icon]:p-2!` 和 `p-0!` 会同时进 stylesheet，导致折叠态下 lg 按钮 padding 仍是 8px、logo / 头像被 `overflow-hidden` 切掉一半。

### 前端目录约定

```
apps/web/app/
├── components/ui/<name>/   # shadcn 生成，原样保留
├── components/layout/      # AppSidebar / AppHeader / MarketingNav 等布局拼装件
├── composables/            # useCurrentUser 等（Nuxt 自动导入）
├── layouts/                # default(sidebar) / auth / blank / marketing
├── middleware/             # auth.global.ts 路由守卫
├── pages/                  # 文件路由
├── plugins/                # auth.client.ts、zod-i18n.client.ts
├── types/                  # User、UserListResp 等 API DTO
├── utils/                  # cn() 等共享工具（Nuxt 自动导入）；components.json 的 `utils` 别名指向 `@/utils/utils`
└── error.vue               # Nuxt 顶层错误页（404/500），独立于 layouts
apps/web/i18n/locales/      # zh-CN.json、en.json
```

> 注：shadcn-vue 默认把 `cn` 放在 `app/lib/utils.ts`，我们把它移到 `app/utils/utils.ts` 以遵循 Nuxt 的自动导入约定（`app/utils/*` 会被注入到全局）。`components.json` 里 `aliases.utils` 也跟着改了，确保后续 `pnpm dlx shadcn-vue@latest add <name>` 生成的新组件会引到正确路径。

## 构建与编排

| 选型 | 角色 |
| --- | --- |
| **Turborepo** | 任务图。`web#generate` → `copy-web-dist` → `hangrix#build` 是嵌入打包的核心链路。 |
| **pnpm + workspaces** | JS 包管理。Go 模块通过 `go.work` 关联。 |
| **docker-compose** | 本地依赖（Postgres + Redis），见 `docker-compose.yml`。 |

## 已经做出但容易踩坑的决策

- **session 存 Redis 而不是 Postgres**：sessions 是天然的 TTL KV。RedisSessionStore 维护 `session:<token>` 和 `user_sessions:<id>` 两套键，后者用于禁用账号时强制下线全部会话。
- **首个注册账号自动 `admin`**：bootstrap 设计，避免空库时没人能进 admin 面板。后续不能在 UI 改回来——只能让 admin 把别人提升。
- **权限模型只有 `user` / `admin` 两级**：仓库级 ACL 推迟到真有多协作者需求时再加；当前 owner 判断都在 handler 内显式做。
- **users 只代表人类**：agent identity 不复用 users 表（也不再保留 `kind` 字段）。M4 会单独建模 agent 身份，账号系统就此回归 human-only，避免 "用户/智能体" 混在一张表带来的权限边界、密码、UI 文案歧义。
- **ioc 允许接口返回**：放宽了构造器返回类型的限制，否则 `redis.NewUniversalClient` 这种返回接口的工厂没法直接 `Provide`。

## 不在选型内的事

- **不引入 ORM（gorm/ent/bun…）**：sqlc 已经覆盖了类型安全 + 性能两个核心诉求，再多一层抽象只是增加心智负担。
- **不引入 JWT**：本地优先形态下，服务端 session（cookie + Redis）的可吊销性比 JWT 重要得多。
- **不引入状态管理库（Pinia / Vuex）**：当前只有 `useCurrentUser` 这一个全局态，`useState` 够用。等真出现需要细粒度选择器的复杂状态时再讨论。
- **不引入 ElementPlus / Naive 这类完整 UI 套件**：shadcn-vue 的「直接落到 components/ui/ 里、随手改」模型更契合本项目的产品形态——后面 issue 视图、agent 时间线这些都不是套件能直接给的样式。

## 升级 / 重新生成的命令

```sh
# 安装 Go 依赖
cd apps/hangrix && go get <pkg>

# 重新生成 sqlc（在 migrations 改完 / queries.sql 改完都要跑）
cd apps/hangrix && sqlc generate

# 加 shadcn 组件
cd apps/web && pnpm dlx shadcn-vue@latest add <name> --yes

# 启动本地依赖
docker compose up -d

# 查看某模块当前迁移版本（debug 用）
docker exec hangrix-postgres psql -U hangrix -d hangrix \
  -c "SELECT version_id, is_applied, tstamp FROM goose_user ORDER BY id;"
```

## MCP

仓库根目录 `.mcp.json` 注册了 **shadcn-vue MCP**（`npx shadcn-vue@latest mcp`），让在 Claude Code 内的 agent 可以直接查询 / 添加 shadcn 组件。验证：在 Claude Code 里跑 `/mcp` 看 `shadcn` 是否 Connected。如果不连接，重启 Claude Code。
