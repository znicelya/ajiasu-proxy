# AJiaSu Enterprise Proxy Platform

AJiaSu Enterprise Proxy Platform 是一个面向多租户场景的企业代理管理平台。本仓库包含 Control Plane、Gateway、Node Agent、按连接隔离的 Runner、React 运维控制台，以及 Docker Compose、Helm、可观测性、备份恢复和发布验证资产。

平台不会修改官方 AJiaSu CLI。Agent 会按需创建 Runner，每个活动连接使用独立的 Runner 隔离边界；租户、账户、代理池、端点、配额、审计和调度状态由 Control Plane 管理。

> 使用真实 AJiaSu 账户前，必须完成 [AJiaSu 使用合规门禁](docs/compliance/ajiasu-usage-gate.md)。仓库许可证不代表获得 AJiaSu 服务的使用授权。

## 功能概览

- 多租户、成员、角色、会话、服务令牌和审计管理
- AJiaSu 账户密文存储、轮换、代理池和租户配额
- 固定代理端点与池化端点调度
- Gateway HTTP CONNECT 与 SOCKS5 入口
- Agent 节点注册、Runner 生命周期、健康检查和故障收敛
- PostgreSQL 权威状态、Redis 租约协调和 fencing 防超卖
- React + Fluent UI 运维控制台
- Prometheus 告警、Grafana 仪表盘、OpenTelemetry 和 SIEM 导出契约
- Docker Compose 单机生产包和 Helm/Kubernetes 部署包
- PostgreSQL/keyring 备份恢复、容量测试、SBOM、签名和发布验证

## 架构

```text
Operator / Console
        |
        v
Control Plane ---- PostgreSQL
      |  \--------- Redis
      |
      +---- Gateway <---- HTTP CONNECT / SOCKS5 clients
      |
      +---- Agent ---- creates isolated Runner containers
                            |
                            v
                       official AJiaSu CLI
```

关键边界：

- PostgreSQL 是租户、账户、端点、分配、操作和审计的权威数据源。
- Redis 只用于租约、fencing 和短期协调，不能作为业务数据备份恢复。
- 只有 Agent 可以访问 Docker socket；Agent 被攻破等同于宿主机被攻破。
- Runner 默认使用非 root 用户 `65532:65532`、只读根文件系统、`cap_drop: ALL` 和 `no-new-privileges`。
- 需要精确聚合连接/流量限制时，只支持一个活动 Gateway。

## 环境要求

### Docker Compose

- 受维护的 64 位 Linux 生产宿主机；开发时可使用 Docker Desktop
- Docker Engine 27+
- Docker Compose v2.33.1+
- Docker Buildx v0.19+
- PowerShell 7，命令为 `pwsh`
- Git、Go 1.25+ 和 Rust stable（仅源码开发或完整测试需要）
- 同步的 UTC 时间和足够的 PostgreSQL/备份磁盘空间

检查工具：

```powershell
docker version
docker compose version
docker buildx version
pwsh --version
```

### Kubernetes

- Kubernetes 1.27+
- Helm 3.17+
- kubectl
- 外部 PostgreSQL、Redis、OIDC 和 Secret 管理方案

## 快速开始：Docker Compose 开发环境

所有命令均从仓库根目录执行。示例中的镜像必须替换为实际发布的不可变摘要，不能使用 `latest` 或其他可变标签。

### 1. 获取代码

```powershell
git clone https://github.com/znicelya/ajiasu-proxy.git
Set-Location ajiasu-proxy
```

### 2. 准备不可变镜像

需要四个按 digest 固定的镜像：

```text
registry.example.com/ajiasu-control-plane@sha256:<64位摘要>
registry.example.com/ajiasu-gateway@sha256:<64位摘要>
registry.example.com/ajiasu-agent@sha256:<64位摘要>
registry.example.com/ajiasu-runner@sha256:<64位摘要>
```

