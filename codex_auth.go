package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	authURLPattern     = regexp.MustCompile(`https://auth\.openai\.com/\S+`)
	loginServerPattern = regexp.MustCompile(`https?://localhost:\d+`)
	ansiRegex          = regexp.MustCompile(`\x1b\[[0-9;]*m`)
)

var authGuideSteps = []string{
	"点击下面按钮，会在新页面打开 ChatGPT 登录授权。",
	"在新页面完成授权，浏览器会跳转到一个 http://localhost:1455/auth/callback?... 链接。",
	"复制完整回调链接，回到当前页面粘贴，然后点击“完成授权”。",
}

const codexAuthSessionTTL = 15 * time.Minute

func newCodexAuthManager() *codexAuthManager {
	return &codexAuthManager{}
}

func stripANSI(text string) string {
	return ansiRegex.ReplaceAllString(text, "")
}

func codexLoginStatus() (bool, string) {
	cmd := exec.Command("codex", "login", "status")
	output, err := cmd.CombinedOutput()
	text := strings.TrimSpace(stripANSI(string(output)))
	if err != nil {
		if text == "" {
			text = err.Error()
		}
		return false, text
	}
	return strings.Contains(strings.ToLower(text), "logged in"), text
}

func isCodexAuthError(message string) bool {
	text := strings.ToLower(strings.TrimSpace(message))
	return strings.Contains(text, "not logged in") ||
		strings.Contains(text, "codex login") ||
		strings.Contains(text, "authentication") ||
		strings.Contains(text, "unauthorized") ||
		strings.Contains(text, "login required") ||
		strings.Contains(text, "logged out") ||
		strings.Contains(text, "expired")
}

