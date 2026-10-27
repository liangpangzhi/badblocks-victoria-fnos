# 飞牛 fnOS iframe 型应用「网关入口规范」只读采集报告

采集对象：192.168.5.12（fnOS），用户 liang，全程只读（sudo 仅用于读取），未改动任何文件。
采集时间：2026-10-27

---

## 一、实测样本：ui/config 全文

### 1. fpk-extractor（/vol1/@appcenter/fpk-extractor/ui/config）
```json
{
    ".url": {
        "fpk-extractor.main": {
            "title": "应用提取器",
            "desc": "扫描已安装应用并还原为离线 .fpk",
            "icon": "images/{0}.png",
            "type": "iframe",
            "protocol": "",
            "gatewayPrefix": "/app/fpk-extractor",
            "gatewaySocket": "app.sock",
            "url": "/app/fpk-extractor",
            "allUsers": true
        }
    }
}
```

### 2. 100zip（/vol1/@appcenter/100zip/ui/config，摘要）
```json
"100zip.main": {
  "title": "100解压",
  "icon": "images/icon_{0}.png",
  "type": "iframe",
  "protocol": "",
  "gatewayPrefix": "/app/100zip",
  "gatewaySocket": "app.sock",
  "url": "/app/100zip/",
  "allUsers": true
}
```
（同文件另有 "100zip.open"，共用同一 gatewayPrefix / gatewaySocket）

### 3. moo（/vol1/@appcenter/moo/ui/config，全文）
```json
{
    ".url": {
        "moo.Application": {
            "title": "Moo",
            "desc": "飞牛第三方应用中心",
            "icon": "images/ICON.PNG",
            "type": "iframe",
            "gatewaySocket": "app.sock",
            "gatewayPrefix": "/app/moo",
            "url": "/app/moo/",
            "allUsers": false
        }
    }
}
```

### 4. fnclearup（/vol1/@appcenter/fnclearup/ui/config，全文）
```json
{
    ".url": {
        "fnclearup.Application": {
            "title": "清理精灵",
            "icon": "images/icon_{0}.png",
            "type": "iframe",
            "url": "/app/fnclearup",
            "gatewaySocket": "fnclearup.sock",
            "gatewayPrefix": "/app/fnclearup",
            "allUsers": true,
            "control": {"accessPerm": "editable"}
        }
    }
}
```

结论：`/vol1/@appcenter/*/ui/config` 中共 9 个 `type:"iframe"` 应用，字段集高度一致，仅 `url` 是否带尾斜杠、`gatewaySocket` 文件名不同。

---

## 二、字段含义与取值（实测 + 系统侧证据）

| 字段 | 是否必需 | 含义 | 实测取值 |
|---|---|---|---|
| `type` | 必需 | 桌面入口类型，`iframe` = 桌面内嵌窗口（不新开标签） | 固定 `"iframe"` |
| `gatewayPrefix` | iframe 必需 | 统一网关的路由前缀，nginx 把 `/app/<app>` 的请求交给 trim_http_cgi 后，由它按此前缀匹配应用 | `/app/<appname>` |
| `gatewaySocket` | iframe 必需 | **目标 socket 的文件名（相对应用的 target 目录）**，只能是 unix socket 文件名，不能是 URL | `app.sock`、`fnclearup.sock`、`fndepot.sock` |
| `url` | 必需 | iframe 的 src。桌面前端拼接为 `${protocol}://${host}${url}` | `/app/<appname>`（尾斜杠可选） |
| `protocol` | 可选 | 留空 = 继承桌面页面协议（https 桌面即 https）；写死 http 会在 https 下被混合内容拦截 | `""` |
| `allUsers` | 可选 | 是否所有用户可见 | true/false |
| `gatewayUrl` / `gatewayPath` | **不存在** | fnOS 实测所有 iframe 应用及 appcenter 库 `app_service` 表均无这两个字段，属外部文档误传。等价信息由 `gatewayPrefix` + `url` 承担 | — |

### 关键：安装器把 `gatewaySocket` 落库为绝对路径
appcenter 库 `app_service` 表实测（join `app`）：

