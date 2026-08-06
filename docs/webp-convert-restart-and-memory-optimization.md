# WebP 转换重启恢复与内存优化

最后更新：2026-08-06

## 背景

WebP 离线转换在应用重启后会重新大范围逐包重扫，导致长时间卡在恢复阶段；同时单个压缩包转换链路还存在 `workDir -> finalZip` 二次落盘，以及图片读取阶段的额外内存峰值。

## 现状链路

1. 管理端创建 WebP 任务后进入 `internal/server/webp_task.go`
2. 实际压缩包转换由 `internal/server/webp_convert.go` 执行
3. 旧恢复逻辑会在 `buildWebPTaskDiskSnapshot()` 中再次打开源压缩包，重扫条目并核对输出 zip / workDir
4. 旧转换逻辑会先落到 `*.work` 目录，再重新打包成最终 zip
5. 非 WebP 图片旧路径会先 `ReadAll` 整张图，再 `Decode`

## 关键证据

- 新增完成清单表：`internal/db/store.go` / `internal/db/models.go`
  - `webp_archive_manifests`
  - 记录源包路径、目标 zip、质量参数、源包 mtime/size、图片条目数、转换/保留数量、输出大小
- 运行完成后立即写 manifest：`convertArchiveJobToWebP()`
- 重启恢复改为优先匹配 manifest：`Server.buildWebPTaskDiskSnapshot()`
- 输出 zip 现在直接从源压缩包写到 `finalZip.tmp`，成功后原子重命名：`writeZipArchiveToWebPOutput()` / `writeRARArchiveToWebPOutput()`
- 非 WebP 图片改为直接从 `io.Reader` 解码：`internal/imgcodec/lossy_webp.go`

## 数据/状态流

- 单包完成：
  - 源压缩包扫描条目
  - 直接写入 `finalZip.tmp`
  - 写入成功后 `rename -> finalZip`
  - `UpsertWebPArchiveManifest(...)`
- 任务收尾：
  - 无论成功、失败还是取消，都会重新枚举当前任务命中的压缩包
  - 统一执行 `os.RemoveAll(<finalZip>.work)` 清理遗留中间目录
- 重启恢复：
  - 只重新枚举候选压缩包路径
  - `stat(source)` + `stat(finalZip)` + 查询 `webp_archive_manifests`
  - mtime / size / 参数一致则直接判定为已完成
  - 有输出 zip 但 manifest 不匹配时，保守记为 `partial`

## 结论

这次改造把恢复判定从“逐包逐图核验”收敛成“路径枚举 + manifest 校验”，重启后不会再为每个压缩包重新扫全部图片；转换产物也不再经过 `workDir -> finalZip` 的二次磁盘中转。单图处理上，至少去掉了源图 `ReadAll + bytes.NewReader + Decode` 这一段重复内存占用。同时，任务结束后会统一清掉残留 `.work` 目录，避免成功/失败后留下中间垃圾目录。

## 风险

- manifest 目前以 `source_path + output_zip_path + quality + method` 为唯一键；如果未来还引入更多影响输出的编码参数，需要一并纳入匹配条件。
- 对于“存在输出 zip 但 manifest 缺失/失配”的历史任务，恢复阶段会保守标记为部分完成，并在下次执行时整包重做。
- ZIP 源包仍会在并发转换阶段为编码结果保留 `[]byte`，这已经避开源图整包读入，但最终编码结果本身仍需暂留到按序写入。

## 待确认

- 是否需要后续增加 manifest 清理策略，避免历史完成记录长期累计。
- 是否继续把 WebP 原图透传也改成纯流式写入，以进一步压低大图内存峰值。

## 改动点清单与影响面

- 后端转换链路：
  - `internal/server/webp_convert.go`
  - 影响压缩包写出方式、完成判定、`.work` 清理和重启后恢复耗时
- 任务恢复链路：
  - `internal/server/webp_task.go`
  - 影响运行中 / 异常退出后的快照刷新逻辑与任务结束收尾清理
- 持久化层：
  - `internal/db/models.go`
  - `internal/db/store.go`
  - 新增 `webp_archive_manifests` 表与读写方法
- 图片编码：
  - `internal/imgcodec/lossy_webp.go`
  - 影响非 WebP 图片的解码入口
- 测试：
  - `internal/db/store_test.go`
  - `internal/server/webp_task_test.go`
  - 覆盖 manifest 持久化、基于 manifest 的恢复快照和 `.work` 目录清理