func authGuideJSArray() string {
	parts := make([]string, 0, len(authGuideSteps))
	for _, step := range authGuideSteps {
		parts = append(parts, fmt.Sprintf("%q", step))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func authStatusMessage(message string) string {
	text := strings.TrimSpace(stripANSI(message))
	lower := strings.ToLower(text)
	switch {
	case text == "":
		return ""
	case strings.Contains(lower, "state mismatch"):
		return "这条回调链接不属于当前这次授权，请重新点击“打开授权页面”后再完成一次。"
	case strings.Contains(lower, "operation timed out"), strings.Contains(lower, "context deadline exceeded"):
		return "本次授权已超时，请重新点击“打开授权页面”。"
	case strings.Contains(lower, "callback url is required"):
		return "请先粘贴授权完成后的回调链接。"
	case strings.Contains(lower, "missing required authorization parameters"):
		return "回调链接不完整，请确认包含 code 和 state 参数。"
	case strings.Contains(lower, "port 127.0.0.1:1455 is already in use"):
		return "本机登录端口已被占用，请关闭旧的授权流程后重试。"
	case strings.Contains(lower, "not ready"):
		return "当前授权会话还没准备好，请重新点击“打开授权页面”。"
	case strings.Contains(lower, "invalid callback url"):
		return "回调链接格式不正确，请粘贴完整链接。"
	}
	return text
}

func isExpiredCodexAuthSession(session *codexAuthSession) bool {
	if session == nil {
		return false
	}
	if session.Status != "pending" && session.Status != "ready" {
		return false
	}
	return time.Since(session.StartedAt) > codexAuthSessionTTL
}

func newCodexAuthID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("auth-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", buf[:])
}

func newCodexAuthSession() *codexAuthSession {
	return &codexAuthSession{
		ID:        newCodexAuthID(),
		StartedAt: time.Now(),
		Status:    "pending",
	}
}

func (m *codexAuthManager) stopLocked() {
	if m.proc != nil && m.proc.Process != nil {
		_ = m.proc.Process.Kill()
	}
	m.proc = nil
}

func (m *codexAuthManager) expireLocked() {
	if !isExpiredCodexAuthSession(m.session) {
		return
	}
	m.stopLocked()
	m.session.Status = "failed"
	m.session.Error = authStatusMessage("operation timed out")
	log.Printf("auth session expired: id=%s", m.session.ID)
}

func (m *codexAuthManager) Status(force bool) codexAuthStatusResponse {
	loggedIn, _ := codexLoginStatus()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireLocked()
	if loggedIn && !force {
		return codexAuthStatusResponse{LoggedIn: true, Session: m.session}
	}
	if m.session == nil {
		return codexAuthStatusResponse{LoggedIn: loggedIn && !force}
	}
	copySession := *m.session
	copySession.Error = authStatusMessage(copySession.Error)
	return codexAuthStatusResponse{LoggedIn: loggedIn && !force, Session: &copySession}
}

func (m *codexAuthManager) EnsureStarted(ctx context.Context, force bool, restart bool) codexAuthStatusResponse {
	if loggedIn, _ := codexLoginStatus(); loggedIn && !force {
		return m.Status(false)
	}

	m.mu.Lock()
	m.expireLocked()
	if restart {
		m.stopLocked()
		m.session = nil
	}
	if m.session != nil && (m.session.Status == "pending" || m.session.Status == "ready") {
		copySession := *m.session
		copySession.Error = authStatusMessage(copySession.Error)
		m.mu.Unlock()
		return codexAuthStatusResponse{LoggedIn: false, Session: &copySession}
	}

	session := newCodexAuthSession()
	m.session = session
	m.mu.Unlock()
	log.Printf("auth session started: id=%s force=%t restart=%t", session.ID, force, restart)

	cmd := exec.Command("codex", "login")
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	var transcript bytes.Buffer
	if err := cmd.Start(); err != nil {
		m.mu.Lock()
		session.Status = "failed"
		session.Error = authStatusMessage(err.Error())
		copySession := *session
		m.mu.Unlock()
		return codexAuthStatusResponse{LoggedIn: false, Session: &copySession}
	}
	m.mu.Lock()
	m.proc = cmd
	m.mu.Unlock()

	var streamWG sync.WaitGroup
	streamWG.Add(2)
	go func() {
		defer streamWG.Done()
		m.captureAuthStream(session, stdout, &transcript)
	}()
	go func() {
		defer streamWG.Done()
		m.captureAuthStream(session, stderr, &transcript)
	}()
	go func() {
		err := cmd.Wait()
		streamWG.Wait()
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.proc == cmd {
			m.proc = nil
		}
		if session.Status != "pending" && session.Status != "ready" {
			return
		}
		if err == nil {
			if loggedIn, _ := codexLoginStatus(); loggedIn {
				session.Status = "complete"
				log.Printf("auth session completed: id=%s", session.ID)
				return
			}
		}
		session.Status = "failed"
		session.Error = authStatusMessage(strings.TrimSpace(stripANSI(transcript.String())))
		if session.Error == "" && err != nil {
			session.Error = authStatusMessage(err.Error())
		}
		log.Printf("auth session failed: id=%s error=%s", session.ID, session.Error)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		ready := session.AuthURL != ""
		copySession := *session
		m.mu.Unlock()
		if ready {
			copySession.Status = "ready"
			log.Printf("auth session ready: id=%s", copySession.ID)
			return codexAuthStatusResponse{LoggedIn: false, Session: &copySession}
		}
		time.Sleep(100 * time.Millisecond)
	}

	return m.Status(force)
}

func (m *codexAuthManager) captureAuthStream(session *codexAuthSession, r io.Reader, transcript *bytes.Buffer) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(stripANSI(scanner.Text()))
		if line == "" {
			continue
		}
		transcript.WriteString(line + "\n")
		m.mu.Lock()
		if session.AuthURL == "" {
			if match := authURLPattern.FindString(line); match != "" {
				session.AuthURL = match
				if parsed, err := url.Parse(match); err == nil {
					session.State = strings.TrimSpace(parsed.Query().Get("state"))
				}
			}
		}
		if session.Callback == "" {
			if match := loginServerPattern.FindString(line); match != "" {
				session.Callback = match + "/auth/callback"
			}
		}
		if session.AuthURL != "" && session.Status == "pending" {
			session.Status = "ready"
		}
		m.mu.Unlock()
	}
}