```
app_name            | service_name          | gateway_socket                          | gateway_prefix
moo                 | moo.Application       | /var/apps/moo/target/app.sock           | /app/moo
100zip              | 100zip.main           | /var/apps/100zip/target/app.sock        | /app/100zip
fpk-extractor       | fpk-extractor.main    | /var/apps/fpk-extractor/target/app.sock | /app/fpk-extractor
fnclearup           | fnclearup.Application | /var/apps/fnclearup/target/fnclearup.sock | /app/fnclearup
fndepot             | fndepot.Application   | /var/apps/fndepot/target/fndepot.sock   | /app/fndepot
treatstarsfnetstats | treatstarsfnetstats.main | /var/apps/treatstarsfnetstats/target/app.sock | /app/treatstarsfnetstats
all.editor          | all.editor            | （空）                                   | （空）
tailscale           | tailscale.Application | （空）                                   | （空）
```

- 写入形式：`/var/apps/<appname>/target/<gatewaySocket值>`
- `/var/apps/<appname>/target` 是软链 → `/vol1/@appcenter/<appname>`
- 因此**物理 socket 路径 = `/vol1/@appcenter/<appname>/<文件名>.sock`**
- 没有 gatewaySocket 的应用（all.editor、tailscale）走 `/cgi/ThirdParty/<app>/index.cgi`，即 CGI 型，不是 unix socket

### 是否可填 http://127.0.0.1:<port>？
**不可以。** 全部 9 个 iframe 应用样本 + `app_service` 表全部非空记录，`gateway_socket` 一律为 unix socket 路径，无任何 URL 形式反例；`trim_http_cgi` 以 `http://unix:<path>:` 方式转发。需要 TCP 端口的应用只能走 `/cgi/ThirdParty/*.cgi` 的 CGI 形态或直接 `type:"url"`（后者 https 下会被混合内容拦截）。

---

## 三、fpk-extractor 的 socket 文件名 / 权限 / 创建者

| 项目 | 实测结果 |
|---|---|
| 文件名 | `app.sock`（来源：`ui/config` 的 `gatewaySocket`） |
| 物理路径 | `/vol1/@appcenter/fpk-extractor/app.sock` |
| 权限 | `srw-rw-rw- root root`（**0666**，world 可读写） |
| 创建者 | 应用自身服务进程 `python3`（pid 6810，`run-as: root`），不是 nginx、也不是安装器 |
| 创建方式 | `/var/apps/fpk-extractor/cmd/main` 启动前 `rm -f $TARGET/app.sock` 清理残留，随后 `server.py` 在 `TARGET/app.sock` 上 `bind`，并显式 `os.chmod(SOCK_PATH, 0o666)`（源码注释：nginx 转发进程需要能连上） |

对比其他应用的 socket 权限：
```
srw-rw-rw- 1 root    root     /vol1/@appcenter/fpk-extractor/app.sock
srw-rw-rw- 1 fndepot fndepot /vol1/@appcenter/fndepot/fndepot.sock
srwx------ 1 root    root     /vol1/@appcenter/moo/app.sock
```
→ 0666 是通行做法；0600（moo）在 trim_http_cgi 以 root 身份连接时也能工作。**给 0666 最稳**。

应用内 `config/` 与 socket 无关：
- `config/privilege`：`{"defaults": {"run-as": "root"}}` ← 服务以 root 运行（因此 socket 属主为 root）
- `config/resource`：仅声明 `data-share` 共享目录 `fpk-extractor`

服务为**双栈监听**（`ss` 实测）：
```
u_str LISTEN /vol1/@appcenter/fpk-extractor/app.sock  users:(("python3",pid=6810))
tcp   LISTEN 0.0.0.0:8520                             users:(("python3",pid=6810))
```

---

## 四、系统 nginx 的 /app/ 处理与统一网关反代

主配置 `/usr/trim/nginx/conf/nginx.conf`（master: `/usr/trim/nginx/sbin/nginx`），第 134 行 `include conf.d/*.conf;`

规则文件 `/usr/trim/nginx/conf/conf.d/trim_http_cgi.conf` **全文**：
```nginx
location /cgi {
	proxy_pass http://unix:/var/run/trim_http_cgi.socket:;
}

location /app/ {
    proxy_pass http://unix:/var/run/trim_http_cgi.socket:;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header Host $host; 
    proxy_read_timeout 300s;
}
```

