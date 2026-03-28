function ensureSocket() {
  clearTimeout(reconnectTimer);
  if (ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)) {
    return;
  }
  if (ws) {
    wsIntentionalClose = true;
    try {
      ws.close();
    } catch (err) {}
    ws = null;
  }
  connect();
}

async function createSession(workdir, connectNow) {
  var res = await fetch("/api/session/new", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ workdir: String(workdir || "").trim() }),
  });
  if (!res.ok) {
    throw new Error(await res.text());
  }
  var data = await res.json();
  setSession(data.sessionId);
  replaceTimeline([], []);
  setTaskState(false);
  setFooterStatus("ready", "等待输入");
  scheduleStatusRefresh(0);
  if (ws) {
    var current = ws;
    ws = null;
    wsIntentionalClose = true;
    current.close();
  }
  if (connectNow !== false) {
    ensureSocket();
  }
}

async function submitPrompt(raw) {
  var content = (raw == null ? input.value : raw).trim();
  if ((!content && !pendingImages.length) || !currentSessionId) return;
  var commandToken = commandQuery(content);
  var exactCommand = commands.find(function (item) {
    return item.name === commandToken || (item.aliases || []).includes(commandToken);
  });
  if (isRunning && (!exactCommand || exactCommand.name !== "/stop")) return;
  if (exactCommand) {
    await executeCommand(exactCommand);
    return;
  }
  hideCommandPalette();
  var formData = new FormData();
  formData.append("sessionId", currentSessionId);
  formData.append("content", content);
  pendingImages.forEach(function (item) {
    formData.append("images", item.file, item.file.name);
  });

  ensureSocket();
  setTaskState(true);
  setFooterStatus("Working", compact(content || "发送图片"));
  ensureWorkingPlaceholder();
  try {
    var res = await fetch("/api/send", { method: "POST", body: formData });
    if (!res.ok) {
      throw new Error(await res.text());
    }
    input.value = "";
    pendingImages = [];
    renderAttachmentTray();
    autoResize();
    hideCommandPalette();
  } catch (err) {
    removeWorkingPlaceholder();
    setTaskState(false);
    showError(err && err.message ? err.message : "发送失败");
  }
}

function connect() {
  clearTimeout(reconnectTimer);
  setTransportState("connecting");

  var protocol = location.protocol === "https:" ? "wss:" : "ws:";
  wsIntentionalClose = false;
  ws = new WebSocket(protocol + "//" + location.host + "/ws");
  var socket = ws;

  socket.addEventListener("open", function () {
    if (ws !== socket) return;
    wsIntentionalClose = false;
    setTransportState("connected");
    socket.send(JSON.stringify({ type: "hello", sessionId: currentSessionId }));
    scheduleStatusRefresh(0);
  });

  socket.addEventListener("message", function (evt) {
    if (ws !== socket) return;
    var data = JSON.parse(evt.data);
    if (data.type === "snapshot" && data.session) {
      setSession(data.session.id);
      setMeta(data.meta);
      replaceTimeline(data.session.messages || [], data.session.events || [], data.session.draftMessage || null);
      setTaskState(Boolean(data.running));
      scheduleStatusRefresh(0);
      if (data.running && !data.session.draftMessage) {
        ensureWorkingPlaceholder();
      }
      return;
    }
    if (data.type === "meta_update" && data.meta) {
      setMeta(data.meta);
      return;
    }
    if (data.type === "message" && data.message) {
      renderMessage(data.message, { animate: false });
      return;
    }
    if (data.type === "message_delta" && data.message) {
      removeWorkingPlaceholder();
      renderMessage(data.message, { draft: true, animate: false });
      return;
    }
    if (data.type === "message_final" && data.message) {
      removeWorkingPlaceholder();
      removeOtherDrafts(data.message.id);
      renderMessage(data.message, { draft: false, animate: false });
      return;
    }
    if (data.type === "log" && data.log) {
      if (eventBody(data.log)) {
        renderEvent(data.log, { animate: false });
      }
      return;
    }
    if (data.type === "task_status") {
      setTaskState(Boolean(data.running));
      if (data.running) {
        ensureWorkingPlaceholder();
      } else {
        removeWorkingPlaceholder();
      }
      return;
    }
    if (data.type === "error" && data.error) {
      showError(data.error);
    }
  });

  socket.addEventListener("close", function () {
    if (ws === socket) {
      ws = null;
    }
    if (wsIntentionalClose) {
      wsIntentionalClose = false;
      return;
    }
    setTransportState("reconnecting");
    showError("连接已断开，正在重连");
    reconnectTimer = setTimeout(connect, 1500);
  });

  socket.addEventListener("error", function () {
    if (ws !== socket) return;
    setTransportState("error");
    showError("连接异常");
  });
}

