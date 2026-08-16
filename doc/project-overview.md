# 项目总览

最后更新：2026-08-04

## 1. 技术形态

- 后端：Go 1.22，标准库 `net/http`
- 存储：SQLite（`modernc.org/sqlite`，WAL）
- 前端：服务端模板 + 原生 `web/static/app.js`
- 静态资源：支持内嵌，也支持开发态 `--web-dir ./web`
- 第三方能力：
  - SMB：`github.com/hirochachacha/go-smb2`
  - WebDAV：`github.com/studio-b12/gowebdav`
  - PDF：`rsc.io/pdf`
  - WebP 编解码：`github.com/linzeyan/webp-go`（本地 replace 到 `third_party/linzeyan-webp-go`）

## 2. 目录职责

### 入口与构建

- `cmd/zipmanga/main.go`：CLI 入口，支持 `serve`、`db repair`、`version`
- `build-simple.sh` / `build-musl.sh`：构建脚本
- `internal/buildinfo`：构建信息与源码/资源指纹

### 应用装配

- `internal/app/app.go`
  - 配置加载
  - 数据库初始化
  - 运行时配置覆盖
  - 缓存/扫描/HTTP 服务装配
  - 后台任务调度

### 配置与持久化

- `internal/config/config.go`：配置结构、默认值、校验、默认配置文件生成
- `internal/db/store.go`：SQLite schema、迁移、修复、DAO
- `internal/db/models.go`：数据库领域模型

### 认证与用户侧状态

- `internal/auth/service.go`
  - 管理员引导创建
  - bcrypt 密码哈希
  - Session 创建、刷新、过期清理

### 挂载与文件访问

- `internal/mounts`
  - `manager.go`：Provider 生命周期缓存
  - `provider.go`：统一 Provider 接口
  - `local.go` / `smb.go` / `webdav.go`：三种挂载实现
  - `multi_root.go`：多根目录聚合

### 扫描与建库

- `internal/scanner/service.go`
  - 全量扫描
  - 增量扫描 `ScanChanged`
  - 目录遍历、漫画文件识别、封面/页数探测、批量替换 mount 下 comics

### 缓存

- `internal/cache/cover_cache.go`：封面缓存，按条目数淘汰
- `internal/cache/page_cache.go`：页面缓存，按总字节数淘汰

### 阅读/管理服务

- `internal/server/server.go`
  - 页面路由
  - API 路由
  - 漫画分页、封面、进度、收藏、用户设置
  - 运行时设置
- `internal/server/archive_task.go` / `archive_workflow.go`
  - 归档工作流任务：收集、解压、整理、重打包
- `internal/server/name_format.go`
  - 命名规范预览与应用
- `internal/server/webp_task.go` / `webp_convert.go`
  - 离线 WebP 转换任务与任务恢复

### 格式与图像处理

- `internal/comicfmt`：识别 ZIP/CBZ/PDF 等格式
- `internal/comicutil`：探测页数、封面等
- `internal/pdfutil`：PDF 读取/渲染
- `internal/imgcodec`：WebP 编码
- `internal/imgenhance`：图像增强
- `internal/archiveutil` / `internal/ziputil`：压缩包辅助

### 前端

- `web/templates/app.html`：主应用页模板
- `web/templates/login.html`：登录页模板
- `web/static/app.js`：全部主要前端交互
- `web/static/styles.css`：样式

## 3. 核心业务链路

## 3.1 启动链路

入口：

- `cmd/zipmanga/main.go`

装配：

- `app.New()` in `internal/app/app.go`

关键事实：

- 如果 `config.json` 不存在会自动生成默认配置。
- 数据库打开时会先迁移，再执行 `ValidateAndRepairSchema()`。
- 运行时设置优先从 `app_runtime_settings` 读取，再覆盖配置文件的 `runtime`。
- 启动后有 3 个后台周期任务：
  - `periodicSessionCleanup`
  - `periodicCacheReap`
  - `periodicScan`

## 3.2 挂载 -> 扫描 -> 漫画建库

主入口：

- 管理端手动全量扫描：`POST /api/scan`
- 周期扫描：`App.periodicScan -> scanner.ScanChanged`

核心代码：

- `internal/scanner/service.go`

主流程：

1. 从数据库取启用挂载 `ListEnabledMounts`
2. 通过 `mounts.Manager.Ensure` 获取对应 Provider
3. 遍历挂载目录，过滤隐藏目录/文件
4. 识别漫画文件（ZIP/CBZ/PDF 等）
5. 对需要的文件探测页数与封面
6. 调用 `ReplaceComicsForMount` 以挂载为粒度刷新 `comics`

关键约束：

- `ShouldHideEntry` 统一过滤以 `.` 开头及 `@eadir`、`.recycle`、`$recycle.bin`
- `ScanChanged` 会先比较挂载快照，未变化时跳过实际重扫
- WebDAV 会尽量走“轻探测”，避免远端高成本读取

## 3.3 登录 -> 会话 -> 权限控制

核心代码：

- `internal/auth/service.go`
- `internal/server/server.go`

主流程：

1. 登录 `POST /api/login`
2. 认证成功后创建 session token
3. token 写入 Cookie `zm_session`
4. API 页面通过 `authRequired` / `adminOnly` 包装
5. 请求期间会按 session TTL 做续期

## 3.4 书库/阅读器链路

主要路由：

