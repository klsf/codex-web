# codex-web

`codex-web` 是一个基于 `Go + HTML + WebSocket` 的 Codex Web UI。

它面向移动端和桌面浏览器，提供接近 Codex CLI 的连续会话体验：

- 浏览器关闭后，任务继续在服务端执行
- 重新打开页面后，自动恢复最新聊天内容
- 同一个会话会复用同一个 Codex thread，保留上下文
- 支持移动端聊天界面，也支持桌面端左侧状态栏

## 特性

- 基于 `codex app-server`，不是每条消息都重新跑一次独立 CLI
- 会话持久化到 `data/sessions/*.json`
- 浏览器本地保存 `sessionId`，下次打开优先回到上次会话
- 登录保护，未登录时只显示密码页
- 新建会话时可指定工作目录
- 支持图片随消息一起发送
- 支持流式输出、`Working...` 状态行、自动重连
- 支持桌面端左侧状态栏，显示 `/status` 信息
- 支持基础 Markdown 渲染
- 前端静态资源内嵌进 Go 二进制，发布时不需要额外携带 `static/`

## 当前支持的命令

- `/status`
- `/model`
- `/fast`
- `/skills`
- `/resume`
- `/clear`
- `/compact`
- `/stop`
- `/delete`
- `/new`
- `/logout`

其中一部分命令是本地 UI 命令，一部分会调用后端接口或 `codex app-server`。

## 运行要求

1. Go `1.22+`
2. 机器上可直接执行 `codex`
3. 已完成 `codex login`
4. `codex app-server` 可用

## 启动

```bash
cd /www/codex
go build -o codex-web .
./codex-web
```

构建后的 `codex-web` 二进制已经包含前端静态资源，部署时只需要二进制本体，以及运行期会写入的 `data/` 目录。

默认监听：

```text
0.0.0.0:991
```

默认会尝试连接：

```text
ws://127.0.0.1:8765
```

也就是本机上的 `codex app-server`。

## 登录密码

可通过启动参数设置登录密码：

```bash
./codex-web -password "123456"
```

如果没有指定，默认密码是：

```text
codex
```

## 访问

浏览器打开：

```text
http://你的服务器IP:991
```

登录成功后：

- 如果浏览器本地保存的 `sessionId` 仍然存在，会直接进入该会话
- 如果本地没有会话，或者会话已经被删除，会进入“新建会话 / 恢复会话”页面

## 工作目录

新建会话时可以输入工作目录，例如：

```text
/www/codex
```

这个目录会按 session 保存，并影响：

- `/status` 里的 `cwd`
- 后续 Codex 对话所在目录
- `thread/start` / `thread/resume` 的 `cwd`
- `/review` 这类依赖工作目录的任务

## 数据目录

- 会话数据：`data/sessions/`
- 上传图片：`data/uploads/`

## 项目结构

- [main.go](/www/codex/main.go)
  Go 后端，负责 HTTP、WebSocket、会话存储、命令处理、`codex app-server` 通信
- [static/index.html](/www/codex/static/index.html)
  页面结构
- [static/app.js](/www/codex/static/app.js)
  前端交互、命令面板、流式渲染、登录流程
- [static/style.css](/www/codex/static/style.css)
  移动端和桌面端样式

## 反向代理

如果放在 Nginx 后面，至少要转发 WebSocket：

```nginx
location / {
    proxy_pass http://127.0.0.1:991;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header Host $host;
}
```

## 说明

- 这个项目不是官方 OpenAI 产品
- 它是一个面向个人部署的 Codex Web 外壳
- 当前实现优先保证连续会话、恢复能力和移动端可用性

## License

本项目使用 `MIT` 许可证，见 [LICENSE](/www/codex/LICENSE)。

## English

`codex-web` is a lightweight web UI for Codex built with `Go + HTML + WebSocket`.

It is designed for both mobile and desktop browsers and focuses on resumable, long-lived Codex sessions:

- tasks keep running on the server after the browser page is closed
- reopening the page restores the latest chat content
- the same session keeps the same Codex thread and conversation context
- desktop layout includes a sidebar for status information

### Highlights

- powered by `codex app-server`
- persistent sessions stored in `data/sessions/*.json`
- browser-local `sessionId` restore
- simple password-based login
- per-session working directory
- image upload with message send
- streaming output and reconnect support
- Markdown rendering for final assistant replies

### Run

```bash
cd /www/codex
go build -o codex-web .
./codex-web
```

Default listen address:

```text
0.0.0.0:991
```

Default login password:

```text
codex
```

You can override it with:

```bash
./codex-web -password "your-password"
```