发布兼容性记录位于 [build/compatibility-matrix.yaml](build/compatibility-matrix.yaml)。当前数据库 schema 为 11，支持 `linux/amd64` 和 `linux/arm64`。

### 3. 初始化开发环境

初始化命令会生成私有 CA、服务身份、keyring、环境文件和一次性注册材料。重复执行不会自动轮换已有密钥。

```powershell
pwsh -File scripts/compose-init.ps1 `
  -EnvironmentId dev-local `
  -Mode development `
  -ControlPlaneImage 'registry.example.com/ajiasu-control-plane@sha256:<digest>' `
  -GatewayImage 'registry.example.com/ajiasu-gateway@sha256:<digest>' `
  -AgentImage 'registry.example.com/ajiasu-agent@sha256:<digest>' `
  -RunnerImage 'registry.example.com/ajiasu-runner@sha256:<digest>'
```

默认生成：

- 环境文件：`deploy/compose/env/compose.env.local`
- 私密状态：`deploy/compose/generated/`
- 本地 PostgreSQL、Redis，以及开发身份提供方配置

这些文件均不能提交到 Git，也不要复制到工单或聊天记录。

### 4. 启动服务

首次体验可以显式跳过代理 smoke probe：

```powershell
pwsh -File scripts/compose-up.ps1 `
  -EnvFile deploy/compose/env/compose.env.local `
  -Mode development `
  -SkipSmoke
```

启动流程会依次验证镜像摘要和生成状态、启动依赖、运行数据库迁移、启动 Control Plane、自动注册 Agent/Gateway，并等待健康状态。

默认端口：

| 端口 | 用途 |
| --- | --- |
| `8081` | Control Plane 管理入口 |
| `8080` | Gateway HTTP CONNECT |
| `1080` | Gateway SOCKS5 |

### 5. 创建本地应急管理员

```powershell
pwsh -File scripts/compose-admin-bootstrap.ps1 `
  -EnvFile deploy/compose/env/compose.env.local `
  -Mode development
```

该命令是交互式的。TOTP 和恢复码只显示一次，请立即保存到批准的密码管理器。

### 6. 查看状态

```powershell
pwsh -File scripts/compose-status.ps1 `
  -EnvFile deploy/compose/env/compose.env.local `
  -Mode development
```

输出是一个经过限制的 JSON 文档，只包含组件健康、会话和分配计数，不包含 DSN、令牌、代理凭据或目标地址。没有固定与池化端点时，整体状态可能是 `degraded`，即使核心组件已经健康。

### 7. 停止服务

```powershell
pwsh -File scripts/compose-down.ps1 `
  -EnvFile deploy/compose/env/compose.env.local `
  -Mode development
```

停止流程会先 drain，再停止 Gateway 和 Agent，只清理由当前节点拥有且标签完整的 Runner，最后停止依赖。它不会删除持久卷。

## 生产模式选择

| 模式 | PostgreSQL/Redis | OIDC | 适用场景 |
| --- | --- | --- | --- |
| `development` | Compose 内置 | 可使用开发身份服务 | 本地开发和假数据测试 |
| `single-host` | Compose 内置 | HTTPS OIDC | 单机生产或受控验证环境 |
| `external` | 运维方托管 | HTTPS OIDC | 正式生产环境 |

生产环境必须使用 HTTPS OIDC issuer 和 redirect URL。`external` 模式还要求：

- 三个数据库 DSN 分别写入私密文件：normal、platform、migration
- PostgreSQL 使用 `sslmode=verify-full`
- Redis 使用 TLS，并提供显式外部地址
- DSN 和密码只通过文件或 Secret 系统传入，不能放入命令行参数

外部依赖初始化示例：

```powershell
pwsh -File scripts/compose-init.ps1 `
  -EnvironmentId prod-cn1 `
  -Mode external `
  -ControlPlaneImage 'registry.example.com/ajiasu-control-plane@sha256:<digest>' `
  -GatewayImage 'registry.example.com/ajiasu-gateway@sha256:<digest>' `
  -AgentImage 'registry.example.com/ajiasu-agent@sha256:<digest>' `
  -RunnerImage 'registry.example.com/ajiasu-runner@sha256:<digest>' `
  -NormalDatabaseDsnFile 'C:\secure\normal-dsn' `
  -PlatformDatabaseDsnFile 'C:\secure\platform-dsn' `
  -MigrationDatabaseDsnFile 'C:\secure\migration-dsn' `
  -RedisAddress 'redis.example.com:6380' `
  -RedisTls $true `
  -OidcIssuer 'https://id.example.com/realms/ajiasu' `
  -OidcRedirectUrl 'https://proxy.example.com/api/v1/auth/oidc/callback'
```

