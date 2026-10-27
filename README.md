# badblocks-victoria-fnos

飞牛 fnOS（fnOS / Debian 12）平台的硬盘坏道检测与修复应用，Victoria 风格 Web UI，支持桌面图标内嵌运行。

- 当前版本：**0.3.4**
- 技术栈：Go（单二进制，CGO_ENABLED=0） + 原生 JS 前端
- 安装包：见 Releases 中的 `badblocks-victoria-0.3.4.fpk`

## 功能

| 能力 | 说明 |
|---|---|
| 磁盘清单 | 汇总物理盘型号、序列号、容量、转速类型、传输总线、SMART 状态、挂载点、holders（LVM / md / ZFS） |
| 挂载判定 | 物理盘本身无挂载点时，向上追溯分区 / md / LVM / ZFS 上层的实际挂载结果（`/proc/mounts` + `zpool status -P`），避免"已挂载盘被误判为未挂载" |
| 坏道扫描 | `badblocks` 只读扫描（`-s`），输出进度与坏块区间 |
| 坏道重映射 remap | `badblocks -n` 非破坏性写入重映射，**仅允许未挂载的机械盘**，需显式 confirm 盘符，默认 dry-run |
| Btrfs scrub | 对 btrfs 卷做数据校验修复（`btrfs scrub start`） |
| 复扫 recheck | 修复后只读复扫，对比坏块变化 |

> 物理坏道不可"修复"，本应用只提供重映射（remap）、文件系统校验（scrub）与复扫（recheck）三类动作，任何写入类操作都要求二次确认，且默认以 dry-run 展示计划。

## 安装

### 方式一：App Center 安装包

下载 Releases 中的 `.fpk`，在飞牛 App Center 手动安装。

### 方式二：CLI（推荐，适配自建 fpk）

```bash
# 解包后安装（install-fpk 对本地自建包可能报 code 10111，改用 install-local）
mkdir -p /tmp/bbv && tar -xzf badblocks-victoria-0.3.4.fpk -C /tmp/bbv
sudo appcenter-cli install-local -d /tmp/bbv
```

升级同理，用 `install-local` 覆盖安装即可，无需先卸载。

### 桌面入口

应用以 `iframe` 型入口注册，经系统网关 `/app/badblocks-victoria` 反代到进程内的 `app.sock`：

- `type: "iframe"`，`gatewayPrefix: "/app/badblocks-victoria"`，`gatewaySocket: "app.sock"`
- 服务端监听 Unix socket（权限 0666），并对带 `/app/<app>` 前缀的请求做 StripPrefix
- 前端所有 `fetch` 通过 `API_PREFIX` 拼接网关前缀，兼容内嵌与独立窗口两种打开方式

## 目录结构

```
manifest              # fnOS 应用清单（版本、入口、权限声明）
src/                  # Go 源码：主服务、磁盘采集、修复任务
ui/                   # 前端源码（index.html / app.js / styles.css）
app/                  # 打包产物骨架：二进制 + 前端静态文件 + 入口 config
cmd/                  # 安装/卸载/升级回调脚本，main 启动脚本
config/               # privilege / resource 声明
scripts/              # 交叉编译脚本（Linux / Windows）
tools/fpkpack/        # fpk 打包工具源码
docs/                 # 版本说明与技术报告
```

## 构建

```bash
# 交叉编译（本机 Windows/Linux 均可）
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o app/bin/fn-badblocks-victoria ./src

# 打包 fpk
fnpack build            # 或使用 tools/fpkpack 生成的 fpkpack
```

## 版本

- **0.3.4** — 修复物理盘挂载状态误判（分区 / md / LVM / ZFS 上层追溯）；桌面入口改为 iframe 型走系统网关
- 0.3.3 — 网关前缀适配（API_PREFIX），支持独立窗口打开
- 0.3.2 — 初版：磁盘清单 + badblocks 扫描 + remap / scrub / recheck

## 致谢

基于 GitHub 项目 [enenen342/badblocks-victoria-fpk](https://github.com/enenen342/badblocks-victoria-fpk) 二次开发，针对飞牛 fnOS 的打包规范、桌面网关与挂载判定做了适配与修复。