- 页面：
  - `GET /`
  - `GET /history`
  - `GET /favorites`
  - `GET /reader/{id}`
  - `GET /settings`
  - `GET /admin`
- API：
  - `GET /api/comics`
  - `GET /api/comics/{id}`
  - `GET /api/comics/{id}/cover`
  - `GET /api/comics/{id}/pages/{index}`
  - `GET/POST /api/comics/{id}/progress`
  - `POST/DELETE /api/comics/{id}/favorite`
  - `GET /api/history`

核心代码：

- 前端：`web/static/app.js`
- 后端：`internal/server/server.go`

关键事实：

- 阅读主链路是按页请求，不是整本批量预取。
- 页面缓存和封面缓存都走 SQLite `cache_entries` + 磁盘文件。
- 阅读进度和收藏优先依赖 `comic_file_name_key` 维持扫描重建后的关联稳定性。
- 书库、收藏、历史、设置、管理页都复用同一个前端主脚本。

## 3.5 运行时设置

API：

- `GET /api/admin/runtime`
- `PUT /api/admin/runtime`

配置来源优先级：

1. `config.Default()`
2. `config.json`
3. SQLite `app_runtime_settings`

当前重点字段：

- `memory_limit_mb`
- `disk_cache_mb`
- `page_prefetch_pages`
- `page_load_parallelism`
- `webp_quality`
- `webp_method`
- `webp_concurrency`
- `webp_output_root`

注意：

- 旧字段 `frontend_page_prefetch_pages` 仍在 schema 里，仅用于兼容旧库，不应当作为现行公开行为恢复。
- `gc_percent` 也保留在 schema 写入路径里，属于兼容性遗留面。

## 3.6 管理端归档工作流

API：

- `POST /api/admin/archive-workflow/tasks`
- `GET /api/admin/archive-workflow/tasks`
- `GET /api/admin/archive-workflow/tasks/{id}`
- `POST /api/admin/archive-workflow/name-format/preview`
- `POST /api/admin/archive-workflow/name-format/apply`
- `POST /api/admin/archive-workflow/run`

动作类型：

- `collect`
- `extract`
- `organize`
- `repack`
- `format_names`
- `webp_convert`

实现位置：

- `internal/server/archive_task.go`
- `internal/server/archive_workflow.go`
- `internal/server/name_format.go`

关键事实：

- 归档工作流只允许本地挂载。
- 任务状态保存在进程内存，提供日志和轮询，不是分布式任务系统。
- 同一套任务视图同时承载普通归档任务和 WebP 转换任务。

## 3.7 离线 WebP 转换

API：

- `GET /api/admin/webp/tasks`
- `POST /api/admin/webp/tasks`
- `GET /api/admin/webp/tasks/{id}`
- `POST /api/admin/webp/tasks/{id}/cancel`
- `POST /api/admin/mounts/{id}/browse`

实现位置：

- `internal/server/webp_task.go`
- `internal/server/webp_convert.go`

主流程：

1. 校验源目录与输出根目录
2. 扫描源目录下的 ZIP/CBZ
3. 预检查已有输出是否完整，完整则跳过
4. 串行逐压缩包处理
5. 保留原有 WebP 条目，其余图片转为同名 `.webp`
6. 输出到 `<source-folder-name>-webp`

实现约束：

- 默认质量/方法是 `75 / 0`
- 支持持久化默认值
- 如果目标输出目录已存在且非空，会拒绝覆盖
- 列表接口固定返回 1 个运行中任务 + 最近 2 个取消任务，避免任务列表膨胀
- 当前记录里只有 Go/JS 测试通过，没有浏览器层面的手工验证记录

## 4. SQLite 关键表

用户与认证：

- `users`
- `sessions`

挂载与书库：

- `mounts`
- `root_directories`
- `comics`

缓存：

- `cache_entries`

用户阅读状态：

- `reading_progress`
- `favorite_comics`
- `user_reader_settings`

运行时与任务：

- `app_runtime_settings`
- `webp_tasks`
- `webp_task_logs`

设计重点：

- `comics` 用 `(mount_id, path)` 唯一
- 阅读进度/收藏会补充 `comic_file_name_key`，降低扫描重建后关联丢失概率
- `ValidateAndRepairSchema()` 包含较多修复逻辑，不能只看 `CREATE TABLE`

## 5. 前端结构速记

主文件：

- `web/static/app.js`

状态块重点：

- `state.library`
- `state.favorites`
- `state.runtime`
- `state.archiveTask`
- `state.webpConvert`
- `state.reader`

当前 UI 事实：

- 书库、收藏、历史、阅读器、设置、管理页都由一个脚本切视图驱动
- 管理页同时承载：
  - 扫描状态
  - WebP 转换
  - 归档工作流
  - 运行时设置
  - 挂载管理
  - 根目录管理
  - 用户管理

## 6. 容易误判的点

- `README.md` 里仍提到“预取下一页”，但当前代码与历史记录表明前端预取队列已移除；以后若更新说明文档，应以现实现状为准。
- 不要把 `page_prefetch_pages` 和已移除的前端预取混为一谈。现存字段主要影响后端加载/缓存并发策略相关行为。
- 修改 schema 相关逻辑时，必须同时看：
  - `Migrate()`
  - `ValidateAndRepairSchema()`
  - 读写 DAO
  - 前端 DTO / 管理页表单
- 归档和 WebP 转换功能都强依赖“本地路径可达”，不是通用远端任务框架。
