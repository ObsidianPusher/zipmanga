# ZIP Manga Docs

最后更新：2026-08-05

## 背景

这个目录用于沉淀后续继续维护本仓库所需的最小闭环文档，优先记录真实代码链路、状态流、关键证据和影响面，不写泛泛说明。

## 现状链路

- WebP 离线转换主链路：`internal/server/webp_task.go` + `internal/server/webp_convert.go`
- 前端管理页主入口：`web/static/app.js`
- 管理页结构：`web/templates/app.html`
- 样式：`web/static/styles.css`

## 索引

- `docs/webp-convert-page-stability.md`：WebP 转换页卡死与日志移除改造
- `docs/webp-convert-restart-and-memory-optimization.md`：WebP 转换重启恢复、落盘链路与内存峰值优化

## 关键证据

- 当前仓库历史说明仍主要在 `doc/` 下；本目录从 2026-08-05 起作为新的归集入口使用。
- 本次 WebP 页问题修复已同步更新这里，不再继续只落在旧 `doc/`。

## 数据/状态流

- 页面加载 -> `loadWebPTasks()` -> `loadWebPTaskDetail()`
- 任务详情 -> `/api/admin/webp/tasks/{id}`
- 后端默认只返回任务摘要，日志改为按需显式请求
- WebP 转换完成态 -> `webp_archive_manifests` 持久化 -> 重启后按 manifest 快速判定已完成压缩包

## 结论

- 后续处理前应优先检索 `docs/`，必要时再对照旧 `doc/` 历史文档。

## 风险

- `doc/` 与 `docs/` 会在一段时间内并存，后续若继续整理，需要逐步把仍有价值的内容迁入 `docs/`。

## 待确认

- 是否在下一轮统一迁移 `doc/` 历史内容并删除旧目录。