func codexAuthPageHTML(returnTo string, force bool) string {
	return fmt.Sprintf(`<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
  <title>ChatGPT Account Verification</title>
  <style>
    body { margin:0; min-height:100vh; display:flex; align-items:center; justify-content:center; background:#000; color:#f4f4f5; font-family:"PingFang SC","Noto Sans SC","Helvetica Neue",sans-serif; padding:24px; }
    .card { width:min(420px,100%%); padding:22px; border:1px solid #23262d; border-radius:22px; background:#101113; box-shadow:0 24px 50px rgba(0,0,0,.35); }
    .title { font-size:26px; font-weight:700; text-align:center; }
    .sub { margin-top:8px; color:#9b9ba1; font-size:13px; line-height:1.6; text-align:center; }
    .box { margin-top:16px; padding:16px; border-radius:16px; background:#0b0c0e; border:1px solid #1f2228; }
    .steps { display:grid; gap:10px; }
    .step { display:grid; grid-template-columns:26px 1fr; gap:10px; align-items:flex-start; }
    .step-num { width:26px; height:26px; border-radius:999px; background:#1b2334; color:#b9ccff; display:flex; align-items:center; justify-content:center; font-size:12px; font-weight:700; }
    .step-text { color:#d9dce3; font-size:13px; line-height:1.6; }
    .label { color:#8f95a3; font-size:11px; text-transform:uppercase; letter-spacing:.08em; }
    .input { width:100%%; margin-top:8px; min-height:116px; resize:vertical; box-sizing:border-box; border-radius:14px; border:1px solid #2a2f3a; background:#14171d; color:#eef2ff; padding:12px 14px; font:inherit; }
    .actions { display:grid; gap:10px; margin-top:12px; }
    .btn { display:flex; align-items:center; justify-content:center; height:46px; border-radius:14px; border:1px solid #2a2f3a; background:#1a1e27; color:#fff; text-decoration:none; }
    .btn.primary { background:#2348d8; border-color:#2348d8; }
    .btn.compact { width:180px; height:40px; margin:10px auto 0; }
    .btn[disabled] { opacity:.55; pointer-events:none; }
  </style>
</head>
<body>
  <div class="card">
    <div class="title">ChatGPT 账户验证</div>
    <div class="sub">按下面 3 步完成验证后，再回到这里提交回调链接。</div>
    <div class="box">
      <div class="steps" id="steps"></div>
    </div>
    <div class="box">
      <div class="label">Callback Link</div>
      <textarea id="callbackInput" class="input" placeholder="示例: http://localhost:1455/auth/callback?code=...&scope=...&state=..."></textarea>
      <div class="actions">
        <button id="completeAuth" class="btn" type="button">完成授权</button>
      </div>
    </div>
  </div>
  <script>
    const returnTo = %q;
    const force = %t;
    const authGuideSteps = %s;
    let pollTimer = null;
    let currentSessionId = "";
    function renderSteps() {
      const steps = document.getElementById("steps");
      steps.innerHTML = authGuideSteps.map((text, index) => {
        const button = index === 0 ? '<button id="openAuth" class="btn primary compact" type="button">打开授权页面</button>' : '';
        return '<div class="step"><div class="step-num">' + (index + 1) + '</div><div class="step-text">' + text + button + '</div></div>';
      }).join("");
      document.getElementById("openAuth").addEventListener("click", openAuth);
    }
    async function fetchStatus() {
      const res = await fetch("/api/codex-auth/status" + (force ? "?force=1" : ""), { credentials: "same-origin" });
      return res.json();
    }
    async function loadStatus() {
      const data = await fetchStatus();
      render(data);
      if (data.loggedIn && !force) {
        window.location.replace(returnTo);
      }
    }
    function render(data) {
      if (!data) return;
      if (data.loggedIn && !force) {
        document.getElementById("openAuth").disabled = true;
        return;
      }
      const session = data.session || {};
      if (session.id) {
        currentSessionId = session.id;
      }
    }
    async function openAuth() {
      const button = document.getElementById("openAuth");
      button.disabled = true;
      try {
        const query = new URLSearchParams();
        if (force) query.set("force", "1");
        query.set("restart", "1");
        const suffix = "?" + query.toString();
        const res = await fetch("/api/codex-auth/start" + suffix, { method: "POST", credentials: "same-origin" });
        const data = await res.json();
        render(data);
        if (data.loggedIn && !force) {
          window.location.replace(returnTo);
          return;
        }
        const authURL = data && data.session && data.session.authUrl;
        if (!authURL) {
          alert((data.session && data.session.error) || "当前没有可用的授权链接，请重试。");
          return;
        }
        window.open(authURL, "_blank", "noopener,noreferrer");
      } catch (err) {
        alert("生成授权链接失败。");
      } finally {
        button.disabled = false;
        if (!pollTimer) {
          poll();
        }
      }
    }
    async function completeAuth() {
      const button = document.getElementById("completeAuth");
      const input = document.getElementById("callbackInput");
      const callbackUrl = String(input.value || "").trim();
      if (!callbackUrl) {
        alert("请先粘贴授权完成后的回调链接。");
        return;
      }
      button.disabled = true;
      try {
        const res = await fetch("/api/codex-auth/complete", {
          method: "POST",
          credentials: "same-origin",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ sessionId: currentSessionId, callbackUrl }),
        });
        const data = await res.json();
        render(data);
        if (data.loggedIn) {
          alert("验证成功，正在返回。");
          window.location.replace(returnTo);
          return;
        }
        alert((data.session && data.session.error) || "授权还未完成，请检查回调链接是否完整。");
      } catch (err) {
        alert("提交回调链接失败。");
      } finally {
        button.disabled = false;
      }
    }
    function poll() {
      pollTimer = setInterval(async () => {
        const res = await fetch("/api/codex-auth/status" + (force ? "?force=1" : ""), { credentials: "same-origin" });
        const data = await res.json();
        render(data);
        if (data.loggedIn && !force) {
          clearInterval(pollTimer);
          pollTimer = null;
          window.location.replace(returnTo);
        }
      }, 2000);
    }
    renderSteps();
    document.getElementById("completeAuth").addEventListener("click", completeAuth);
    loadStatus();
  </script>
</body>
</html>`, returnTo, force, authGuideJSArray())
}

