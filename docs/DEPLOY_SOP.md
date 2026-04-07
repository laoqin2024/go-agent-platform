# Agent 部署与更新指导手册（小白版，V1.2）

本文在保留原有技术参数和专业逻辑的前提下，改写为“普通用户优先”的阅读顺序。  
参考实现文件：
- `deploy/linux/deploy_agent.yml`
- `deploy/windows/deploy_agent.ps1`
- `deploy/windows/install.bat`（根目录也保留了一个用户入口 `install.bat`）
- `internal/service/update_runner.go`
- `templates/go-agent.service.j2`

## 1. 先看这个：普通用户安装（最常用）

### 1.1 Windows 安装（最推荐）

适用：个人办公电脑、Windows 服务器。

#### 第一步：准备文件
- 从公司内网下载 `go-agent-setup.zip`
- 解压到 `D:\GoAgent`（建议不要放桌面，减少误删）

#### 第二步：开始安装（图标要看对）
- 找到 `install.bat`
- **右键点击** -> 选择带**蓝黄色盾牌图标**的 **“以管理员身份运行”**

> 重要说明（避免误解）  
> 当前版本 `install.bat` **需要参数**，不是双击裸跑版本。  
> 推荐让运维给你“已封装好参数的安装包/快捷方式”；如果没有，请按下面命令运行。

```bat
install.bat http://10.x.x.x:8080/ingest your_token device-001
```

#### 第三步：遇到 Windows 安全提示怎么点
- 如果弹出“Windows 已保护你的电脑”
  1. 点击“更多信息”
  2. 再点击“仍要运行”

#### 第四步：如何确认安装成功
- 看到 `Service GoAgent started successfully` 基本就是成功
- 或在 `services.msc` 中确认 `GoAgent/go-agent` 服务为“正在运行”

#### 这版脚本自动帮你做的事
- 非管理员运行会弹窗提醒并退出（提示“请右键点击并选择‘以管理员身份运行’”）
- 安装前自动检测网络连通（`ping` 服务器）
- 安装失败自动生成 `install_error.txt`（就在当前目录），可直接发给运维

### 1.2 Linux 安装（便捷模式）

适用：开发环境、Linux 工作站。

```bash
curl -sSL http://your-server:8080/deploy.sh | sudo bash -s -- -token your_token
```

请把 `your-server` 和 `your_token` 换成公司实际值。

## 2. 如何确认安装成功

### Windows
- 打开 `services.msc`，查看服务状态是否为 Running
- 查看日志：
  - `C:\Program Files\GoAgent\logs\stdout.log`
  - `C:\Program Files\GoAgent\logs\stderr.log`

### Linux
- `systemctl status go-agent`
- `getcap /usr/local/bin/go-agent`，应包含 `cap_net_bind_service`

## 3. 自动更新（日常不用手动管）

- Agent 会每 6 小时自动检查更新（带随机抖动，避免全网同时下载）
- 也支持后台强制触发 `force_update`

### Windows 更新过程（已做防失败处理）
1. 下载 `go-agent.exe.new`
2. 释放 `update.bat` 到临时目录（通常 `C:\Windows\Temp`）
3. `cmd /C start /B` 后台启动 `.bat`
4. Agent 立即 `os.Exit(0)` 释放文件锁
5. `.bat` 执行：停服务 -> 检查进程退出（必要时 `taskkill /F`）-> **`timeout /t 5` 缓冲** -> 替换 -> 启服务 -> 自删除

路径安全性说明：
- `.bat` 中 `%OLD_EXE%`、`%NEW_EXE%` 均有双引号，兼容 `C:\Program Files\...` 空格路径。

## 4. 报错对照表（带“通俗解释”）

### 4.1 通用连接类

| 报错信息 (Log/Console) | 通俗解释 | 真正原因 | 你该怎么做 | 运维怎么做 |
|---|---|---|---|---|
| `dial tcp: i/o timeout` | 公司网络未连通（先看 VPN） | 连不上服务器 | 检查网络/Wi-Fi/VPN | 检查管理端和 8080/8443 端口策略 |
| `websocket: bad handshake` | 通行证失效或不匹配 | 身份校验失败 | 不用自己改，联系运维 | 检查 Token 是否错误或过期 |
| `x509: certificate signed by unknown authority` | 证书不被电脑信任 | 证书链不被信任 | 无需自行处理，联系运维 | 导入根证书或调整证书策略 |

### 4.2 Windows 常见报错

