function setTransportState(state) {
  transportBadge.textContent = state;
  if (desktopTransportBadge) desktopTransportBadge.textContent = state;
  statusTransport.textContent = state;
  if (!isRunning) {
    setFooterStatus(state === "connected" ? "ready" : state, transportDetail(state));
  }
}

function showLoginScreen() {
  isAuthenticated = false;
  document.body.classList.add("auth-required");
  loginScreen.hidden = false;
  sessionChooser.hidden = true;
  codexAuthScreen.hidden = true;
  loginError.textContent = "";
  timeline.innerHTML = "";
  removeWorkingPlaceholder();
  clearTimeout(statusRefreshTimer);
  setTimeout(function () {
    passwordInput.focus();
  }, 0);
}

function hideLoginScreen() {
  isAuthenticated = true;
  document.body.classList.remove("auth-required");
  loginScreen.hidden = true;
  loginError.textContent = "";
  passwordInput.value = "";
}

function showSessionChooser() {
  document.body.classList.add("auth-required");
  sessionChooser.hidden = false;
  codexAuthScreen.hidden = true;
  resumeList.hidden = true;
  resumeList.innerHTML = "";
  resumeEmpty.hidden = true;
}

function hideSessionChooser() {
  document.body.classList.remove("auth-required");
  sessionChooser.hidden = true;
  resumeList.hidden = true;
  resumeList.innerHTML = "";
  resumeEmpty.hidden = true;
}

function buildCodexAuthLink() {
  return "/codex-auth?return=" + encodeURIComponent(window.location.href);
}

function authGuideStepsConfig() {
  var config = window.__APP_CONFIG || {};
  return Array.isArray(config.authGuideSteps) ? config.authGuideSteps : [];
}

async function submitCodexAuthCallback(callbackUrl) {
  var res = await fetch("/api/codex-auth/complete", {
    method: "POST",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ sessionId: currentCodexAuthSessionId, callbackUrl: String(callbackUrl || "").trim() }),
  });
  var data = await res.json();
  if (!res.ok && (!data || !data.session || !data.session.error)) {
    throw new Error("提交回调链接失败");
  }
  return data;
}

async function openCodexAuthLink(force) {
  var params = new URLSearchParams();
  if (force) params.set("force", "1");
  params.set("restart", "1");
  var query = "?" + params.toString();
  var res = await fetch("/api/codex-auth/start" + query, { method: "POST", credentials: "same-origin" });
  var data = await res.json();
  if (!res.ok) {
    throw new Error((data && data.session && data.session.error) || "生成授权链接失败");
  }
  return data;
}

function renderAuthGuide(container, buttonId, buttonClass) {
  if (!container) return;
  var steps = authGuideStepsConfig();
  container.innerHTML = "";
  steps.forEach(function (text, index) {
    var row = document.createElement("div");
    row.className = "resume-item";
    row.style.marginTop = index === 0 ? "12px" : "10px";
    var buttonHtml = index === 0
      ? '<div style="margin-top:10px;text-align:center"><button id="' + buttonId + '" class="' + buttonClass + '" type="button">打开授权页面</button></div>'
      : "";
    row.innerHTML = '<div class="resume-open" style="cursor:default">' +
      '<div class="resume-item-title">步骤 ' + (index + 1) + '</div>' +
      '<div class="resume-item-desc">' + text + '</div>' +
      buttonHtml +
      '</div>';
    container.appendChild(row);
  });
}

async function showCodexAuthScreen(message) {
  document.body.classList.add("auth-required");
  codexAuthScreen.hidden = false;
  loginScreen.hidden = true;
  sessionChooser.hidden = true;
  currentCodexAuthSessionId = "";
  renderAuthGuide(codexAuthSteps, "codexAuthLink", "login-button login-button-compact");
  if (codexAuthInput) codexAuthInput.value = "";
  codexAuthLink = document.getElementById("codexAuthLink");
  if (codexAuthHint) {
    codexAuthHint.textContent = "";
  }
  var status = await checkCodexAuthStatus().catch(function () { return null; });
  if (status && status.loggedIn) {
    if (codexAuthLink) {
      codexAuthLink.disabled = true;
      codexAuthLink.textContent = "当前设备已登录";
    }
    if (codexAuthHint) {
      codexAuthHint.textContent = "";
    }
    return;
  }
  if (status && status.session) {
    if (status.session.id) {
      currentCodexAuthSessionId = status.session.id;
    }
    if (codexAuthHint && status.session.error) {
      codexAuthHint.textContent = "";
    }
  }
}

function hideCodexAuthScreen() {
  codexAuthScreen.hidden = true;
}

