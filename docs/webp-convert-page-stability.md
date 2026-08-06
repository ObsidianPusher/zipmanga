# WebP 转换页卡死与日志移除

最后更新：2026-08-06

## 背景

管理页里的 WebP 转换模块在显示任务详情时会明显拖慢甚至卡死浏览器。用户同时明确要求该页面不再显示命令日志。

## 现状链路

1. 前端进入管理页后执行 `loadWebPTasks()`
2. 选中任务后继续执行 `loadWebPTaskDetail()`
3. 前端请求 `GET /api/admin/webp/tasks/{id}`
4. 后端 `handleAPIAdminGetWebPTask()` 返回任务详情和日志
5. 前端把日志逐条追加进 `<pre>`，轮询期间持续放大响应体、JSON 解析和 DOM 重排成本

## 关键证据

- 前端原先会持续累积 WebP 任务日志：`web/static/app.js`
- 页面原先使用 `<pre id="webpConvertOutput">` 承载日志：`web/templates/app.html`
- 后端详情接口原先默认返回 `logs`：`internal/server/webp_task.go`
- WebP 任务日志来自 `webp_task_logs`，任务执行过程中会持续写入：`appendPersistedWebPTaskLog()`

## 数据/状态流

- 列表接口：`GET /api/admin/webp/tasks`
  - 返回 1 个运行中任务
  - 返回最近 2 个已完成或失败任务
- 详情接口：`GET /api/admin/webp/tasks/{id}`
  - 现在默认只返回 `task`
  - 仅在显式 `include_logs=1` 时返回 `logs`
- 前端详情展示：
  - 进度文案：`webpConvertProgressText`
  - 统计文案：`webpConvertTaskMeta`
  - 摘要卡片：`webpConvertTaskSummary`

## 结论

本次卡死的主因不是单一 CSS 或轮询频率，而是 WebP 详情轮询把大日志响应和大文本 DOM 更新绑在了一起。修复策略是：

- 后端默认不返回 WebP 命令日志
- 前端不再为 WebP 页维护日志输出区域
- 页面右侧改为轻量任务摘要卡片

这样保留了任务状态、进度和错误可见性，同时去掉最重的渲染负担。

## 风险

- 如果未来需要重新查看 WebP 命令日志，需要显式使用 `include_logs=1` 或单独提供调试入口。
- 旧 `webp_task_logs` 数据仍会继续在后端保存，当前改动只是默认不向页面返回。

## 待确认

- 是否需要给管理员额外提供一个“调试模式”按钮，在明确需要时再加载 WebP 日志。

## 改动点清单与影响面

- 后端：
  - `internal/server/webp_task.go`
  - 影响 WebP 任务详情接口默认返回体
- 前端：
  - `web/static/app.js`
  - `web/templates/app.html`
  - `web/static/styles.css`
  - 影响 WebP 转换页详情展示与轮询成本
- 测试：
  - `internal/server/webp_task_test.go`
  - 增加“默认不返回日志”与“列表仅返回最近 2 个完成/失败任务”回归验证
