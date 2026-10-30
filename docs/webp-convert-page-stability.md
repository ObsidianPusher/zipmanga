# WebP 转换页卡死与日志移除

最后更新：2026-10-26

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

## 转换兼容性与失败摘要

- ZIP/CBZ、RAR/CBR 转换统一过滤 `__MACOSX` 和 `._*` 元数据，扫描与实际读取使用同一规则，防止伪图片导致失败或页码错位。
- AVIF 当前没有解码器，按原字节和原扩展名保留，计入 `kept`；输出 ZIP 因此可能包含 AVIF，并非所有页面都会转成 WebP。已有 WebP 继续原样保留。
- 有损 VP8 编码失败后尝试无损 VP8L，不缩放或丢弃页面。VP8L 最长边上限为 16384 像素，超过上限或两种编码都失败时仍明确报错；无损输出可能更大。
- 新任务错误摘要包含首个失败压缩包、图片路径和底层错误；损坏图片不会被静默跳过。
- 详情接口默认仍不返回完整日志。失败任务或已有失败项的任务额外返回 `task.failure_examples`，只读取最早 3 条 `error` 日志，每条最多 1024 字符。摘要界面显示这些样例，历史任务只要原日志仍在即可查看原因。
- 回归测试：`internal/server/webp_convert_test.go`、`internal/imgcodec/lossy_webp_test.go`。覆盖元数据、AVIF 原样保留、编码降级、页名错误上下文、失败时保留旧输出及有界错误样例。

## 文件名称兼容性

- 同目录下同名的 ZIP/CBZ/RAR/CBR，以及忽略大小写、Unicode NFC 后重名的输出，使用源相对路径生成稳定哈希后缀，避免本批任务相互覆盖；无冲突的普通名称保持不变。
- 输出 ZIP 文件名限制在 240 字节内，超长名称在完整 UTF-8 字符边界截断并追加哈希。目录名和文件名的共享清理函数也不再截断半个字符。
- 包内图片去重同时检查自然名称和已生成名称，避免 `001.png`、`001.jpg`、`001__1.png` 等转换后再次撞名；大小写和 Unicode NFC 等价名称也参与去重。
- 临时 ZIP 使用同目录下独立的短随机名称，避免长名称超限或并发转换互相截断；输出权限保持 `0644`。
- ZIP 优先使用带正确原名称 CRC 的 Info-ZIP Unicode Path 字段；无法确认编码的非 UTF-8 名称按路径组件可逆百分号转义，不猜测 GBK 或 Shift-JIS。
- 以上规则不自动迁移或删除已有输出。名称回归测试见 `internal/server/webp_names_test.go`。

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
