# CPA RespFilter

> 一个 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) C ABI 插件，通过完全可配置、可热重载的规则引擎检测并过滤 OpenAI 推理降智问题——**"516 问题"**。

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go 1.21+](https://img.shields.io/badge/Go-1.21+-00ADD8.svg)](https://go.dev/)
[![Platform: Linux · macOS · Windows](https://img.shields.io/badge/Platform-Linux·macOS·Windows-blue.svg)](#build-from-source)

---

## 目录

- [概述](#概述)
- [516 问题](#516-问题)
- [架构](#架构)
- [特性](#特性)
- [快速开始（Ubuntu）](#快速开始ubuntu)
- [从源码构建](#从源码构建)
- [配置](#配置)
- [Management API](#management-api)
- [工作原理](#工作原理)
- [故障排查](#故障排查)
- [文件结构](#文件结构)
- [许可证](#许可证)

---

## 概述

`CPA RespFilter` 是 CLIProxyAPI 的标准动态库插件。它检查模型响应（流式和非流式）中是否存在推理降智的证据，并在检测到降智时执行可配置的操作——替换为错误响应、重试请求或仅记录日志。

插件**不依赖 CLIProxyAPI SDK**——直接实现 C ABI 并内置类型定义，轻量且跨 CPA 版本稳定。

---

## 516 问题

当 OpenAI 对第三方客户端（CLIProxyAPI、OpenCode、Codex SDK 等）的请求进行降智时，响应始终报告固定的 `reasoning_tokens` 值——通常是 **516**——与请求的推理强度（`low`/`medium`/`high`/`xhigh`）无关。

**可观察的症状：**

| 指标 | 正常 | 降智 |
|------|------|------|
| `reasoning_tokens` | 随推理强度变化 | 固定为 516 |
| 回答质量 | 复杂推理正确 | 明显变差 |
| Token 费用 | 全额 | 仍按全额计费 |
| UI 思考显示 | 可见推理摘要 | 摘要存在但被截断 |

**来源：** [CLIProxyAPI Discussion #3937](https://github.com/router-for-me/CLIProxyAPI/discussions/3937)

本插件将这个固定特征转化为检测信号：如果 `reasoning_tokens` 匹配配置的值（默认 516），响应被视为降智并按规则处理。

---

## 架构

```
┌─────────────────────────────────────────────────────────────────┐
│                     CLIProxyAPI 宿主                            │
│                                                                 │
│  客户端请求 ──► 凭证选择 ──► 上游执行                           │
│                                  │                              │
│                                  ▼                              │
│                        ┌──────────────────┐                     │
│                        │  响应管道         │                     │
│                        │                  │                     │
│  ┌────────────────────┤  response.       │                     │
│  │   RespFilter 插件   │  normalize_after  ├─► 客户端            │
│  │                    │                  │                     │
│  │  ┌─────────┐  ┌───┴──────┐          │  response.       │   │
│  │  │ 规则    │  │ 检测器    │          │  intercept_       │   │
│  │  │ (有序)  │──│ (算子)    │          │  stream_chunk     │   │
│  │  └────┬────┘  └────┬─────┘          │                  │   │
│  │       │            │                 └──────────────────┘   │
│  │       ▼            │                          ┌──────────┐  │
│  │  ┌─────────┐       │                          │usage     │  │
│  │  │ 动作    │◄──────┘                          │.handle   │  │
│  │  │ 引擎    │                                  │(统计/日志)│  │
│  │  └────┬────┘                                  └──────────┘  │
│  │       │                                                     │
│  │       ├── error ──► 注入错误响应                             │
│  │       ├── retry ──► host.model.execute（重入保护）           │
│  │       └── log   ──► host.log 回调                            │
│  │                                                               │
│  │  配置：atomic.Pointer（热重载，无锁读取）                    │
│  └─────────────────────────────────────────────────────────────┘
│                                                                 │
│  Management API: /v0/resource/plugins/respfilter/status        │
└─────────────────────────────────────────────────────────────────┘
```

---

## 特性

| 特性 | 说明 |
|------|------|
| **多规则引擎** | 定义多条命名检测规则，首个命中执行 |
| **7 种匹配算子** | `exact`、`any_of`、`lte`、`gte`、`range`、`not_in`、`always` |
| **可配置 JSON 路径** | gjson 语法——无需新版本插件即可适应上游 API 变更 |
| **通配符模型过滤** | `gpt-5*`、`*-codex`、`*flash*` 等 |
| **模板化响应** | Go `text/template` 渲染错误体、流式错误和日志消息 |
| **三种操作模式** | `error`（注入错误）、`retry`（通过宿主回调重试）、`log`（仅观察） |
| **热重载** | 通过 `plugin.reconfigure` 即时生效——无需重启，不阻塞请求 |
| **流式优化** | 关键词预过滤，跳过不含 usage 数据的 SSE chunk |
| **Management API** | 浏览器可访问的 `/status` 端点，展示实时规则和检测统计 |
| **原子配置** | `atomic.Pointer` 无锁读取；配置时预编译模板 |
| **零 SDK 依赖** | 直接实现 C ABI，无需 `go get` CLIProxyAPI |

---

## 快速开始（Ubuntu）

### 前置条件

```bash
# Go 1.21+
sudo apt-get install -y golang-go

# C 编译器 (gcc) 和构建工具
sudo apt-get install -y build-essential

# 验证
go version
gcc --version
```

### 第 1 步 — 构建插件

```bash
git clone <你的仓库地址> respfilter
cd respfilter

# 编译为 Linux 共享库
CGO_ENABLED=1 go build -buildmode=c-shared -o respfilter.so .
```

你会看到 `respfilter.so`（以及一个运行时不需要的 `respfilter.h` 头文件）：

```bash
ls -la respfilter.so
# -rwxr-xr-x 1 user user 6.6M ... respfilter.so

rm -f respfilter.h  # 运行时不需要
```

### 第 2 步 — 安装到 CLIProxyAPI

```bash
# 定位你的 CLIProxyAPI 安装目录。常见路径：
#   /opt/cliproxyapi/        （手动安装）
#   ~/cliproxyapi/           （主目录）
#   /usr/local/bin/          （仅二进制——需要工作目录）

# 创建插件目录结构（CPA 按优先级扫描这些目录）：
mkdir -p /opt/cliproxyapi/plugins/linux/amd64

# 复制插件（用 cp，不要用符号链接——CPA 会跳过符号链接）
cp respfilter.so /opt/cliproxyapi/plugins/linux/amd64/

# 设置权限（dlopen 需要读+执行权限）
chmod 755 /opt/cliproxyapi/plugins/linux/amd64/respfilter.so
chmod +x /opt/cliproxyapi/plugins /opt/cliproxyapi/plugins/linux /opt/cliproxyapi/plugins/linux/amd64
```

> **目录优先级：** CPA 扫描顺序为 `plugins/linux/amd64-v3/` → `plugins/linux/amd64/` → `plugins/`（首个匹配的文件生效）。多架构部署用架构子目录，简单部署直接放 `plugins/`。

### 第 3 步 — 配置

编辑 CLIProxyAPI 的 `config.yaml`：

```yaml
plugins:
  enabled: true                          # 总开关——必需
  configs:
    respfilter:                          # 必须与文件名匹配（respfilter.so → "respfilter"）
      enabled: true                      # 插件级开关——必需
      priority: 1
      # 最小配置：使用内置默认值（检测 516，操作 error，所有模型）
```

完整规则化配置参见 [配置](#配置) 章节。

### 第 4 步 — 启动并验证

```bash
# 从 CLIProxyAPI 的工作目录启动（plugins/ 相对于当前工作目录）
cd /opt/cliproxyapi
./cli-proxy-api --config config.yaml &

# 检查日志确认加载成功
journalctl -u cliproxyapi -f 2>/dev/null | grep pluginhost
# 或在前台运行时直接查看 stdout
```

预期日志输出：

```
pluginhost: plugin loaded  plugin_id=respfilter path=plugins/linux/amd64/respfilter.so
```

**通过 Management API 验证**（需要 config 中设置 `remote-management.secret-key`）：

```bash
# 列出已注册插件
curl -s -H "Authorization: Bearer <你的密钥>" \
  http://localhost:8317/v0/management/plugins | jq

# 查看 respfilter 状态（规则 + 统计）
curl -s http://localhost:8317/v0/resource/plugins/respfilter/status | jq
```

### 第 5 步 — 测试检测

发送会触发降智的请求。检测到 516 时：

- **`error` 模式：** 客户端收到 HTTP 503 和 JSON 错误体
- **`retry` 模式：** 插件重新执行请求；日志显示重试次数
- **`log` 模式：** 响应原样透传；日志显示检测事件

查看检测统计：

```bash
curl -s http://localhost:8317/v0/resource/plugins/respfilter/status | jq .stats
```

---

## 从源码构建

### Linux (Ubuntu/Debian)

```bash
sudo apt-get install -y golang-go build-essential
CGO_ENABLED=1 go build -buildmode=c-shared -o respfilter.so .
rm -f respfilter.h
```

### 交叉编译

```bash
# 不同架构（如 arm64）
GOOS=linux GOARCH=arm64 CGO_ENABLED=1 CC=aarch64-linux-gnu-gcc \
  go build -buildmode=c-shared -o respfilter.so .

# 从 Linux 编译 Windows 版（需要 mingw-w64）
GOOS=windows GOARCH=amd64 CGO_ENABLED=1 CC=x86_64-w64-mingw32-gcc \
  go build -buildmode=c-shared -o respfilter.dll .

# 从 Linux 编译 macOS 版（需要 osxcross）
GOOS=darwin GOARCH=amd64 CGO_ENABLED=1 CC=o64-clang \
  go build -buildmode=c-shared -o respfilter.dylib .
```

### 使用 Makefile

```bash
make build    # 构建当前平台
make clean    # 清理构建产物
make test     # 运行 go vet
```

### 依赖

插件外部依赖极少：

| 依赖 | 用途 |
|------|------|
| `github.com/tidwall/gjson` | JSON 路径提取（`usage.reasoning_tokens` 等） |
| `gopkg.in/yaml.v3` | YAML 配置解析 |
| Go 标准库 | `text/template`、`encoding/json`、`sync/atomic`、`sync`、`unsafe`、`C` |

**无 CLIProxyAPI SDK 依赖**——插件直接实现 C ABI，使用本地类型定义。

---

## 配置

插件使用**规则化**配置系统。规则按顺序求值——首个匹配的规则决定操作。

### 最小配置（内置默认值）

```yaml
plugins:
  enabled: true
  configs:
    respfilter:
      enabled: true
      priority: 1
```

使用内置默认值：检测 `reasoning_tokens == 516`，操作 `error`，所有模型，标准 JSON 路径。不需要 `rules` 或 `defaults` 块。

### 完整配置示例

```yaml
plugins:
  enabled: true
  configs:
    respfilter:
      enabled: true
      priority: 1

      # ── 检测规则（首个命中执行）──────────────────────────────────
      rules:
        # 规则 1：GPT-5.x 模型的经典 516 降智
        - name: "openai-516"
          paths:
            - "usage.reasoning_tokens"
            - "usage.completion_tokens_details.reasoning_tokens"
            - "usage.output_tokens_details.reasoning_tokens"
            - "reasoning_tokens"
          stream_paths:
            - "response.usage.output_tokens_details.reasoning_tokens"
            - "response.usage.reasoning_tokens"
          operator: "exact"
          values: [516]
          models: ["gpt-5*"]
          action: "error"
          error_status: 503
          error_body: '{"error":{"message":"推理降智检测 (tokens={{.Tokens}})。","type":"degradation_filter","code":"reasoning_516"}}'
          stream_error: |
            data: {"error":{"message":"推理降智检测。","type":"degradation_filter","code":"reasoning_516"}}

            data: [DONE]
          log: true
          log_level: "warn"
          log_msg: 'respfilter[{{.Rule}}]: {{.Model}} tokens={{.Tokens}} path={{.Path}}'

        # 规则 2：监控极低推理——仅记录
        - name: "low-reasoning-watch"
          operator: "lte"
          values: [10]
          action: "log"

        # 规则 3：特定模型的重试模式
        - name: "gpt-5-codex-retry"
          operator: "any_of"
          values: [516, 512, 508]
          models: ["gpt-5-codex"]
          action: "retry"
          retry_max: 2
          retry_backoff_ms: 500

      # ── 默认值（规则省略字段时使用）──────────────────────────────
      defaults:
        operator: "exact"
        values: [516]
        action: "error"
        error_status: 503

      # ── 流式优化 ──────────────────────────────────────────────────
      stream:
        keywords: ["usage", "reasoning"]

      # ── 模型匹配模式 ──────────────────────────────────────────────
      model_matching: "wildcard"   # exact | wildcard
```

### 顶层字段

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `rules` | 数组 | 一条默认规则 | 检测规则，按顺序求值，首个命中执行。 |
| `defaults` | 对象 | 内置默认值 | 规则中省略字段时使用的默认值。 |
| `stream.keywords` | 数组 | `["usage", "reasoning"]` | 只解析包含这些关键词的 SSE chunk。留空 `[]` = 解析全部。 |
| `model_matching` | 枚举 | `wildcard` | `exact`（大小写不敏感精确匹配）或 `wildcard`（支持 `*`）。 |

### 规则字段

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `name` | 字符串 | `"unnamed"` | 规则名称，用于日志和统计。 |
| `paths` | 数组 | 见默认值 | 非流式响应中检查的 JSON 路径（gjson 语法），首个存在的路径生效。 |
| `stream_paths` | 数组 | 见默认值 | SSE 流式 chunk 的额外 JSON 路径（如 Responses API 的 `response.usage.*`）。 |
| `operator` | 枚举 | `exact` | 匹配算子——见下表。 |
| `values` | 数组 | `[516]` | 匹配值。`range` 用 `[min, max]`。 |
| `models` | 数组 | `[]`（全部） | 模型名称过滤。`wildcard` 模式下支持 `*` 通配符。 |
| `action` | 枚举 | `error` | `error`、`retry` 或 `log`。 |
| `error_status` | 整数 | `503` | 错误响应中的 HTTP 状态码。 |
| `error_body` | 字符串 | 模板 | Go `text/template` 渲染的错误响应体。 |
| `stream_error` | 字符串 | 模板 | Go `text/template` 渲染的 SSE 错误事件。 |
| `retry_max` | 整数 | `1` | `retry` 模式下的最大重试次数。 |
| `retry_backoff_ms` | 整数 | `0` | 重试之间的退避时间（毫秒）。 |
| `log` | 布尔 | `true` | 通过 `host.log` 记录检测事件。 |
| `log_level` | 字符串 | `warn` | 日志级别：`debug`、`info`、`warn`、`error`。 |
| `log_msg` | 字符串 | 模板 | Go `text/template` 渲染的日志消息。 |

### 匹配算子

| 算子 | 条件 | 示例 |
|------|------|------|
| `exact` | `tokens == values[0]` | `[516]` |
| `any_of` | `tokens 在 values 中` | `[516, 512, 508]` |
| `lte` | `tokens <= values[0]` | `[10]` |
| `gte` | `tokens >= values[0]` | `[1000]` |
| `range` | `values[0] <= tokens <= values[1]` | `[0, 100]` |
| `not_in` | `tokens 不在 values 中` | `[0, 1, 2]` |
| `always` | 始终匹配（用于日志） | （忽略） |

### 模板变量

在 `error_body`、`stream_error` 和 `log_msg` 中可用：

| 变量 | 说明 |
|------|------|
| `{{.Rule}}` | 规则名称 |
| `{{.Model}}` | 请求中的模型名称 |
| `{{.Tokens}}` | 检测到的 reasoning token 数 |
| `{{.Path}}` | 找到 token 的 JSON 路径 |
| `{{.Attempt}}` | 重试次数（0 表示初始检测） |

### 热重载

`config.yaml` 变更时，CLIProxyAPI 调用 `plugin.reconfigure` 传入新配置。插件：

1. 解析新 YAML
2. 预编译所有 `text/template` 实例
3. 通过 `atomic.Pointer` 原子替换配置指针

替换期间不阻塞任何请求。统计数据按键（规则名）跨重载持久化。

---

## Management API

插件注册了一个浏览器可访问的资源端点：

```
GET /v0/resource/plugins/respfilter/status
```

**无需认证**（资源路由是公开的）。返回 JSON：

```json
{
  "plugin": "respfilter",
  "version": "2.0.0",
  "enabled": true,
  "model_matching": "wildcard",
  "stream_keywords": ["usage", "reasoning"],
  "rules": [
    {
      "name": "openai-516",
      "operator": "exact",
      "values": [516],
      "action": "error",
      "models": ["gpt-5*"],
      "paths": ["usage.reasoning_tokens", "..."],
      "stream_paths": ["response.usage.output_tokens_details.reasoning_tokens", "..."],
      "retry_max": 1,
      "retry_backoff": 0,
      "log": true
    }
  ],
  "stats": {
    "openai-516": {
      "matches": 42,
      "retries": 0,
      "retry_successes": 0
    }
  }
}
```

在浏览器中打开：

```
http://localhost:8317/v0/resource/plugins/respfilter/status
```

---

## 工作原理

### 注册的能力

| 能力 | RPC 方法 | 用途 |
|------|----------|------|
| `response_after_translator` | `response.normalize_after` | 在翻译后检查非流式响应 |
| `response_stream_interceptor` | `response.intercept_stream_chunk` | 检查每个 SSE 流式 chunk |
| `usage_plugin` | `usage.handle` | 观察完成的 usage 记录 |
| `management_api` | `management.register` / `management.handle` | 状态端点 |

### 检测流程

**非流式：**
1. 响应被翻译为客户端协议格式
2. 插件检查所有配置的 JSON 路径（首个存在的路径生效）
3. Token 值按顺序与每条规则的算子匹配
4. 首个匹配的规则执行对应操作

**流式：**
1. 每个 SSE chunk 先经关键词预过滤（如 `"usage"`、`"reasoning"`）
2. 只有含关键词的 chunk 才进行 JSON 解析
3. 在 usage chunk 中发现降智特征时注入错误 SSE 事件

**重试：**
1. 插件调用 `host.model.execute` 重新执行原始请求
2. 宿主的重入保护在重试时跳过本插件的拦截器（避免无限循环）
3. 重试响应检查相同的降智特征
4. 首个非降智的重试结果返回；重试耗尽则回退到 error 模式

### 传输协议说明

- 所有 `[]byte` 字段在 JSON 传输中自动 base64 编解码（Go `encoding/json` 默认行为）
- 插件类型字段名匹配 `pluginapi` Go 结构体字段名（PascalCase，无 JSON tag）
- RPC 信封：`{"ok": true, "result": {...}}` 或 `{"ok": false, "error": {"code": "...", "message": "..."}}`

---

## 故障排查

### 插件未加载

| 症状 | 原因 | 解决方案 |
|------|------|----------|
| 日志中无 `respfilter` | `plugins.enabled: false` | 设置 `plugins.enabled: true` |
| 日志中无 `respfilter` | `configs.respfilter.enabled` 缺失或为 `false` | 在插件配置块下添加 `enabled: true` |
| `plugin not found` | 文件名或目录错误 | 文件必须为 `respfilter.so`，放在 `plugins/`、`plugins/linux/amd64/` 或 `plugins/linux/amd64-v3/` |
| `missing cliproxy_plugin_init` | 构建失败或构建模式错误 | 用 `CGO_ENABLED=1 go build -buildmode=c-shared` 重新构建 |
| `plugin ABI version N is not supported` | 构建的 CPA 版本不同 | 从源码重新构建；本插件硬编码 ABI 版本 1 |
| `cannot open shared object file` | 缺少 libc 依赖 | `sudo apt-get install -y libc6 libgcc-s1`；用 `ldd respfilter.so` 检查 |
| `wrong ELF class` | 架构不匹配 | 用与宿主匹配的 `GOARCH` 构建 |
| 符号链接未加载 | CPA 跳过符号链接 | 用 `cp` 而非 `ln -s` |

### 配置键大小写敏感

插件 ID 从**文件名**派生（保留大小写）。`RespFilter.so` → ID `RespFilter`，所以配置键必须是 `configs.RespFilter:`——不是 `configs.respfilter:`。

### 插件已加载但未检测

1. **检查 `enabled`** — `plugins.enabled: true` 和 `configs.respfilter.enabled: true` 都需要
2. **检查模型过滤** — 如果 `models: ["gpt-5*"]`，确保请求使用匹配的模型名
3. **检查 JSON 路径** — 验证响应实际包含配置的路径。在 CPA 配置中设 `debug: true` 并检查请求日志
4. **检查流式关键词** — 如果是流式请求，确保 `stream.keywords` 包含 usage chunk 中出现的子字符串
5. **检查状态端点** — `curl http://localhost:8317/v0/resource/plugins/respfilter/status` 显示活跃规则

### 检查共享库依赖

```bash
# 列出动态依赖
ldd respfilter.so

# 验证导出符号
nm -D respfilter.so | grep cliproxy
# 预期：
#   cliproxyPluginCall
#   cliproxyPluginFree
#   cliproxyPluginShutdown
#   cliproxy_plugin_init
```

### 在调试模式运行 CPA

```yaml
# config.yaml
debug: true
```

这会启用详细日志，包括插件注册详情和 RPC 调用。

---

## 文件结构

```
respfilter/
├── main.go                 C ABI 导出 + 方法分发（薄层——唯一 import "C" 的文件）
├── config.go               配置类型、YAML 解析、默认值继承、模板预编译
├── detect.go               匹配算子、通配符模型匹配、JSON 路径提取
├── handlers.go             响应/流/usage 处理器 + 规则匹配引擎 + 模板渲染
├── retry.go                通过 host.model.execute 的重试逻辑
├── management.go           Management API /status 端点
├── go.mod                  模块定义（仅 gjson + yaml.v3）
├── go.sum                  依赖校验和
├── Makefile                跨平台构建
├── config.snippet.yaml     完整带注释的配置示例
├── README.md               英文文档
└── README_CN.md            本文件（中文）
```

---

## 许可证

MIT