| 报错信息 | 通俗解释 | 真正原因 | 解决方法 |
|---|---|---|---|
| `Access is denied` | 没有“管理员权限” | 权限不足 | 右键脚本 -> 以管理员身份运行 |
| `nssm: Unexpected status SERVICE_PAUSED` | 被安全软件拦住了 | 杀毒/防护拦截 | 将 `go-agent.exe` 加白名单 |
| `The system cannot find the path specified` | 路径有问题 | 路径空格/中文/目录缺失 | 使用标准目录 `C:\Program Files\GoAgent`，避免中文路径 |

### 4.3 更新失败类

| 现象/报错 | 通俗解释 | 真正原因 | 处理方法 |
|---|---|---|---|
| 更新后 Agent 消失 | 更新脚本没跑完整 | `.bat` 流程异常/被拦截 | 重新运行安装脚本，必要时重启后重试 |
| `checksum mismatch` | 下载包损坏了 | 网络抖动或文件被篡改 | Agent 会拒绝替换并重试；通知运维检查下载源 |
| `Access to the path ... is denied` | 文件被占用 | 进程未退出或权限不足 | 结束重复 Agent 进程，再重试 |

## 5. 出问题时，先给运维这些材料

- **自助诊断工具（先跑一次，节省沟通成本）**
  - 项目自带快速自检脚本：`scripts/check_health.sh`
  - 作用：检查服务端 `/healthz`、Redis 连通性、Swagger 与版本接口（可选）

```bash
chmod +x ./scripts/check_health.sh
./scripts/check_health.sh http://127.0.0.1:8080 127.0.0.1:6379
```

- `install_error.txt`（安装失败自动生成）
- `stdout.log` / `stderr.log`
- 你执行的命令（完整复制）
- 报错原文截图

---

## 6. 运维专家附录（保留原技术参数）

### 6.1 部署前置准备 (Pre-flight Check)

#### 服务端准备
- **控制 Token**：后端已配置 `CONTROL_CHANNEL_TOKEN`，并与 `-control-token` 一致。
- **分发中心**：`dist/` 中有对应平台产物：
  - `go-agent-amd64` / `go-agent-arm64`（Linux）
  - `agent-windows-amd64.exe`（Windows）
- **版本接口**：`GET /api/v1/agent/version` 返回版本元数据（`md5`、`sha256`、`download_url`）。

#### 网络准入
- 目标设备可访问管理端 API/WS 端口（按环境策略放通）
- Linux：SSH 可达且可 sudo
- Windows：已执行 `Enable-PSRemoting -Force`

#### 端口白名单（生产环境建议）

| 用途 | 协议 | 端口 | 方向 | 谁访问谁 | 说明 |
|---|---|---:|---|---|---|
| 上报 API / 查询 API / Swagger / Health | HTTP/HTTPS | 8080 | 出站 | Agent/Web → Server | 默认服务端监听 `--addr 0.0.0.0:8080`，包含 `/ingest`、`/api/v1/*`、`/swagger/*`、`/healthz` |
| 控制通道（Control WS） | WS/WSS | 8443 | 出站 | Agent/Web → Server | 推荐生产使用 TLS（WSS）并与上报端口隔离；如未单独部署 8443，可统一复用 8080（需按你的实际 `--addr` 调整） |

### 6.2 服务端 (Server) 部署与维护

#### 6.2.1 环境依赖（必须满足）
- **Redis 7.0+**：用于设备快照存储、资产聚合缓存与 LRU/抑制窗口（告警抑制等）。
- **Go 1.21+**：用于编译服务端与生成 Swagger（如需）。
- **系统建议**：
  - Linux x86_64（推荐）
  - `ulimit -n 65535`（2,000 台规模建议）

#### 6.2.2 服务端安装步骤（推荐：systemd 托管）

**1) 编译服务端**

```bash
make build-server
```

> 产物建议放在服务端机器的 `/usr/local/bin/go-agent-server`（下方 systemd 模板按此路径写）。

**2) Redis 准备**
- 确认 Redis 可用：

```bash
redis-cli -h 127.0.0.1 -p 6379 PING
```

**3) 配置文件（`config.yaml`）**

说明：当前服务端的 `config.yaml` 主要用于 **Agent 版本分发接口**（`GET /api/v1/agent/version`）。  
服务端监听端口与 Redis 连接参数使用 **启动参数**（见下方 systemd `ExecStart`）。

- **`agent_version.latest_version`**：全局最新版本号（示例：`1.0.1`）
- **`agent_version.download_url`**：下载地址（建议指向内网 Nginx/CDN，不要压在 API 进程上）
- **`agent_version.sha256`**：二进制 SHA256（Agent 更新时强校验）
- （可选）**`agent_version.md5`**：兼容字段（用于展示/校验）
- （可选）**`rollout_pct` / `artifacts`**：按 `device_id` 末位分桶的灰度下发（分批更新）