要点：
1. `/app/` 是**通配 location**，不区分具体应用 → **新增应用无需修改任何 nginx 配置**。
2. 请求进入 `trim_http_cgi`（`/usr/trim/bin/trim_http_cgi`，pid 4354，监听 `/var/run/trim_http_cgi.socket`，属主 `root:www-data`，`srwxrwx---`），由它按 `app_service.gateway_prefix` 匹配、按 `gateway_socket` 连接应用 unix socket。
3. **路径前缀不剥离**：应用收到的仍是 `/app/<appname>/...`。fpk-extractor 源码注释与实现确认：
   ```python
   # 飞牛在 https 域名下会把 /app/<appname> 的请求转发到 target/app.sock，
   # 且【保留路径前缀】。所以同一个 Handler 必须能同时吃两种前缀。
   GATEWAY_PREFIX = "/app/" + SELF
   def split_base(path):  # 命中前缀则剥离给内部路由，否则按直连 "/" 处理
   ```
4. 支持 WebSocket（Upgrade/Connection 头透传），长请求读超时 300s。
5. 鉴权在网关层：未登录直接请求 `http://127.0.0.1/app/fpk-extractor/` 返回
   `302 → Location: http://127.0.0.1:5666/app/fpk-extractor/`（跳登录），`/app/moo/` 同样 302，而直连 `http://127.0.0.1:8520/api/apps` 返回 200。说明**应用自身不需要做登录鉴权**。

### unix socket 路径约定（结论）
- nginx → trim_http_cgi：`/var/run/trim_http_cgi.socket`（系统固定，与应用无关）
- trim_http_cgi → 应用：`/var/apps/<appname>/target/<sock名>`，即 `/vol1/@appcenter/<appname>/<sock名>`
- **不是** `/var/apps/<appname>/<sock名>`（该层需经 target 软链）

---

## 五、结论：Go 服务被 https 桌面 iframe 内嵌的最小改动

### 必须做（3 项）
1. **监听 unix socket**：`/vol1/@appcenter/<appname>/app.sock`（= `/var/apps/<appname>/target/app.sock`），启动时 `os.Remove` 残留后 `net.Listen("unix", path)`，并 `os.Chmod(path, 0666)`。
2. **路由兼容 `/app/<appname>` 前缀**：用 `http.StripPrefix("/app/<appname>", mux)` 同时挂载 `mux` 于 `/`，两种访问方式共用一套 handler（前端资源、API 全走相对路径）。
3. **ui/config 写 iframe 四件套**：
```json
{
  ".url": {
    "<appname>.main": {
      "title": "硬盘坏道修复",
      "icon": "images/icon_{0}.png",
      "type": "iframe",
      "protocol": "",
      "gatewayPrefix": "/app/<appname>",
      "gatewaySocket": "app.sock",
      "url": "/app/<appname>/",
      "allUsers": true
    }
  }
}
```
`manifest` 需同步：`desktop_uidir = ui`、`desktop_applaunchname = <appname>.main`、`service_port = <端口>`、`checkport = true`。

### 建议保留（1 项）
- **同时保留 TCP 监听**（如 `0.0.0.0:<service_port>`）：`manifest` 的 `checkport = true` 会探测该端口判定应用在线状态；无 TCP 监听可能导致桌面图标被判离线。调试也依赖它。

### 无需改动
- 系统 nginx 配置（`location /app/` 已通配）
- 应用鉴权（网关层已处理，302 到登录页）
- `config/privilege`：`run-as: root` 决定 socket 属主；若以普通用户运行，socket 属主变化但 0666 仍可被网关连接

### 与当前 badblocks-victoria（type:"url" + http 端口 24046）的差异
| 项 | 现状 | 目标 |
|---|---|---|
| ui/config type | `url` | `iframe` |
| 入口地址 | `src=http://<host>:24046/`，https 桌面下混合内容被拦截 | `src=/app/<appname>/`，同源，无混合内容 |
| 传输 | TCP 24046 直连 | unix socket 经 trim_http_cgi 网关 |
| nginx | 无规则 | 复用 `/app/` 通配规则 |
| Go 代码改动 | — | 增加 unix listener + StripPrefix + chmod 0666 |
