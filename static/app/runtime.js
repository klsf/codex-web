function setTransportState(state) {
  transportBadge.textContent = state;
  statusTransport.textContent = state;
  if (!isRunning) {
    footerState.textContent = state === "connected" ? "ready" : state;
    footerDetail.textContent = transportDetail(state);
  }
  scheduleStatusRefresh(0);
}

function showLoginScreen() {
  isAuthenticated = false;
  document.body.classList.add("auth-required");
  loginScreen.hidden = false;
  sessionChooser.hidden = true;
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

function setTaskState(running) {
  isRunning = running;
  imageBtn.disabled = running;
  updateSendState();
  statusTask.textContent = running ? "running" : "idle";
  if (running) {
    footerState.textContent = "Working";
    footerDetail.textContent = "Codex 正在执行任务，可输入 /stop 终止";
    input.placeholder = "任务执行中，输入 /stop 终止";
    scheduleStatusRefresh(0);
    return;
  }
  footerState.textContent = transportBadge.textContent === "connected" ? "ready" : transportBadge.textContent;
  footerDetail.textContent = "等待输入";
  input.placeholder = "发送消息...";
  scheduleStatusRefresh(0);
}

function setSession(id) {
  currentSessionId = id;
  localStorage.setItem("codex_session_id", id);
  sessionBadge.textContent = id.slice(0, 8);
  statusSession.textContent = shortSession(id);
  scheduleStatusRefresh(0);
}

function setMeta(meta) {
  if (!meta) return;
  if (meta.model) modelBadge.textContent = meta.model;
  if (meta.cwd) cwdBadge.textContent = meta.cwd;
  if (meta.model) statusModel.textContent = meta.model;
  if (meta.cwd) statusCwd.textContent = meta.cwd;
  scheduleStatusRefresh(0);
}

function autoResize() {
  input.style.height = "auto";
  input.style.height = Math.min(input.scrollHeight, 132) + "px";
}

function updateSendState() {
  var hasContent = String(input.value || "").trim().length > 0;
  var hasImages = pendingImages.length > 0;
  sendBtn.disabled = isRunning || (!hasContent && !hasImages);
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

function scheduleStatusRefresh(delay) {
  if (!isAuthenticated) return;
  clearTimeout(statusRefreshTimer);
  statusRefreshTimer = setTimeout(function () {
    refreshSidebarStatus().catch(function () {});
  }, delay == null ? 250 : delay);
}

function startWorkingTimer() {
  if (workingTimer) return;
  workingTimer = setInterval(updateWorkingElapsed, 100);
}

function stopWorkingTimer() {
  if (workingTimer) {
    clearInterval(workingTimer);
    workingTimer = null;
  }
  workingStartedAt = 0;
}

function updateWorkingElapsed() {
  if (!workingElapsed || !workingStartedAt) return;
  var seconds = Math.max(0, (Date.now() - workingStartedAt) / 1000);
  workingElapsed.textContent = Math.floor(seconds) + "s";
}

function ensureWorkingPlaceholder() {
  if (!workingStartedAt) {
    workingStartedAt = Date.now();
  }
  renderWorkingLabel("Working...");
  if (workingDock) {
    workingDock.hidden = false;
  }
  updateWorkingElapsed();
  startWorkingTimer();
  scrollToBottom();
}

function removeWorkingPlaceholder() {
  if (workingDock) {
    workingDock.hidden = true;
  }
  if (workingLabel) {
    workingLabel.innerHTML = "";
  }
  if (workingElapsed) {
    workingElapsed.textContent = "";
  }
  stopWorkingTimer();
}

function shortSession(id) {
  return id ? String(id).slice(0, 8) : "unknown";
}

function resumeSummary(item) {
  var text = compact(item.lastMessage || "");
  var count = Number(item.messageCount || 0);
  var updated = item.updatedAt ? formatTime(item.updatedAt) : "--:--";
  var workdir = compact(item.workdir || "");
  return [workdir || "无工作目录", text || "无消息记录", count + " 条消息", updated].join(" · ");
}