async function checkAuth() {
  var res = await fetch("/api/auth", { credentials: "same-origin" });
  if (!res.ok) {
    throw new Error("auth check failed");
  }
  var data = await res.json();
  return Boolean(data.authenticated);
}

async function submitLogin(password) {
  var res = await fetch("/api/login", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    credentials: "same-origin",
    body: JSON.stringify({ password: password }),
  });
  if (!res.ok) {
    throw new Error("密码错误");
  }
}

async function logout() {
  await fetch("/api/logout", {
    method: "POST",
    credentials: "same-origin",
  }).catch(function () {});
  if (ws) {
    var current = ws;
    ws = null;
    wsIntentionalClose = true;
    current.close();
  }
  input.value = "";
  pendingImages.forEach(function (item) {
    URL.revokeObjectURL(item.url);
  });
  pendingImages = [];
  renderAttachmentTray();
  updateSendState();
  showLoginScreen();
}

function startStatusInterval() {
  if (statusIntervalStarted) return;
  statusIntervalStarted = true;
  setInterval(function () {
    scheduleStatusRefresh(0);
  }, 30000);
}

function enterApp() {
  hideSessionChooser();
  hideLoginScreen();
  hideCodexAuthScreen();
  autoResize();
  renderAttachmentTray();
  updateSendState();
  scheduleStatusRefresh(0);
  startStatusInterval();
  ensureSocket();
}

async function openSessionChooser() {
  hideLoginScreen();
  showSessionChooser();
  var items = await fetchSessions().catch(function () { return null; });
  if (!items || !items.length) {
    resumeSessionChoice.disabled = true;
    resumeEmpty.hidden = false;
    return;
  }
  resumeSessionChoice.disabled = false;
  resumeSessionChoice.dataset.ready = "true";
  resumeSessionChoice.dataset.items = JSON.stringify(items);
}

async function boot() {
  var authenticated = await checkAuth().catch(function () { return false; });
  if (!authenticated) {
    showLoginScreen();
    return;
  }
  var codexAuth = await checkCodexAuthStatus().catch(function () { return { loggedIn: true }; });
  if (!codexAuth.loggedIn) {
    showCodexAuthScreen("当前机器上的 Codex CLI 尚未授权，或授权已失效。");
    return;
  }
  var items = await fetchSessions().catch(function () { return []; });
  var saved = String(currentSessionId || "").trim();
  var matched = saved ? items.find(function (item) { return item && item.id === saved; }) : null;
  if (matched) {
    setSession(matched.id);
    enterApp();
    return;
  }
  if (saved) {
    localStorage.removeItem("codex_session_id");
    currentSessionId = "";
  }
  await openSessionChooser();
}

async function resolveSessionId(prefix) {
  var query = String(prefix || "").trim();
  if (!query) {
    throw new Error("缺少会话 ID");
  }
  var items = await fetchSessions();
  var match = items.find(function (item) {
    return String(item.id || "").startsWith(query);
  });
  if (!match) {
    throw new Error("没有找到匹配的会话");
  }
  return match.id;
}

async function fetchSessions() {
  var res = await fetch("/api/sessions");
  if (!res.ok) {
    throw new Error(await res.text());
  }
  var data = await res.json();
  return (data.items || []).filter(function (item) {
    return item && item.id;
  });
}

async function switchSession(sessionId, connectNow) {
  var nextId = String(sessionId || "").trim();
  if (!nextId) {
    throw new Error("缺少会话 ID");
  }
  if (nextId === currentSessionId) {
    input.value = "";
    autoResize();
    return;
  }
  removeWorkingPlaceholder();
  streamStates.forEach(function (state) {
    if (state && state.timer) {
      clearTimeout(state.timer);
    }
  });
  streamStates.clear();
  activeDraftId = "";
  pendingImages.forEach(function (item) {
    URL.revokeObjectURL(item.url);
  });
  pendingImages = [];
  renderAttachmentTray();
  renderEmpty();
  input.value = "";
  autoResize();
  setTaskState(false);
  setSession(nextId);
  setFooterStatus("ready", "已恢复会话 " + shortSession(nextId));
  scheduleStatusRefresh(0);
  if (ws) {
    var current = ws;
    ws = null;
    wsIntentionalClose = true;
    current.close();
  }
  if (connectNow === false) {
    return;
  }
  ensureSocket();
}
