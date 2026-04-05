<p align="right">
  中文 | <a href="./README.en.md">English</a>
</p>

# <p align="center">Code Web</p>

<p align="center">
  <img alt="Go Version" src="https://img.shields.io/badge/Go-1.22%2B-00ADD8?logo=go&logoColor=white">
  <img alt="Version" src="https://img.shields.io/badge/version-v1.2.1-111827">
  <img alt="GitHub Repo stars" src="https://img.shields.io/github/stars/klsf/code-web?style=social">
</p>
`Code Web` 是一个基于 `Go + HTML + WebSocket` 构建的代码助手 Web UI，目前支持 `Codex` 和 `Claude`。

它面向移动端和桌面浏览器，目标是把本地代码助手 CLI 的连续会话体验搬到浏览器里：

- 浏览器关闭后，任务继续在服务端执行
- 重新打开页面后，自动恢复最新聊天内容

## 界面截图

<img alt="桌面端截图" width="500" src="./screen1.png" />
<img alt="移动端截图" height="252" src="./screen2.png" />

## 特性

- 在 `codex` 模式下，基于 `codex app-server` 运行，而不是每次消息都启动一个全新的独立 CLI 进程
- 通过 `Claude` 的 headless CLI 支持可恢复会话
- 本地会话会持久化到 `data/sessions/`，浏览器刷新或重开后可继续恢复
- 支持发送图片
- 支持流式输出、任务过程事件、自动滚动和自动重连
- 支持基础 Markdown 渲染
- 支持按 provider 配置模型列表、默认模型和默认工具
- 可通过 `config.json` 控制是否持久化 event 过程消息
- 前端静态资源已打包进二进制，无需额外携带 `static/` 目录

## 运行要求

1. Go `1.22+`
2. 机器上可直接执行对应 provider 的 CLI
   - `codex` 模式：需要 `codex`
   - `claude` 模式：需要 `claude`
3. 如果使用 `codex` 模式，需要先完成 `codex login`

## 配置文件

默认情况下，程序会从二进制运行目录读取下面三个配置文件：

- `config.json`
  - 用于配置应用名、登录密码、监听地址、默认 provider、模型列表，以及是否持久化 event
  - `password` 用于 Web 登录密码
  - `listen` 用于 HTTP 服务监听地址，例如 `:8080` 或 `0.0.0.0:8080`
  - `persistEvents` 为 `true` 时，过程事件会写入 `data/sessions/*.json`
  - `persistEvents` 为 `false` 时，过程事件仍会实时推送到前端，但不会写入本地会话文件
- `claude-settings.json`
  - 用于配置 Claude 会话
  - 可通过 `claude-settings.json` 配置代理、模型以及相关环境变量
- `codex-settings.json`
  - 当前用于向 `codex app-server` 注入额外环境变量

示例 `config.json`：

```json
{
  "appName": "Code Web",
  "password": "codex",
  "listen": ":8080",
  "provider": "codex",
  "persistEvents": false,
  "providers": [
    {
      "id": "claude",
      "name": "Claude",
      "defaultModel": "opus",
      "models": ["opus", "sonnet", "haiku"]
    },
    {
      "id": "codex",
      "name": "Codex",
      "isDefault": true,
      "defaultModel": "gpt-5.4",
      "models": ["gpt-5.4", "gpt-5.3-codex", "gpt-5.4-mini"]
    }
  ]
}
```

## 启动

```bash
go build -o code-web 
./code-web
```

部署时只需要保留二进制本体，以及运行期间会写入的 `data/` 目录。

默认监听：

```text
0.0.0.0:8080
```

## 登录密码

登录密码通过 `config.json` 中的 `password` 配置。

如果没有指定，默认密码是：

```text
codex
```

## 访问

浏览器打开：

```text
http://你的服务器IP:8080
```

如果你修改了 `config.json` 里的 `listen`，这里的端口也要对应调整。

## 反向代理

如果放在 Nginx 后面，至少要转发 WebSocket：

```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header Host $host;
}
```

## 说明

- 这个项目不是官方 OpenAI 产品
- 它是一个面向个人部署的 Codex / Claude Web 外壳
- 当前实现优先保证连续会话、恢复能力和移动端可用性

## License

本项目使用 `MIT` 许可证，见 [LICENSE](./LICENSE)。
