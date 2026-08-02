# ZIP Manga

轻量漫画阅读服务，支持通过 Web 页面访问 ZIP/CBZ/PDF 漫画，适配低内存低 CPU 服务器。

## 功能

- Web 阅读：流式分页读取、懒加载、预取下一页，阅读更顺滑。
- 独立页面：`/` 书库、`/history` 观看历史、`/reader/{id}` 阅读页、`/admin` 管理页。
- 多挂载源：支持 `local`、`smb`、`webdav`。
- 账户系统：登录鉴权、Session Cookie、多用户、管理员权限。
- 阅读进度：自动记录上次观看页，支持续读与历史列表。
- 收藏与历史：按文件名持久化，扫描重建后可继续匹配。
- 阅读器可配置：支持单页翻页/纵向滚动、当前页定位（滑条+跳页）、画质增强（清晰增强/细节增强/锐化）。
- 封面预缓存：扫描时生成封面缩略图，自动缓存并按条目数清理，默认最多保留 5000 部漫画的封面。
- 双协议：可同时启用 HTTP 和 HTTPS。
- 存储：SQLite（WAL 模式，低开销）。
- 可执行：支持构建 `linux/arm64` 的静态二进制，可直接在 `musl` 环境运行。
- 运维命令：提供显式的数据库修复命令，避免把重型修复放进启动链路。

## 目录结构

- `/cmd/zipmanga`：程序入口
- `/internal`：后端核心逻辑
- `/web`：前端页面与静态资源
- `/build/Dockerfile.musl`：`linux/arm64` 构建模板
- `/build-musl.sh`：`linux/arm64` musl 兼容打包脚本

## 快速开始

1. 直接运行（首次自动生成配置和数据库目录）：

```bash
./zipmanga
```

首次启动会在当前执行目录自动创建：

- `./config.json`（完整默认配置）
- `./data/zipmanga.sqlite`（SQLite 数据库）
- `./cache/`（封面缓存目录）
- Web 界面资源已内嵌在二进制中（无需额外 `web/` 目录）

2. 也可手动准备配置：

```bash
cp config.example.json config.json
```

3. 修改 `config.json`（至少修改管理员密码）。

4. 启动服务（本地 Go 环境）：

```bash
go run ./cmd/zipmanga -config ./config.json -web-dir ./web
```

生产环境推荐显式使用内嵌前端资源，避免部署目录里残留旧 `web/` 覆盖新前端：

```bash
./zipmanga --web-dir= --config ./config.json
```

5. 浏览器访问：

- HTTP：`http://127.0.0.1:8080`
- HTTPS：按配置启用后访问 `https://127.0.0.1:8443`

默认初始管理员：`bootstrap_admin` 中配置的账号。

如需手动修复旧数据库结构，执行：

```bash
./zipmanga db repair --config ./config.json
```

## musl 兼容二进制构建（不使用 Docker）

需要本机安装 Go（macOS 可用 `brew install go`）：

```bash
./build-musl.sh
```

默认输出文件：

- `./dist/zipmanga-linux-arm64-musl`
- `./dist/zipmanga-linux-arm64-musl.buildinfo`
- `./dist/zipmanga-linux-arm64-musl.run-embedded.sh`

这是 Linux/arm64 静态二进制文件（`CGO_ENABLED=0`），可直接拷贝到 `musl` 环境（如 Alpine Linux、OpenWrt/musl 发行版）运行。

可选参数：

```bash
TARGET_ARCH=arm64 ./build-musl.sh
OUTPUT_NAME=zipmanga-linux-arm64-musl ./build-musl.sh
```

脚本默认使用：

- `GOPROXY=https://goproxy.cn,direct`
- `GOSUMDB=sum.golang.google.cn`

可按需覆盖：

```bash
GOPROXY=https://proxy.golang.org,direct ./build-musl.sh
```

推荐部署时直接使用构建脚本生成的嵌入式启动器：

```bash
./dist/zipmanga-linux-arm64-musl.run-embedded.sh --config ./config.json
```

构建脚本会自动：

- 以只读模式解析依赖，不改写 `go.mod/go.sum`
- 对参与编译的源码输入和 Web 资源计算哈希
- 将哈希写入二进制并在构建完成后验证
- 如果构建过程中源码发生变化，直接失败，拒绝输出不一致产物

## 挂载配置说明

挂载通过管理页面创建：

- `local`：本地目录（如 `/data/manga`）
- `smb`：填写 `smb_host/smb_share` 等信息
- `webdav`：填写 `webdav_url` 与账号密码

创建挂载后，管理员点击“全量扫描”，系统会索引 ZIP/CBZ/PDF 并预生成封面缓存。

## 性能策略

- 标准库 HTTP + 轻依赖，降低常驻内存。
- SQLite 单连接 WAL，减小锁竞争和磁盘写抖动。
- 封面缓存采用条目数阈值，超过后按最早入库的封面依次清理。
- 阅读页按需加载，不预解压全书，减少内存峰值。
- WebDAV 读取使用范围请求块缓存，避免整包下载。

## 配置示例

参考：`/config.example.json`

关键项：

- `server.enable_http` / `server.enable_https`
- `server.tls_cert` / `server.tls_key`
- `database.path`
- `cache.cover_max_entries`
- `scan.interval`

## 维护命令

- `./zipmanga db repair --config ./config.json`：手动执行数据库结构校验与修复
- `./zipmanga version`：输出当前二进制的构建时间、提交信息、源码输入哈希和前端资源哈希
