# ZIP Manga 文档入口

最后更新：2026-08-04

## 1. 这个目录的用途

这里不写宣传性说明，只记录下次继续维护这个仓库时最需要的内容：

- 项目主结构和职责分层
- 核心业务链路与代码入口
- SQLite 关键表与运行时持久化点
- 管理端任务能力与边界
- 当前实现里容易误判的约束

配套文档：

- `doc/project-overview.md`：项目总览、启动链路、模块职责、数据流
- `doc/code-map.md`：按需求类型快速定位代码文件与入口

## 2. 一句话理解项目

这是一个单体 Go Web 漫画服务，核心目标是让 ZIP/CBZ/PDF 漫画在低资源环境下可浏览、可阅读、可持久化用户进度，并提供本地/SMB/WebDAV 挂载、扫描建库、封面/页面缓存、管理员批处理（归档整理、命名规范化、离线 WebP 转换）能力。

## 3. 启动链路

程序入口：

- `cmd/zipmanga/main.go`

主装配流程：

1. 读取或生成 `config.json`
2. 打开 SQLite，执行 `Migrate + ValidateAndRepairSchema`
3. 从数据库加载运行时设置，覆盖配置文件中的运行时项
4. 初始化认证、挂载管理器、封面缓存、页面缓存、扫描器、HTTP Server
5. 启动后台任务：
   - Session 清理
   - 缓存回收
   - 周期性增量扫描
6. 启动 HTTP/HTTPS 服务

真正的装配代码在：

- `internal/app/app.go`
- `internal/server/server.go`

## 4. 当前最重要的实现约束

这些点下次最容易看错：

- 阅读接口主链路是单页按需加载，不是整本批量预渲染。
- 前端图片预取逻辑已经移除，旧的 `frontend_page_prefetch_pages` 只作为数据库兼容字段保留，不应再恢复成公开配置。
- 图片增强模式的公开契约是 `off / level1 / level2`，前后端都围绕这三个值工作，旧值只做兼容归一化。
- 周期扫描走 `ScanChanged`，不是无脑全量扫描；会基于挂载快照判断是否变化。
- WebP 离线转换只支持本地挂载目录链路，输出目录策略是 `<source-folder-name>-webp`，默认质量/方法是 `75/0`。

## 5. 下次改动前建议顺序

1. 先看 `doc/project-overview.md`
2. 再按需求类别查 `doc/code-map.md`
3. 再从对应入口往下追调用链，不要靠文件名猜行为