正式流量前应在审查过的入口代理处终止外部 TLS，并且不要公开 PostgreSQL 或 Redis 端口。

## Smoke probe 与就绪验收

生产启动必须同时验证固定端点和池化端点。创建一个不纳入版本控制的私密 JSON 文件：

```json
{
  "fixed": {
    "proxy_uri": "http://gateway.example.com:8080",
    "target_uri": "https://approved-target.example.com/health",
    "username": "probe-user",
    "password": "replace-in-private-file",
    "expected_status": 200
  },
  "pool": {
    "proxy_uri": "http://gateway.example.com:8080",
    "target_uri": "https://approved-target.example.com/health",
    "username": "pool-probe-user",
    "password": "replace-in-private-file",
    "expected_status": 200
  }
}
```

然后启动：

```powershell
pwsh -File scripts/compose-up.ps1 `
  -EnvFile deploy/compose/env/compose.env.local `
  -Mode single-host `
  -SmokeConfigurationFile C:\secure\ajiasu-smoke.json
```

不要在 CI artifact、日志或工单中保存该文件内容。

## 运维控制台

Console 位于 `console/`，通过 `/api/v1` 调用 Control Plane，并使用浏览器安全会话。它不会把凭据或 secret payload 保存到 localStorage/sessionStorage。

本地开发：

```powershell
Set-Location console
npm ci
npm run dev
```

生产构建：

```powershell
npm ci
npm run build
```

构建产物位于 `console/dist/`。部署时应由受信任的静态服务器或入口代理托管，并将 `/api/v1`、`/readyz` 和认证回调转发到 Control Plane。生产环境必须使用 HTTPS，且前端与 API 的 Cookie、CSRF 和 OIDC origin 配置必须一致。

## Kubernetes / Helm 部署

1. 复制并审查 `deploy/helm/ajiasu/examples/values-production.yaml`。
2. 在目标 namespace 中预先创建运行时 Secret，或使用 External Secrets/Vault/CSI/KMS 物化。
3. 使用四个不可变镜像摘要运行 preflight。
4. 安装后检查 rollout、迁移、Agent/Gateway 会话和 fixed/pool smoke probe。

```powershell
pwsh -File scripts/helm-preflight.ps1 `
  -Release ajiasu `
  -Namespace ajiasu-system `
  -ValuesFile deploy/helm/ajiasu/examples/values-production.yaml `
  -ControlPlaneDigest 'sha256:<digest>' `
  -GatewayDigest 'sha256:<digest>' `
  -AgentDigest 'sha256:<digest>' `
  -RunnerDigest 'sha256:<digest>' `
  -SecretName ajiasu-runtime

pwsh -File scripts/helm-install.ps1 `
  -Release ajiasu `
  -Namespace ajiasu-system `
  -ValuesFile deploy/helm/ajiasu/examples/values-production.yaml `
  -ControlPlaneDigest 'sha256:<digest>' `
  -GatewayDigest 'sha256:<digest>' `
  -AgentDigest 'sha256:<digest>' `
  -RunnerDigest 'sha256:<digest>' `
  -SecretName ajiasu-runtime