**4) 服务端运行参数（与 Agent 参数对齐）**
- **`CONTROL_CHANNEL_TOKEN`（环境变量）**：控制通道 WS 接入 token（需与 Agent `-control-token` 一致）
- **`CONTROL_COMMAND_TOKEN`（环境变量）**：指令下发载荷 token（服务端下发时写入 payload，Agent 侧校验）
- **`--addr`**：监听地址（示例：`0.0.0.0:8080`）
- **`--redis-addr`**：Redis 地址（示例：`127.0.0.1:6379`）
- **`--config`**：版本分发配置文件路径（示例：`/etc/go-agent/config.yaml`）

**5) systemd 服务模板（包含 Restart=always）**

将以下内容保存为 `/etc/systemd/system/go-agent-server.service`：

```ini
[Unit]
Description=Go Agent Server (Ingest / WS / Control / Version API)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=goagent
Group=goagent
Environment=CONTROL_CHANNEL_TOKEN=please_set_me
Environment=CONTROL_COMMAND_TOKEN=please_set_me
Environment=NOTIFY_WEBHOOK_URL=
ExecStart=/usr/local/bin/go-agent-server \
  --addr 0.0.0.0:8080 \
  --redis-addr 127.0.0.1:6379 \
  --config /etc/go-agent/config.yaml
Restart=always
RestartSec=2
LimitNOFILE=65535
## 生产加固：2,000 台并发连接时，必须提高句柄上限，否则会出现 accept/conn 失败、WS 频繁断开等问题。

[Install]
WantedBy=multi-user.target
```

启动并设置开机自启：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now go-agent-server
sudo systemctl status go-agent-server --no-pager
```

#### 6.2.3 Update Service 配置（给全网自动更新提供“版本元数据 + 下载源”）

**1) 放置 Agent 二进制到静态分发目录（dist/）**

建议做法：在服务端或内网镜像机上准备一个静态目录（例如 `/srv/go-agent/dist/`），并由 Nginx/CDN 对外提供下载：
- Linux amd64：`go-agent-amd64`
- Linux arm64：`go-agent-arm64`
- Windows amd64：`agent-windows-amd64.exe`（或你内部约定的文件名）

> 说明：当前 Go Server **不负责静态文件托管**；`download_url` 通常应指向内网 Nginx/CDN 的静态地址。

**2) 修改 `config.yaml` 触发全网自动更新**

当你要发布新版本（例如 `v1.0.2`）：
- 将新二进制上传到你的静态分发目录（Nginx/CDN）
- 计算新二进制的 `sha256`（必须准确）
- 更新 `config.yaml`：
  - `latest_version: 1.0.2`
  - `download_url: http://intranet-cdn.local/go-agent/v1.0.2/go-agent-amd64`（示例）
  - `sha256: <new_sha256>`
  - （可选）`rollout_pct: 10`（先灰度 10%）

服务端无需重启（版本接口读的是文件内容；如你用配置热加载/重启策略，可按运维规范执行）。

#### 6.2.4 灰度发布建议（2,000 台规模推荐节奏）

为避免“全网同时更新导致不可控故障”，建议按以下节奏逐步放量（结合 `rollout_pct` 与 `device_id` 末位分桶）：

- **第 1 阶段：1%**（先验证关键环境/网络/杀软/权限差异）
  - `rollout_pct: 1`
  - 观察 30-60 分钟：在线率、更新失败率、崩溃回滚次数、资产上报完整性
- **第 2 阶段：10%**
  - `rollout_pct: 10`
  - 观察 1-4 小时：服务端资源、Redis 压力、带宽峰值、异常离线设备数
- **第 3 阶段：100%**
  - `rollout_pct: 100`
  - 持续观察：告警风暴是否被抑制、控制通道是否稳定、更新后离线是否异常

> 关键原则：任何阶段发现“更新后离线/崩溃回滚”显著升高，应立即停止放量并回滚 `config.yaml` 的版本元数据。

#### 6.2.5 备份与灾备（强烈建议）

- **Redis 数据备份**
  - Redis 承载快照与聚合缓存（以及告警抑制窗口等），建议开启持久化（RDB/AOF）并做定期备份。
  - 备份内容至少包括：Redis 数据文件、Redis 配置文件、以及恢复操作手册（演练很重要）。

