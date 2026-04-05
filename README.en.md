<p align="right">
  <a href="./README.md">中文</a> | English
</p>

# <p align="center">Code Web</p>

<p align="center">
  <img alt="Go Version" src="https://img.shields.io/badge/Go-1.22%2B-00ADD8?logo=go&logoColor=white">
  <img alt="Version" src="https://img.shields.io/badge/version-v1.2.0-111827">
  <img alt="GitHub Repo stars" src="https://img.shields.io/github/stars/klsf/code-web?style=social">
</p>

`Code Web` is a coding assistant Web UI built with `Go + HTML + WebSocket`, currently supporting both `Codex` and `Claude`.

It targets mobile and desktop browsers, and aims to bring the continuous session experience of local coding assistant CLIs into the browser:

- Tasks keep running on the server after the browser is closed
- Reopening the page restores the latest chat automatically

## Screenshots

<img alt="Desktop screenshot" width="500" src="./screen1.png" />
<img alt="Mobile screenshot" height="252" src="./screen2.png" />

## Features

- In `codex` mode it runs on top of `codex app-server`, instead of starting a fresh standalone CLI process for every message
- Supports resumable sessions through the `Claude` headless CLI
- Local sessions are persisted under `data/sessions/` and can be restored after refresh or reopen
- Supports sending images together with messages
- Supports streaming output, task events, auto-scroll, and automatic reconnect
- Supports basic Markdown rendering
- Supports provider-specific model lists, default models, and default provider selection
- Can control whether event messages are persisted through `config.json`
- Frontend static assets are embedded into the binary, so no separate `static/` directory is required

## Requirements

1. Go `1.22+`
2. The matching provider CLI must be directly executable on the machine
   - `codex` mode requires `codex`
   - `claude` mode requires `claude`
3. If you use `codex` mode, `codex login` must already be completed

## Config Files

By default, the program reads these three config files from the working directory where the binary runs:

- `config.json`
  - Controls the app name, login password, listen address, default provider, model lists, and whether event messages are persisted
  - `password` is used as the Web login password
  - `listen` is the HTTP listen address, for example `:8080` or `0.0.0.0:8080`
  - When `persistEvents` is `true`, task events are written into `data/sessions/*.json`
  - When `persistEvents` is `false`, task events are still pushed to the frontend in real time, but are not saved into local session files
- `claude-settings.json`
  - Configuration for Claude sessions
  - You can use `claude-settings.json` to configure environment variables such as proxies, models, and related settings
- `codex-settings.json`
  - Currently used to inject extra environment variables into `codex app-server`

Example `config.json`:

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

## Start

```bash
go build -o code-web
./code-web
```

For deployment you only need the binary itself and the runtime `data/` directory.

Default listen address:

```text
0.0.0.0:8080
```

## Login Password

The login password is configured through `password` in `config.json`.

If not specified, the default password is:

```text
codex
```

## Access

Open this URL in your browser:

```text
http://YOUR_SERVER_IP:8080
```

If you change `listen` in `config.json`, adjust the port here accordingly.

## Reverse Proxy

If you put it behind Nginx, you at least need to forward WebSocket traffic:

```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header Host $host;
}
```

## Notes

- This project is not an official OpenAI product
- It is a Codex / Claude Web wrapper intended for personal deployment
- The current implementation prioritizes continuous sessions, recovery, and mobile usability

## License

This project is licensed under `MIT`. See [LICENSE](./LICENSE).