function isCodexAuthError(message) {
  var text = String(message || "").toLowerCase();
  return text.includes("not logged in") ||
    text.includes("codex login") ||
    text.includes("authentication") ||
    text.includes("unauthorized") ||
    text.includes("login required") ||
    text.includes("logged out") ||
    text.includes("expired");
}

function setTaskState(running) {
  isRunning = running;
  imageBtn.disabled = running;
  updateSendState();
  statusTask.textContent = running ? "running" : "idle";
  if (running) {
    setFooterStatus("Working", "Codex 正在执行任务，可输入 /stop 终止");
    input.placeholder = "发送消息...";
    return;
  }
  setFooterStatus(transportBadge.textContent === "connected" ? "ready" : transportBadge.textContent, "等待输入");
  input.placeholder = "发送消息...";
}

function setSession(id) {
  currentSessionId = id;
  localStorage.setItem("codex_session_id", id);
  sessionBadge.textContent = id.slice(0, 8);
  if (desktopSessionBadge) desktopSessionBadge.textContent = id.slice(0, 8);
  statusSession.textContent = shortSession(id);
}

function setMeta(meta) {
  if (!meta) return;
  if (meta.model) modelBadge.textContent = meta.model;
  if (meta.cwd) cwdBadge.textContent = meta.cwd;
  if (meta.model) statusModel.textContent = meta.model;
  if (meta.cwd) statusCwd.textContent = meta.cwd;
  statusApprovals.textContent = meta.approvalPolicy || statusApprovals.textContent || "never";
  statusFast.textContent = meta.fastMode ? "on" : "off";
  statusServiceTier.textContent = meta.serviceTier || "default";
}

function autoResize() {
  input.style.height = "auto";
  input.style.height = Math.min(input.scrollHeight, 132) + "px";
}

function updateSendState() {
  var hasContent = String(input.value || "").trim().length > 0;
  var hasImages = pendingImages.length > 0;
  var commandToken = commandQuery(input.value || "");
  var exactCommand = commands.find(function (item) {
    return item.name === commandToken || (item.aliases || []).includes(commandToken);
  });
  var canSubmitWhileRunning = Boolean(exactCommand && exactCommand.name === "/stop");
  sendBtn.disabled = (isRunning && !canSubmitWhileRunning) || (!hasContent && !hasImages);
}

function canAcceptImageFile(file) {
  return Boolean(file && typeof file.type === "string" && file.type.toLowerCase().startsWith("image/"));
}

function addPendingImageFiles(files) {
  var added = false;
  (files || []).forEach(function (file) {
    if (!canAcceptImageFile(file)) {
      return;
    }
    pendingImages.push({
      file: file,
      url: URL.createObjectURL(file),
    });
    added = true;
  });
  if (!added) {
    return false;
  }
  renderAttachmentTray();
  updateSendState();
  return true;
}

function compact(text) {
  return String(text || "").replace(/\s+/g, " ").trim().slice(0, 120) || "等待输入";
}

function applyBuildInfo() {
  var config = window.__APP_CONFIG || {};
  var version = String(config.version || "dev").trim();
  if (!version) version = "dev";
  if (version.charAt(0) !== "v") {
    version = "v" + version;
  }
  Array.from(versionNodes || []).forEach(function (node) {
    node.textContent = version;
  });
}

function showError(message) {
  var text = compact(message || "操作失败");
  setFooterStatus("error", text);
  if (isCodexAuthError(text)) {
    showCodexAuthScreen("Codex CLI 授权已失效，请重新授权。");
  }
  if (!errorToast) {
    return;
  }
  errorToast.textContent = text;
  errorToast.hidden = false;
  clearTimeout(errorToastTimer);
  errorToastTimer = setTimeout(function () {
    errorToast.hidden = true;
  }, 3200);
}

function shouldRenderMarkdown(message) {
  return Boolean(message && message.role === "assistant");
}

function renderMarkdown(node, text) {
  var bubble = node.querySelector(".bubble");
  var source = String(text || "");
  var markedLib = window.marked;
  var purifier = window.DOMPurify;
  if (!markedLib || !purifier) {
    bubble.textContent = source;
    return;
  }
  markedLib.setOptions({
    breaks: true,
    gfm: true,
  });
  var html = markedLib.parse(source);
  bubble.innerHTML = purifier.sanitize(html);
}

function firstLine(text) {
  return String(text || "").split("\n")[0];
}

function transportDetail(state) {
  if (state === "connected") return "等待输入";
  if (state === "connecting") return "正在建立连接";
  if (state === "reconnecting") return "正在恢复连接";
  return "连接不可用";
}

