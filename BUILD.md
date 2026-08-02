# 构建指南

## 最简单的方法（推荐）

直接复制粘贴以下命令，逐个执行：

```bash
# 1. 下载模块
go mod download

# 2. 为 macOS 构建（如果你是在 Mac 上运行）
go build -o zipmanga ./cmd/zipmanga

# 或者构建 linux/arm64 静态二进制
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o dist/zipmanga-linux-arm64 ./cmd/zipmanga
```

## 使用新的简单构建脚本

构建脚本现在会强制校验“当前源码是否真正打进产物”：

```bash
# 为 linux/arm64 构建
bash build-simple.sh

# 为 linux/arm64 musl 兼容二进制构建
bash build-musl.sh

# 为 macOS 构建
TARGET_OS=darwin TARGET_ARCH=arm64 bash build-simple.sh
```

构建完成后会生成三类文件：

- `dist/<name>`：二进制
- `dist/<name>.buildinfo`：本次构建的源码输入哈希、前端资源哈希、构建时间和提交信息
- `dist/<name>.run-embedded.sh`：强制使用二进制内嵌前端资源的启动脚本

脚本包含以下保护：

- 使用 `-mod=readonly`，不再在构建阶段改写 `go.mod` / `go.sum`
- 构建前后重新计算源码输入哈希，源码在构建过程中变化会直接失败
- 将源码输入哈希和前端资源哈希写入二进制，并在构建完成后校验
- 输出推荐的嵌入式启动方式，避免部署目录残留旧 `web/` 覆盖新前端

## 如果仍然遇到问题

### 检查当前目录是否正确

```bash
pwd
ls -la
```

### 使用绝对路径

如果相对路径有问题，尝试使用绝对路径：

```bash
# 假设你的项目在 /Users/xumaojie/IdeaProjects/manga
cd /Users/xumaojie/IdeaProjects/manga
go build -o zipmanga ./cmd/zipmanga
```

### 在新的终端窗口中尝试

有时当前终端会话可能有问题，在新的终端窗口中重新尝试：

1. 打开新的终端窗口
2. cd 到项目目录
3. 执行构建命令

## 我们已完成的优化

### 1. 只读依赖解析
- 构建时只下载和校验依赖，不再自动执行 `go mod tidy`
- 避免构建脚本在发布阶段修改源码树

### 2. 源码指纹校验
- 对参与编译的 Go 源码和内嵌 Web 资源计算哈希
- 把哈希写入二进制，并在产物生成后反向验证

### 3. 嵌入式前端启动
- 自动生成 `.run-embedded.sh`
- 部署时优先使用该启动器，彻底规避旧 `web/` 目录覆盖内嵌前端的问题

## 验证构建

构建成功后，你可以运行以下命令来验证：

```bash
# 查看版本和构建指纹
./zipmanga version

# 查看帮助信息
./zipmanga --help

# 手动修复数据库
./zipmanga db repair --config ./config.json
```

## 常见问题

### 问题1：getcwd 权限错误
如果你看到 `shell-init: error retrieving current directory: getcwd: cannot access parent directories: Operation not permitted`，这可能是：
- 目录权限问题
- 终端沙箱环境限制

**解决方案**：尝试在新的终端窗口中重新打开项目目录，或者使用绝对路径执行命令。

### 问题2：找不到 go 命令
确保 Go 已正确安装并添加到 PATH 环境变量中：

```bash
# macOS 上安装 Go
brew install go

# 验证安装
go version
```