```

当前 Agent 可执行运行时是 Docker/process。Chart 中的 Runner Pod 模板是 Kubernetes 所有权与安全契约；在实现网络化 Runner relay adapter 前，不应宣称已启用 Kubernetes-native Runner runtime。详见 [Helm/Kubernetes 运维指南](docs/operations/helm-kubernetes-phase8.md)。

## 备份、恢复、升级与回滚

### 备份

仅 `development` 和 `single-host` 使用 Compose 备份脚本：

```powershell
pwsh -File scripts/compose-backup.ps1 `
  -EnvFile deploy/compose/env/compose.env.local `
  -Mode single-host `
  -Destination E:\backups\ajiasu-2026-07-20
```

备份包含 PostgreSQL dump、匹配的 Control Plane keyring、CA/config evidence 和校验清单。Redis、租约、会话、Runner 状态和路由缓存不会被备份。数据库与 keyring 应分别加密并异地保存。

目标：RPO 不超过 15 分钟，RTO 不超过 60 分钟。

### 恢复

Compose 恢复只能针对明确标记的一次性环境执行：

```powershell
pwsh -File scripts/compose-restore.ps1 `
  -EnvFile deploy/compose/env/compose.env.local `
  -Mode single-host `
  -BackupDirectory E:\backups\ajiasu-2026-07-20 `
  -Disposable
```

恢复后仍需执行 schema、keyring、加密凭据、固定端点和池化端点 smoke 验证。外部 PostgreSQL 应使用数据库服务商的 PITR，并执行相同的清单与 keyring 校验。

### 升级与回滚

- 升级前验证镜像摘要、兼容性矩阵和完整备份。
- 使用 `scripts/compose-upgrade.ps1` 执行 drain、迁移、按依赖顺序重启和 smoke 验收。
- 使用 `scripts/compose-rollback.ps1` 回滚时，必须恢复与旧版本匹配的数据库和 keyring。
- 只切换镜像标签不构成安全回滚。

完整流程见 [Compose 恢复和升级指南](docs/operations/compose-recovery-upgrade.md)。

## 可观测性

Phase 9 提供以下部署资产：

- Prometheus：`deploy/observability/prometheus/`
- Grafana：`deploy/observability/grafana/ajiasu-overview.json`
- OpenTelemetry Collector：`deploy/observability/otel/collector-config.yaml`
- SIEM audit export：`deploy/observability/siem/`

指标标签必须保持有界，不能包含租户 secret、代理凭据、目标主机或任意用户输入。告警对应处理步骤见 [Phase 9 Operator Runbooks](docs/operations/phase9-runbooks.md)。

## 容量测试

负载工具会创建有界 HTTP CONNECT 会话，并输出成功/失败数、p95/p99 建连延迟、客户端 heap 和持续时间：

```powershell
go run ./cmd/phase9-load `
  -address 127.0.0.1:8080 `
  -target approved-target.example.com:443 `
  -connections 100 `
  -hold 10s `
  -timeout 15s `
  -max-errors 0 `
  -max-heap-mib 1024
```

10,000 连接只能在一次性环境、假 AJiaSu 凭据和外部监控就绪时运行：

```powershell
$env:AJIASU_PHASE9_LOAD_GATE = 'I_UNDERSTAND'
go run ./cmd/phase9-load -address <gateway> -target <approved-target> -connections 10000
Remove-Item Env:AJIASU_PHASE9_LOAD_GATE
```

测试前后必须查询 PostgreSQL 中的分配和账户 reservation，证明没有 lease oversell 或账户并发超限。不要仅凭客户端成功率宣布容量门禁通过。

## 构建 Runner 镜像

Runner 锁定官方 AJiaSu archive 的版本、大小和校验和，同时锁定 Alpine 基础镜像 digest。不要替换为可变 tag。

```powershell
$lockLine = Get-Content runner/artifacts/alpine-3.22.lock
if ($lockLine -notmatch '^ALPINE_IMAGE=(alpine:3\.22@sha256:[0-9a-f]{64})$') {
  throw 'Invalid Alpine image lock'
}
$alpineImage = $Matches[1]
docker build --pull=false `
  --build-arg "ALPINE_IMAGE=$alpineImage" `
  -t ajiasu-runner:test .
