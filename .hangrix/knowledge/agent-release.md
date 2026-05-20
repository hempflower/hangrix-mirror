# Agent 发布流程

本项目的发布由 `.github/workflows/release.yml` 驱动，触发条件是推送 `v*` 格式的 semver 标签（如 `v0.4.0`）。Agent 在发布相关任务中需要了解：CI 自动构建产物并上传，以及平台提供了 `release_*` 系列工具用于管理 Release。

## CI 发布流程（自动化）

推送 `v*` 标签后，`release.yml` 工作流执行以下步骤：

1. **构建前端** — `pnpm --filter web run generate` 生成静态 SPA。
2. **构建嵌入载荷** — `apps/hangrix/scripts/build-embed-binaries.mjs` 一次性构建所有 agent + per-arch runner 二进制。
3. **交叉编译服务端** — 为 `linux/amd64` 和 `linux/arm64` 分别编译 `hangrix` 服务端。
4. **上传产物** — 通过 `softprops/action-gh-release@v2` 将产物附加到 GitHub Release。

最终每个 Release 包含 4 个产物：

| 产物名 | 说明 |
|---|---|
| `hangrix_linux_amd64` | 服务端（含全部 runner + agent） |
| `hangrix_linux_arm64` | 同上，arm64 架构 |
| `hangrix-runner_linux_amd64` | 独立 runner（agent 嵌入） |
| `hangrix-runner_linux_arm64` | 同上，arm64 架构 |

产物详情见 `.github/workflows/release.yml:104-119`。

## Agent 可用的发布工具

平台为 agent 提供了 `release_*` 系列工具，可直接在 issue 上下文中操作 Release：

- `release_create` — 从已有的 git tag 创建 draft release。
- `release_upload_asset` — 上传自定义产物文件到指定 release（需 base64 编码）。
- `release_publish` — 发布 draft release，使其正式可见。
- `release_update` — 编辑 release 的 title、notes（draft 时也可改 tag_name）。
- `release_delete` — 删除 release 及其所有自定义产物。

**注意**：`release_upload_asset` 上传的是*自定义*产物（区别于 tag 关联的源码压缩包）。CI 流程中的产物已由 workflow 自动上传，agent 一般不需要重复此步骤；该工具用于补充上传 CI 未覆盖的额外文件。

## 发布操作参考

- CI 工作流：`.github/workflows/release.yml`
- 嵌入构建脚本：`apps/hangrix/scripts/build-embed-binaries.mjs`
- Web 构建脚本：`apps/hangrix/scripts/copy-web-dist.mjs`
