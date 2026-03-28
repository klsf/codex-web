commands = [
  { name: "/status", aliases: [":status"], description: "显示当前会话与剩余套餐量", action: async function () {
    input.value = "";
    hideCommandPalette();
    var res = await fetch("/api/status?sessionId=" + encodeURIComponent(currentSessionId));
    if (!res.ok) throw new Error(await res.text());
    var data = await res.json();
    var lines = [
      "model: " + data.model,
      "cwd: " + data.cwd,
      "session: " + shortSession(data.sessionId || currentSessionId),
      "transport: " + data.transport,
      "task: " + data.task,
      "approvals: " + (data.approvalPolicy || "never"),
      "fast: " + (data.fastMode ? "on" : "off"),
      "service tier: " + (data.serviceTier || "default"),
    ];
    if (data.rateLimits) {
      lines.push("plan: " + (data.rateLimits.planType || "unknown"));
      if (data.rateLimits.primary) lines.push("primary: " + remainText(data.rateLimits.primary));
      if (data.rateLimits.secondary) lines.push("secondary: " + remainText(data.rateLimits.secondary));
      if (data.rateLimits.credits) lines.push("credits: " + creditText(data.rateLimits.credits));
    }
    renderMessage({ id: "status-" + Date.now(), role: "system", content: lines.join("\n"), createdAt: new Date().toISOString() }, { animate: false });
  }},
  { name: "/skills", aliases: [":skills"], description: "快速选择可用 skills", action: async function () {
    var args = extractCommandArgs(input.value, ["/skills", ":skills"]);
    if (!args) {
      input.value = "/skills";
      autoResize();
      await openSkillsPalette();
      return;
    }
    hideCommandPalette();
  }},
  { name: "/fast", aliases: [":fast"], description: "切换 Fast mode，启用最快推理", action: async function () {
    var args = extractCommandArgs(input.value, ["/fast", ":fast"]);
    if (!args) {
      input.value = "/fast";
      autoResize();
      await openFastPalette();
      return;
    }
    hideCommandPalette();
    var res = await fetch("/api/command", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ sessionId: currentSessionId, command: "/fast", args: args }),
    });
    if (!res.ok) throw new Error(await res.text());
    var data = await res.json();
    input.value = "";
    renderMessage({ id: "fast-" + Date.now(), role: "system", content: "fast mode: " + (data.fastMode ? "on" : "off") + " (" + (data.serviceTier || "default") + ")", createdAt: new Date().toISOString() }, { animate: false });
  }},
  { name: "/stop", aliases: [":stop"], description: "终止当前正在执行的任务", action: async function () {
    hideCommandPalette();
    var res = await fetch("/api/command", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ sessionId: currentSessionId, command: "/stop" }),
    });
    if (!res.ok) throw new Error(await res.text());
    var data = await res.json();
    input.value = "";
    autoResize();
    renderMessage({ id: "stop-" + Date.now(), role: "system", content: data.stopped ? "已发送停止请求" : "当前没有正在执行的任务", createdAt: new Date().toISOString() }, { animate: false });
  }},
  { name: "/compact", aliases: [":compact"], description: "压缩当前会话上下文，避免逼近上下文限制", action: async function () {
    hideCommandPalette();
    var res = await fetch("/api/command", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ sessionId: currentSessionId, command: "/compact" }),
    });
    if (!res.ok) throw new Error(await res.text());
    var data = await res.json();
    input.value = "";
    autoResize();
    renderMessage({ id: "compact-" + Date.now(), role: "system", content: data.compacted ? "已开始压缩当前会话上下文" : "当前会话还没有可压缩的上下文", createdAt: new Date().toISOString() }, { animate: false });
  }},
  { name: "/resume", aliases: [":resume"], description: "恢复一个历史 Codex 会话", action: async function () {
    if (isRunning) throw new Error("任务执行中，先用 /stop 终止");
    var args = extractCommandArgs(input.value, ["/resume", ":resume"]);
    if (!args) {
      input.value = "/resume";
      autoResize();
      await openResumePalette();
      return;
    }
    var res = await fetch("/api/sessions");
    if (!res.ok) throw new Error(await res.text());
    var data = await res.json();
    var match = (data.items || []).find(function (item) { return String(item.id || "").startsWith(args); });
    if (!match) throw new Error("没有找到匹配的会话");
    await switchSession(match.id);
  }},
  { name: "/clear", aliases: [":clear"], description: "清空当前界面并开始一个新会话", action: async function () {
    if (isRunning) throw new Error("任务执行中，先用 /stop 终止");
    hideCommandPalette();
    input.value = "";
    autoResize();
    pendingImages.forEach(function (item) { URL.revokeObjectURL(item.url); });
    pendingImages = [];
    renderAttachmentTray();
    await createSession();
  }},
  { name: "/new", aliases: [":new"], description: "返回新建会话页面", action: async function () {
    if (isRunning) throw new Error("任务执行中，先用 /stop 终止");
    hideCommandPalette();
    input.value = "";
    autoResize();
    if (ws) {
      var current = ws;
      wsIntentionalClose = true;
      ws = null;
      current.close();
    }
    clearTimeout(reconnectTimer);
    localStorage.removeItem("codex_session_id");
    currentSessionId = "";
    renderEmpty();
    setFooterStatus("ready", "请选择新建会话或恢复会话");
    await openSessionChooser();
  }},
  { name: "/delete", aliases: [":delete"], description: "删除历史会话，或 /delete current 删除当前会话", action: async function () {
    if (isRunning) throw new Error("任务执行中，先用 /stop 终止");
    var args = extractCommandArgs(input.value, ["/delete", ":delete"]);
    if (!args) {
      input.value = "/delete";
      autoResize();
      await openDeletePalette();
      return;
    }
    hideCommandPalette();
    var deleteCurrent = args === "current";
    var targetId = deleteCurrent ? currentSessionId : await resolveSessionId(args);
    var res = await fetch("/api/command", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ sessionId: currentSessionId, command: "/delete", args: targetId }),
    });
    if (!res.ok) throw new Error(await res.text());
    input.value = "";
    autoResize();
    if (deleteCurrent) {
      await createSession();
      return;
    }
    renderMessage({ id: "delete-" + Date.now(), role: "system", content: "已删除会话 " + shortSession(targetId), createdAt: new Date().toISOString() }, { animate: false });
  }},
  { name: "/model", aliases: [":model"], description: "显示或切换当前模型", action: async function () {
    var args = extractCommandArgs(input.value, ["/model", ":model"]);
    if (!args) {
      input.value = "/model";
      autoResize();
      await openModelPalette();
      return;
    }
    hideCommandPalette();
    var res = await fetch("/api/command", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ sessionId: currentSessionId, command: "/model", args: args }),
    });
    if (!res.ok) throw new Error(await res.text());
    var data = await res.json();
    if (data.model) modelBadge.textContent = data.model;
    input.value = "";
    renderMessage({ id: "model-" + Date.now(), role: "system", content: "model: " + data.model, createdAt: new Date().toISOString() }, { animate: false });
  }},
  { name: "/logout", aliases: [":logout"], description: "退出登录并返回密码页", action: async function () {
    hideCommandPalette();
    await logout();
  }},
];

