# Go Agent (IT Device Data Collection)

跨平台的 IT 设备数据采集 Agent（Windows/Linux/macOS），包含：
- 主机指标采集（CPU/内存/磁盘/网络）
- 进程快照
- 软件资产（已安装应用）扫描（12 小时周期）
- 本地 BoltDB 缓冲（抗网络波动）
- mTLS 安全上报（Dispatcher 从 BoltDB 消费并在 200 OK 后删除）

## 运行与配置

### 关键参数
- `-data-dir`：本地数据目录（BoltDB 缓存会落在该目录下的 `agent_cache.db`）
- `-api-url`：后端上报地址（Dispatcher 会 POST JSON 到该 URL）
- mTLS 证书相关：
  - `-ca-cert`：CA 根证书（PEM）
  - `-client-cert`：客户端证书（PEM）
  - `-client-key`：客户端私钥（PEM）
  - `-insecure-skip-verify`：演示用；建议生产环境关闭（设为 `false`）

### 前台调试
```bash
./agent -debug -data-dir ./data
```

> 说明：`-debug` 模式会在前台运行，并等待 `Ctrl+C` 触发优雅停止。

## 注册为系统服务

本项目使用 `github.com/kardianos/service` 作为跨平台服务管理库。

常见用法（按平台和权限要求运行）：
```bash
# Linux / macOS
sudo ./agent install
sudo ./agent start
sudo ./agent stop
sudo ./agent remove

# Windows（管理员 PowerShell）
.\agent.exe install
.\agent.exe start
.\agent.exe stop
.\agent.exe remove
```

如果你当前仓库处于离线构建模式（`go.mod` 含 `replace` 指向本地 stub），建议在最终打包发布前移除这些 `replace`（以使用真正的依赖实现）。

## 构建

### 本地交叉编译
```bash
make
```

输出目录：`dist/`

## 版本信息注入

可通过 `go build -ldflags` 注入版本信息：
```bash
go build -ldflags "\
  -X 'github.com/qinyilin/go-agent/cmd/agent.Version=1.2.3' \
  -X 'github.com/qinyilin/go-agent/cmd/agent.BuildTime=2026-03-31T00:00:00Z' \
  -X 'github.com/qinyilin/go-agent/cmd/agent.GitCommit=abcdef0' \
"
```