func codexAuthReturnURL(raw string) string {
	text := strings.TrimSpace(raw)
	if text == "" {
		return "/"
	}
	if parsed, err := url.Parse(text); err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		return parsed.String()
	}
	return "/"
}

func codexAuthForce(raw string) bool {
	text := strings.TrimSpace(strings.ToLower(raw))
	return text == "1" || text == "true" || text == "yes"
}

func codexAuthRestart(raw string) bool {
	return codexAuthForce(raw)
}

func codexAuthProxyTarget() string {
	return "http://" + net.JoinHostPort("127.0.0.1", "1455")
}

func completeCodexAuth(ctx context.Context, raw string) error {
	text := strings.TrimSpace(raw)
	if text == "" {
		return errors.New("callback url is required")
	}
	parsed, err := url.Parse(text)
	if err != nil {
		return fmt.Errorf("invalid callback url: %w", err)
	}
	values := parsed.Query()
	code := strings.TrimSpace(values.Get("code"))
	state := strings.TrimSpace(values.Get("state"))
	scope := strings.TrimSpace(values.Get("scope"))
	if code == "" || state == "" {
		return errors.New("callback url is missing required authorization parameters")
	}
	query := url.Values{}
	query.Set("code", code)
	query.Set("state", state)
	if scope != "" {
		query.Set("scope", scope)
	}
	target := codexAuthProxyTarget() + "/auth/callback?" + query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return errors.New(msg)
	}
	return nil
}
