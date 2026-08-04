# 代码定位速查

最后更新：2026-08-04

## 1. 按需求找入口

### 服务起不来 / 配置不生效

- `cmd/zipmanga/main.go`
- `internal/app/app.go`
- `internal/config/config.go`
- `internal/db/store.go`

先看：

- `app.New()`
- `config.Load()`
- `store.GetRuntimeSettings()`

### 登录、密码、管理员、Session

- `internal/auth/service.go`
- `internal/server/server.go`

关键入口：

- `handleAPILogin`
- `handleAPILogout`
- `handleAPIUpdateMyPassword`
- `handleAPICreateUser`
- `handleAPIUpdateUserPassword`

### 挂载新增、编辑、浏览失败

- `internal/server/server.go`
- `internal/mounts/manager.go`
- `internal/mounts/provider.go`
- `internal/mounts/local.go`
- `internal/mounts/smb.go`
- `internal/mounts/webdav.go`
- `internal/mounts/multi_root.go`

关键入口：

- `handleAPIListMounts`
- `handleAPICreateMount`
- `handleAPIUpdateMount`
- `handleAPIBrowseMountDirs`
- `handleAPIAdminBrowseStoredMountDirs`

### 扫描不到漫画 / 扫描过慢 / 周期扫描不符合预期

- `internal/scanner/service.go`
- `internal/comicfmt/format.go`
- `internal/comicutil/probe.go`
- `internal/db/store.go`

关键入口：

- `ScanAll`
- `ScanChanged`
- `ScanMount`
- `scanMountWithFiles`
- `mountSnapshotChanged`
- `ReplaceComicsForMount`

### 书库列表 / 搜索 / 收藏 / 历史不对

- 前端：`web/static/app.js`
- 后端：`internal/server/server.go`
- DAO：`internal/db/store.go`

关键入口：

- `handleAPIListComics`
- `handleAPIHistory`
- `handleAPIAddFavorite`
- `handleAPIRemoveFavorite`
- `ListComics...`
- `ListReadingHistory...`

### 阅读页翻页 / 图片加载 / 清晰度 / PDF 行为

- `internal/server/server.go`
- `internal/cache/page_cache.go`
- `internal/cache/cover_cache.go`
- `internal/imgenhance/enhance.go`
- `internal/pdfutil/*`
- `web/static/app.js`

关键入口：

- `handleAPIComicDetail`
- `handleAPIComicCover`
- `handleAPIComicPage`
- `handleAPIGetProgress`
- `handleAPIUpdateProgress`

重点记忆：

- 阅读链路是单页请求
- `enhance=off` 与宽度参数的行为要看具体分支，不要先入为主

### 运行时设置不生效 / 管理页参数不一致

- `internal/server/server.go`
- `internal/config/config.go`
- `internal/db/store.go`
- `web/static/app.js`

关键入口：

- `handleAPIGetRuntimeSettings`
- `handleAPIUpdateRuntimeSettings`
- `GetRuntimeSettings`
- `UpsertRuntimeSettings`

检查点：

- 配置默认值
- DB 覆盖逻辑
- API DTO
- 管理页表单字段

### WebP 离线转换

- `internal/server/webp_task.go`
- `internal/server/webp_convert.go`
- `internal/server/archive_task.go`
- `internal/imgcodec/lossy_webp.go`
- `internal/ziputil/zip.go`

关键入口：

- `handleAPIAdminCreateWebPTask`
- `handleAPIAdminGetWebPTask`
- `handleAPIAdminCancelWebPTask`
- `executeWebPTask`
- `scanWebPArchiveJobs`

重点记忆：

- 只支持本地目录
- 输出目录是 `<source-folder>-webp`
- 已有完整输出会跳过

### 归档整理 / 批量改名

- `internal/server/archive_task.go`
- `internal/server/archive_workflow.go`
- `internal/server/name_format.go`

关键入口：

- `handleAPIAdminArchiveWorkflowCreateTask`
- `runArchiveWorkflowOnRoot`
- `runArchiveCollect`
- `runArchiveExtract`
- `runArchiveOrganize`
- `runArchiveRepack`
- `handleAPIAdminNameFormatPreview`
- `handleAPIAdminNameFormatApply`

## 2. 按表找代码

### `comics`

- schema / 修复：`internal/db/store.go`
- 扫描写入：`ReplaceComicsForMount`
- 阅读读取：`GetComicByID` 相关查询、`handleAPIComicDetail`

### `reading_progress`

- schema / 修复：`internal/db/store.go`
- 读写接口：`GetReadingProgress` / `UpsertReadingProgress`
- API：`handleAPIGetProgress` / `handleAPIUpdateProgress`

### `favorite_comics`

- schema / 修复：`internal/db/store.go`
- 读写接口：收藏相关 DAO
- API：`handleAPIAddFavorite` / `handleAPIRemoveFavorite`

### `app_runtime_settings`

- schema / 修复：`internal/db/store.go`
- 启动覆盖：`internal/app/app.go`
- 管理端：`handleAPIGetRuntimeSettings` / `handleAPIUpdateRuntimeSettings`

### `webp_tasks` / `webp_task_logs`

- schema：`internal/db/store.go`
- 任务运行：`internal/server/webp_task.go`
- 转换实现：`internal/server/webp_convert.go`

## 3. 改动时的检查顺序

### 后端接口改动

1. 路由注册是否变
2. handler 请求/响应 DTO 是否变
3. store 层字段是否覆盖
4. `web/static/app.js` 是否同步
5. 测试是否需要补

### 扫描或缓存改动

1. provider 是否都覆盖
2. scanner 全量/增量分支是否都覆盖
3. `cache_entries` 写入/淘汰是否一致
4. 远端挂载成本是否被放大

### schema 改动

1. `Migrate()`
2. `ValidateAndRepairSchema()`
3. 旧数据回填路径
4. 读写 DAO
5. 前后端字段暴露面

## 4. 当前已知文档债

- `README.md` 的功能描述与当前实现有部分历史残留，尤其是“前端预取”表述，后续如要对外说明，建议先按代码现状校正。
- 当前 `doc/` 已被 `.gitignore` 忽略，适合本地维护知识，不会自动进入版本库。