function commandQuery(value) {
  var text = String(value || "").trimStart();
  if (!text || (text[0] !== ":" && text[0] !== "/")) {
    return "";
  }
  return text.split(/\s+/)[0];
}

function matchingCommands(value) {
  var query = commandQuery(value).toLowerCase();
  if (!query) return [];
  return commands.filter(function (item) {
    return [item.name].concat(item.aliases || []).some(function (token) {
      return token.startsWith(query);
    });
  });
}

function updateCommandPalette() {
  var token = commandQuery(input.value).toLowerCase();
  if (token === "/model" || token === ":model") return openModelPalette();
  if (token === "/skills" || token === ":skills") return openSkillsPalette();
  if (token === "/resume" || token === ":resume") return openResumePalette();
  if (token === "/delete" || token === ":delete") return openDeletePalette();
  if (token === "/fast" || token === ":fast") return openFastPalette();
  paletteMode = "commands";
  commandItems = matchingCommands(input.value);
  if (!commandItems.length) {
    hideCommandPalette();
    return;
  }
  commandIndex = Math.min(commandIndex, commandItems.length - 1);
  renderCommandPalette();
}

function renderCommandPalette() {
  commandPalette.innerHTML = "";
  commandPalette.hidden = false;
  commandItems.forEach(function (item, index) {
    var button = document.createElement("button");
    button.type = "button";
    button.className = "command-item" + (index === commandIndex ? " is-active" : "");
    if (item.disabled) button.disabled = true;
    var title = paletteMode === "models" ? (item.displayName || item.model || item.name) : (item.displayName || item.name);
    var desc = paletteMode === "models" ? (item.description || item.model || "") : (item.description || "");
    button.innerHTML = '<div class="command-name">' + title + '</div><div class="command-desc">' + desc + '</div>';
    button.addEventListener("click", function () {
      if (item.disabled) return;
      if (paletteMode === "commands" && item.name) {
        input.value = item.name;
        autoResize();
      }
      executeCommand(item);
    });
    commandPalette.appendChild(button);
  });
}

function hideCommandPalette() {
  commandPalette.hidden = true;
  commandPalette.innerHTML = "";
  commandItems = [];
  commandIndex = 0;
  paletteMode = "commands";
}

async function executeCommand(item) {
  if (!item) return;
  try {
    if (paletteMode === "models") return await selectModel(item);
    if (paletteMode === "skills") return await selectSkill(item);
    if (paletteMode === "sessions") return await selectResumeSession(item);
    if (paletteMode === "delete_sessions") return await selectDeleteSession(item);
    if (paletteMode === "fast") return await selectFastOption(item);
    await item.action();
    autoResize();
    scrollToBottom();
  } catch (err) {
    showError(err && err.message ? err.message : "命令执行失败");
  }
}