- **mTLS 证书备份（必须重视）**
  - 证书/私钥（CA、Server、Client）属于“系统身份根”，建议加密备份并做权限分级。
  - **严重后果提醒**：如果 CA 私钥或关键客户端证书丢失且无法恢复，可能导致：
    - 新 Agent 无法再被信任接入（需要全网重新发证/重装）
    - 控制通道/上报链路大面积中断（业务不可用）
  - 建议把证书放入专用的密钥管理系统（KMS/保险库），不要散落在个人电脑目录。

#### 6.2.6 性能调优（针对 2,000 台）

- **文件句柄限制**：建议设置 `ulimit -n 65535`（systemd 已可用 `LimitNOFILE=65535`）
- **开启/确保 Gzip**（带宽优化关键）：
  - Agent 上报软件清单体积大，服务端上报接口 **强制要求** `Content-Encoding: gzip`
  - 若看到 415/400 或“解压失败”类报错，优先检查：Agent 是否启用了 gzip 上报、是否被代理改写头部
- **HTTP 超时参数**：
  - 服务端支持 `--read-timeout/--write-timeout/--idle-timeout`，可按链路质量做适当调整

#### 6.2.7 健康检查（快速判断是否工作正常）

- **进程健康**：访问（返回 `{"status":"ok"}` 表示 HTTP 服务已启动）：

```bash
curl -fsS http://127.0.0.1:8080/healthz
```

- **Redis 连通性**：服务端依赖 Redis 存快照与聚合缓存，建议同时确认：

```bash
redis-cli -h 127.0.0.1 -p 6379 PING
```

- **业务自检（可选）**：
  - `GET /api/v1/devices` 能返回列表（说明快照/查询链路可用）
  - `GET /swagger/index.html` 能打开（说明路由与文档可用）

### 6.3 Linux 批量安装（Ansible）

```bash
ansible-playbook -i hosts deploy/linux/deploy_agent.yml \
  -e agent_server_addr="http://10.x.x.x:8080" \
  -e agent_control_token="your_secure_token"
```

技术说明：
- `deploy/linux/deploy_agent.yml` 已内置 `serial: "10%"` 与 `max_fail_percentage: 10`
- 架构映射：`x86_64/amd64 -> ./dist/go-agent-amd64`，`aarch64/arm64 -> ./dist/go-agent-arm64`

### 6.4 Windows 批量安装（PowerShell）

```powershell
.\deploy\windows\deploy_agent.ps1 `
  -ServerAddr "http://10.x.x.x:8080/ingest" `
  -ControlToken "xxx" `
  -TargetServers @("host1","host2")
```

或：

```powershell
.\deploy\windows\deploy_agent.ps1 `
  -ServerAddr "http://10.x.x.x:8080/ingest" `
  -ControlToken "xxx" `
  -TargetServersFile ".\servers.txt"
```

注意：
- 当前 `deploy/windows/deploy_agent.ps1` 不支持 `-ThreadCount`
- `ServerAddr` 在脚本内映射为 Agent 的 `-api-url`

### 6.5 关键资源参数（systemd）

`templates/go-agent.service.j2` 已包含：
- `MemoryAccounting=yes`
- `MemoryMax=128M`
- `CPUAccounting=yes`
- `CPUQuota=10%`
- `LimitNOFILE=65535`

### 6.6 运维回滚与高级排障

| 现象 | 可能原因 | 处置建议 |
|---|---|---|
| 资产中心无设备数据 | Token 不一致 / device-id 冲突 | 对齐 `CONTROL_CHANNEL_TOKEN`，检查 `-device-id` 唯一性 |
| Windows 批量部署失败 | WinRM 未开 / 防火墙限制 | 执行 `Enable-PSRemoting -Force` 并放通 WinRM |
| 更新后持续离线 | 新版崩溃 | 用部署脚本回滚；Agent 已内置“1分钟内连续失败3次自动回滚” |
| 版本接口 404 | 不在灰度批次或元数据缺失 | 检查 `rollout_pct`、`device_id` 末位分桶和 `config.yaml` |

### 6.7 一致性校对结论

- `deploy/windows/deploy_agent.ps1` 参数命名：`-ServerAddr`、`-ControlToken`、`-TargetServers` / `-TargetServersFile`
- Windows `.bat` 替换逻辑已包含路径双引号
- Windows 更新脚本已包含替换前 `timeout /t 5` 缓冲
- `install.bat` 已包含管理员检查、网络预检、`install_error.txt`
- 建议下载地址走内网 Nginx/CDN（当前 `config.yaml` 示例已采用内网 URL 风格）