function formatTime(value) {
  if (!value) return "--:--";
  return new Date(value).toLocaleString("zh-CN", {
    hour12: false,
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
}

function formatReset(ts) {
  if (!ts) return "unknown";
  return new Date(ts * 1000).toLocaleString("zh-CN", {
    hour12: false,
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
}

function remainText(windowData) {
  var used = Number(windowData.usedPercent || 0);
  var remain = Math.max(0, 100 - used);
  return remain + "% left, used " + used + "%, reset " + formatReset(windowData.resetsAt);
}

function creditText(credits) {
  if (credits.unlimited) return "unlimited";
  if (credits.hasCredits && credits.balance != null) return String(credits.balance);
  return "none";
}

function applyStatus(data) {
  if (!data) return;
  statusSession.textContent = shortSession(data.sessionId || currentSessionId);
  statusModel.textContent = data.model || modelBadge.textContent || "unknown";
  statusCwd.textContent = data.cwd || cwdBadge.textContent || "/";
  statusTransport.textContent = data.transport || transportBadge.textContent || "connecting";
  statusTask.textContent = data.task || (isRunning ? "running" : "idle");
  statusApprovals.textContent = data.approvalPolicy || "never";
  statusFast.textContent = data.fastMode ? "on" : "off";
  statusServiceTier.textContent = data.serviceTier || "default";
  statusPlan.textContent = data.rateLimits && data.rateLimits.planType ? data.rateLimits.planType : "-";
  statusPrimary.textContent = data.rateLimits && data.rateLimits.primary ? remainText(data.rateLimits.primary) : "-";
  statusSecondary.textContent = data.rateLimits && data.rateLimits.secondary ? remainText(data.rateLimits.secondary) : "-";
  statusCredits.textContent = data.rateLimits && data.rateLimits.credits ? creditText(data.rateLimits.credits) : "-";
}

async function refreshSidebarStatus() {
  if (!currentSessionId) return;
  var res = await fetch("/api/status?sessionId=" + encodeURIComponent(currentSessionId));
  if (!res.ok) {
    throw new Error(await res.text());
  }
  var data = await res.json();
  applyStatus(data);
}

async function checkCodexAuthStatus() {
  var res = await fetch("/api/codex-auth/status", { credentials: "same-origin" });
  if (!res.ok) {
    throw new Error(await res.text());
  }
  return res.json();
}

function scheduleStatusRefresh(delay) {
  if (!isAuthenticated) return;
  clearTimeout(statusRefreshTimer);
  statusRefreshTimer = setTimeout(function () {
    refreshSidebarStatus().catch(function () {});
  }, delay == null ? 250 : delay);
}

function setFooterStatus(state, detail) {
  renderFooterState(state);
  if (detail != null) {
    footerDetail.textContent = detail;
  }
}

function renderFooterState(state) {
  var text = String(state || "").trim() || "ready";
  footerState.textContent = "";
  footerState.classList.toggle("is-animated", text.toLowerCase() === "working");
  if (text.toLowerCase() !== "working") {
    footerState.textContent = text;
    return;
  }
  var wrap = document.createElement("span");
  wrap.className = "working-text working-marquee";
  Array.from(text).forEach(function (char, index) {
    var node = document.createElement("span");
    node.className = "working-char";
    node.style.animationDelay = (index * 0.12) + "s";
    node.textContent = char;
    wrap.appendChild(node);
  });
  footerState.appendChild(wrap);
}

function ensureWorkingPlaceholder() {
  return;
}

function removeWorkingPlaceholder() {
  return;
}

function shortSession(id) {
  return id ? String(id).slice(0, 8) : "unknown";
}

function resumeSummary(item) {
  var parts = [];
  if (item.running) parts.push("运行中");
  parts.push(compact(item.workdir || "") || "无工作目录");
  if (item.lastMessage) {
    parts.push(compact(item.lastMessage));
  } else if (item.lastEvent) {
    parts.push(compact(item.lastEvent));
  } else {
    parts.push("无消息记录");
  }
  parts.push(Number(item.messageCount || 0) + " 条消息");
  parts.push(item.updatedAt ? formatTime(item.updatedAt) : "--:--");
  return parts.join(" · ");
}

function resumeWorkdir(item) {
  return compact(item && item.workdir || "") || "无工作目录";
}

function resumeActivity(item) {
  if (item && item.lastMessage) {
    return compact(item.lastMessage);
  }
  if (item && item.lastEvent) {
    return compact(item.lastEvent);
  }
  return "无消息记录";
}