function extractCommandArgs(raw, names) {
  var text = String(raw || "").trim();
  var allNames = Array.isArray(names) ? names : [names];
  for (var i = 0; i < allNames.length; i += 1) {
    var escaped = allNames[i].replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    var next = text.replace(new RegExp("^" + escaped + "\\b"), "").trim();
    if (next !== text) return next;
  }
  return "";
}

async function openModelPalette() {
  paletteMode = "models";
  var res = await fetch("/api/models");
  if (!res.ok) throw new Error(await res.text());
  var data = await res.json();
  commandItems = (data.items || []).map(function (item) {
    return {
      name: item.model || item.id,
      model: item.model || item.id,
      displayName: item.displayName || item.model || item.id,
      description: item.description || "",
      isCurrent: (item.model || item.id) === data.current,
    };
  });
  if (!commandItems.length) return hideCommandPalette();
  commandIndex = Math.max(0, commandItems.findIndex(function (item) { return item.isCurrent; }));
  renderCommandPalette();
}

async function openSkillsPalette() {
  paletteMode = "skills";
  var res = await fetch("/api/skills");
  if (!res.ok) throw new Error(await res.text());
  var data = await res.json();
  commandItems = (data.items || []).map(function (item) {
    return { name: item.name, displayName: item.name, description: item.description || "", path: item.path || "" };
  });
  if (!commandItems.length) return hideCommandPalette();
  commandIndex = 0;
  renderCommandPalette();
}

async function openFastPalette() {
  paletteMode = "fast";
  commandItems = [
    { name: "/fast on", displayName: "on", description: "开启 Fast mode" },
    { name: "/fast off", displayName: "off", description: "关闭 Fast mode" },
    { name: "/fast status", displayName: "status", description: "查看当前 Fast mode 状态" },
  ];
  commandIndex = 0;
  renderCommandPalette();
}

async function openResumePalette() {
  paletteMode = "sessions";
  var res = await fetch("/api/sessions");
  if (!res.ok) throw new Error(await res.text());
  var data = await res.json();
  commandItems = (data.items || []).filter(function (item) {
    return item && item.id && item.id !== currentSessionId;
  }).map(function (item) {
    return { id: item.id, name: "/resume " + item.id, displayName: shortSession(item.id), description: resumeSummary(item), updatedAt: item.updatedAt || "" };
  });
  if (!commandItems.length) {
    commandItems = [{ name: "", displayName: "没有可恢复的历史会话", description: "当前没有其它历史会话可切换", disabled: true }];
  }
  commandIndex = 0;
  renderCommandPalette();
}

async function openDeletePalette() {
  paletteMode = "delete_sessions";
  var res = await fetch("/api/sessions");
  if (!res.ok) throw new Error(await res.text());
  var data = await res.json();
  commandItems = (data.items || []).filter(function (item) {
    return item && item.id && item.id !== currentSessionId;
  }).map(function (item) {
    return { id: item.id, name: "/delete " + item.id, displayName: "删除 " + shortSession(item.id), description: resumeSummary(item) };
  });
  if (!commandItems.length) {
    commandItems = [{ name: "", displayName: "没有可删除的历史会话", description: "当前没有其它历史会话可删除", disabled: true }];
  }
  commandIndex = 0;
  renderCommandPalette();
}

async function selectModel(item) {
  hideCommandPalette();
  var res = await fetch("/api/command", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ sessionId: currentSessionId, command: "/model", args: item.model || item.name }),
  });
  if (!res.ok) throw new Error(await res.text());
  var data = await res.json();
  if (data.model) modelBadge.textContent = data.model;
  input.value = "";
  autoResize();
  renderMessage({ id: "model-" + Date.now(), role: "system", content: "已切换模型到 " + data.model, createdAt: new Date().toISOString() }, { animate: false });
}

async function selectSkill(item) {
  hideCommandPalette();
  input.value = "Use " + item.name + " skill for this request: ";
  autoResize();
  input.focus();
}

async function selectFastOption(item) {
  hideCommandPalette();
  input.value = item.name;
  autoResize();
  await commands.find(function (command) {
    return command.name === "/fast";
  }).action();
}

async function selectResumeSession(item) {
  hideCommandPalette();
  if (!item || !item.id) throw new Error("无效的会话");
  await switchSession(item.id);
}

async function selectDeleteSession(item) {
  hideCommandPalette();
  if (!item || !item.id) throw new Error("无效的会话");
  var res = await fetch("/api/command", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ sessionId: currentSessionId, command: "/delete", args: item.id }),
  });
  if (!res.ok) throw new Error(await res.text());
  input.value = "";
  autoResize();
  renderMessage({ id: "delete-" + Date.now(), role: "system", content: "已删除会话 " + shortSession(item.id), createdAt: new Date().toISOString() }, { animate: false });
}