```

## 开发与测试

常用门禁：

```powershell
# Go
go test ./...

# Rust
cargo test --workspace --all-features --locked

# Console
Set-Location console
npm ci
npm run build
Set-Location ..

# Phase 9 契约
pwsh -File tests/console/contract.ps1
pwsh -File tests/observability/contract.ps1
pwsh -File tests/recovery/contract.ps1
pwsh -File tests/release/contract.ps1

# Runner 完整门禁，需要 Docker
pwsh -File scripts/ci.ps1

# Compose 门禁，需要 Docker/Testcontainers
pwsh -File scripts/compose-ci.ps1
```

部分 Go、Compose 和 Helm 测试会启动容器或临时 Kubernetes 集群，不能把缺少 Docker、Helm、Kind 或外部依赖导致的跳过记录为通过。

## 常见问题

### `compose-status.ps1` 返回 `degraded`

先确认 Control Plane、Agent 和 Gateway 是否健康。完整 `ready` 还要求至少一个在线节点、一个 Gateway 会话，以及固定和池化分配均已就绪。

### Redis 不可用

已有安全流量可以依赖 PostgreSQL 已提交分配继续运行，但新的池化分配必须停止。不要手工删除或重建 Redis lease key；恢复 Redis 后等待 fencing 和调度收敛。

### 启动或升级失败

脚本默认保留容器、卷和生成状态用于诊断。先保存操作 ID和经过脱敏的日志，不要立即执行全量清理，也不要把 `docker inspect`、环境变量、DSN 或 Secret 原文发送到工单。

### 忘记管理员恢复码

不要尝试从日志或数据库导出敏感认证材料。按安全事件流程使用批准的 break-glass 或身份恢复程序，并记录审计证据。

### 如何彻底删除环境

正常 `compose-down.ps1` 不会删除持久卷。永久删除属于破坏性操作：先验证备份、正常 drain、确认没有遗留 Runner，再按组织的数据保留策略删除 Compose volumes、生成目录和异地副本。

## 安全注意事项

- 禁止把 AJiaSu 凭据、OIDC secret、DSN、keyring、证书私钥和 smoke 文件提交到仓库。
- 禁止使用 `privileged: true`、host PID/IPC 或把 Docker socket 暴露给 Agent 以外的服务。
- 禁止跨租户共享 Runner。
- 禁止将 Redis 恢复为权威业务状态。
- 发布镜像必须使用 digest，并验证 SBOM、provenance 和签名。
- 安全事件中应撤销会话和注册材料、轮换受影响身份、保留不可变审计记录；Agent 泄露按宿主机泄露处理。

## 相关文档

- [平台设计](docs/superpowers/specs/2026-07-11-enterprise-proxy-platform-design.md)
- [实施路线图](docs/superpowers/plans/2026-07-11-enterprise-proxy-platform-roadmap.md)
- [Docker Compose 运维指南](docs/operations/docker-compose-phase7.md)
- [Compose 生命周期](docs/operations/compose-lifecycle.md)
- [Compose 恢复与升级](docs/operations/compose-recovery-upgrade.md)
- [Helm/Kubernetes 运维指南](docs/operations/helm-kubernetes-phase8.md)
- [Phase 9 Runbooks](docs/operations/phase9-runbooks.md)
- [恢复与容量指南](docs/operations/phase9-recovery-capacity.md)
- [Phase 9 退出证据](docs/operations/phase9-exit-evidence.md)
- [兼容性矩阵](docs/operations/compatibility-matrix.md)
- [Runner 安全边界 ADR](docs/adr/0001-runner-security-boundary.md)
- [AJiaSu 使用合规门禁](docs/compliance/ajiasu-usage-gate.md)

## License

仓库代码使用 MIT License。AJiaSu 是第三方软件，受其自身许可证和服务条款约束；本仓库许可证不授予 AJiaSu 的运营或商业使用许可。
