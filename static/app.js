(function () {
  const timeline = document.getElementById("timeline");
  const loginScreen = document.getElementById("loginScreen");
  const loginForm = document.getElementById("loginForm");
  const passwordInput = document.getElementById("passwordInput");
  const loginError = document.getElementById("loginError");
  const sessionChooser = document.getElementById("sessionChooser");
  const newSessionChoice = document.getElementById("newSessionChoice");
  const resumeSessionChoice = document.getElementById("resumeSessionChoice");
  const resumeList = document.getElementById("resumeList");
  const resumeEmpty = document.getElementById("resumeEmpty");
  const workdirInput = document.getElementById("workdirInput");
  const form = document.getElementById("composer");
  const input = document.getElementById("promptInput");
  const sessionBadge = document.getElementById("sessionBadge");
  const modelBadge = document.getElementById("modelBadge");
  const cwdBadge = document.getElementById("cwdBadge");
  const transportBadge = document.getElementById("transportBadge");
  const attachmentTray = document.getElementById("attachmentTray");
  const commandPalette = document.getElementById("commandPalette");
  const imageInput = document.getElementById("imageInput");
  const imageBtn = document.getElementById("imageBtn");
  const sendBtn = document.getElementById("sendBtn");
  const footerState = document.getElementById("footerState");
  const footerDetail = document.getElementById("footerDetail");
  const statusSession = document.getElementById("statusSession");
  const statusModel = document.getElementById("statusModel");
  const statusCwd = document.getElementById("statusCwd");
  const statusTransport = document.getElementById("statusTransport");
  const statusTask = document.getElementById("statusTask");
  const statusApprovals = document.getElementById("statusApprovals");
  const statusFast = document.getElementById("statusFast");
  const statusServiceTier = document.getElementById("statusServiceTier");
  const statusPlan = document.getElementById("statusPlan");
  const statusPrimary = document.getElementById("statusPrimary");
  const statusSecondary = document.getElementById("statusSecondary");
  const statusCredits = document.getElementById("statusCredits");
  const template = document.getElementById("messageTemplate");
  const attachmentTemplate = document.getElementById("attachmentTemplate");

  let ws = null;
  let reconnectTimer = null;
  let currentSessionId = localStorage.getItem("codex_session_id") || "";
  let isRunning = false;
  let pendingImages = [];
  let activeDraftId = "";
  const streamStates = new Map();
  const WORKING_MESSAGE_ID = "__working__";
  let workingStartedAt = 0;
  let workingTimer = null;
  let commandItems = [];
  let commandIndex = 0;
  let paletteMode = "commands";
  let statusRefreshTimer = null;
  let isAuthenticated = false;
  let statusIntervalStarted = false;

  const commands = [
    { name: "/status", aliases: [":status"], description: "显示当前会话与剩余套餐量", action: async () => {
      input.value = "";
      hideCommandPalette();
      const res = await fetch("/api/status?sessionId=" + encodeURIComponent(currentSessionId));
      if (!res.ok) {
        throw new Error(await res.text());
      }
      const data = await res.json();
      const lines = [
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
        if (data.rateLimits.primary) {
          lines.push("primary: " + remainText(data.rateLimits.primary));
        }
        if (data.rateLimits.secondary) {
          lines.push("secondary: " + remainText(data.rateLimits.secondary));
        }
        if (data.rateLimits.credits) {
          lines.push("credits: " + creditText(data.rateLimits.credits));
        }
      }
      renderMessage({
        id: "status-" + Date.now(),
        role: "system",
        content: lines.join("\n"),
        createdAt: new Date().toISOString(),
      }, { animate: false });
    }},
    { name: "/init", aliases: [":init"], description: "在工作目录创建 AGENTS.md", action: async () => {
      const args = extractCommandArgs(input.value, ["/init", ":init"]);
      hideCommandPalette();
      const res = await fetch("/api/command", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ sessionId: currentSessionId, command: "/init", args }),
      });
      if (!res.ok) {
        throw new Error(await res.text());
      }
      const data = await res.json();
      input.value = "";
      renderMessage({
        id: "init-" + Date.now(),
        role: "system",
        content: (data.created ? "created " : "exists ") + data.path,
        createdAt: new Date().toISOString(),
      }, { animate: false });
    }},
    { name: "/skills", aliases: [":skills"], description: "快速选择可用 skills", action: async () => {
      const args = extractCommandArgs(input.value, ["/skills", ":skills"]);
      if (!args) {
        input.value = "/skills";
        autoResize();
        await openSkillsPalette();
        return;
      }
      hideCommandPalette();
    }},
    { name: "/fast", aliases: [":fast"], description: "切换 Fast mode，启用最快推理", action: async () => {
      const args = extractCommandArgs(input.value, ["/fast", ":fast"]);
      if (!args) {
        input.value = "/fast";
        autoResize();
        await openFastPalette();
        return;
      }
      hideCommandPalette();
      const res = await fetch("/api/command", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ sessionId: currentSessionId, command: "/fast", args }),
      });
      if (!res.ok) {
        throw new Error(await res.text());
      }
      const data = await res.json();
      input.value = "";
      renderMessage({
        id: "fast-" + Date.now(),
        role: "system",
        content: "fast mode: " + (data.fastMode ? "on" : "off") + " (" + (data.serviceTier || "default") + ")",
        createdAt: new Date().toISOString(),
      }, { animate: false });
    }},
    { name: "/stop", aliases: [":stop"], description: "终止当前正在执行的任务", action: async () => {
      hideCommandPalette();
      const res = await fetch("/api/command", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ sessionId: currentSessionId, command: "/stop" }),
      });
      if (!res.ok) {
        throw new Error(await res.text());
      }
      const data = await res.json();
      input.value = "";
      autoResize();
      renderMessage({
        id: "stop-" + Date.now(),
        role: "system",
        content: data.stopped ? "已发送停止请求" : "当前没有正在执行的任务",
        createdAt: new Date().toISOString(),
      }, { animate: false });
    }},
    { name: "/compact", aliases: [":compact"], description: "压缩当前会话上下文，避免逼近上下文限制", action: async () => {
      hideCommandPalette();
      const res = await fetch("/api/command", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ sessionId: currentSessionId, command: "/compact" }),
      });
      if (!res.ok) {
        throw new Error(await res.text());
      }
      const data = await res.json();
      input.value = "";
      autoResize();
      renderMessage({
        id: "compact-" + Date.now(),
        role: "system",
        content: data.compacted ? "已开始压缩当前会话上下文" : "当前会话还没有可压缩的上下文",
        createdAt: new Date().toISOString(),
      }, { animate: false });
    }},
    { name: "/resume", aliases: [":resume"], description: "恢复一个历史 Codex 会话", action: async () => {
      if (isRunning) {
        throw new Error("任务执行中，先用 /stop 终止");
      }
      const args = extractCommandArgs(input.value, ["/resume", ":resume"]);
      if (!args) {
        input.value = "/resume";
        autoResize();
        await openResumePalette();
        return;
      }
      const res = await fetch("/api/sessions");
      if (!res.ok) {
        throw new Error(await res.text());
      }
      const data = await res.json();
      const match = (data.items || []).find((item) => String(item.id || "").startsWith(args));
      if (!match) {
        throw new Error("没有找到匹配的会话");
      }
      await switchSession(match.id);
    }},
    { name: "/clear", aliases: [":clear"], description: "清空当前界面并开始一个新会话", action: async () => {
      if (isRunning) {
        throw new Error("任务执行中，先用 /stop 终止");
      }
      hideCommandPalette();
      input.value = "";
      autoResize();
      pendingImages.forEach((item) => URL.revokeObjectURL(item.url));
      pendingImages = [];
      renderAttachmentTray();
      await createSession();
    }},
    { name: "/new", aliases: [":new"], description: "返回新建会话页面", action: async () => {
      if (isRunning) {
        throw new Error("任务执行中，先用 /stop 终止");
      }
      hideCommandPalette();
      input.value = "";
      autoResize();
      if (ws) {
        const current = ws;
        ws = null;
        current.close();
      }
      renderEmpty();
      await openSessionChooser();
    }},
    { name: "/delete", aliases: [":delete"], description: "删除历史会话，或 /delete current 删除当前会话", action: async () => {
      if (isRunning) {
        throw new Error("任务执行中，先用 /stop 终止");
      }
      const args = extractCommandArgs(input.value, ["/delete", ":delete"]);
      if (!args) {
        input.value = "/delete";
        autoResize();
        await openDeletePalette();
        return;
      }
      hideCommandPalette();
      const deleteCurrent = args === "current";
      const targetId = deleteCurrent ? currentSessionId : await resolveSessionId(args);
      const res = await fetch("/api/command", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ sessionId: currentSessionId, command: "/delete", args: targetId }),
      });
      if (!res.ok) {
        throw new Error(await res.text());
      }
      input.value = "";
      autoResize();
      if (deleteCurrent) {
        await createSession();
        return;
      }
      renderMessage({
        id: "delete-" + Date.now(),
        role: "system",
        content: "已删除会话 " + shortSession(targetId),
        createdAt: new Date().toISOString(),
      }, { animate: false });
    }},
    { name: "/model", aliases: [":model"], description: "显示或切换当前模型", action: async () => {
      const args = extractCommandArgs(input.value, ["/model", ":model"]);
      if (!args) {
        input.value = "/model";
        autoResize();
        await openModelPalette();
        return;
      }
      hideCommandPalette();
      const res = await fetch("/api/command", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ sessionId: currentSessionId, command: "/model", args }),
      });
      if (!res.ok) {
        throw new Error(await res.text());
      }
      const data = await res.json();
      if (data.model) {
        modelBadge.textContent = data.model;
      }
      input.value = "";
      renderMessage({
        id: "model-" + Date.now(),
        role: "system",
        content: "model: " + data.model,
        createdAt: new Date().toISOString(),
      }, { animate: false });
    }},
    { name: "/logout", aliases: [":logout"], description: "退出登录并返回密码页", action: async () => {
      hideCommandPalette();
      await logout();
    }},
  ];

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
    setTimeout(() => {
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
    const hasContent = String(input.value || "").trim().length > 0;
    const hasImages = pendingImages.length > 0;
    sendBtn.disabled = isRunning || (!hasContent && !hasImages);
  }

  function commandQuery(value) {
    const text = String(value || "").trimStart();
    if (!text || (text[0] !== ":" && text[0] !== "/")) {
      return "";
    }
    return text.split(/\s+/)[0];
  }

  function matchingCommands(value) {
    const query = commandQuery(value).toLowerCase();
    if (!query) return [];
    return commands.filter((item) => [item.name].concat(item.aliases || []).some((token) => token.startsWith(query)));
  }

  function updateCommandPalette() {
    if (commandQuery(input.value).toLowerCase() === "/model" || commandQuery(input.value).toLowerCase() === ":model") {
      openModelPalette();
      return;
    }
    if (
      commandQuery(input.value).toLowerCase() === "/skills" ||
      commandQuery(input.value).toLowerCase() === ":skills"
    ) {
      openSkillsPalette();
      return;
    }
    if (commandQuery(input.value).toLowerCase() === "/resume" || commandQuery(input.value).toLowerCase() === ":resume") {
      openResumePalette();
      return;
    }
    if (commandQuery(input.value).toLowerCase() === "/delete" || commandQuery(input.value).toLowerCase() === ":delete") {
      openDeletePalette();
      return;
    }
    if (commandQuery(input.value).toLowerCase() === "/fast" || commandQuery(input.value).toLowerCase() === ":fast") {
      openFastPalette();
      return;
    }
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
    commandItems.forEach((item, index) => {
      const button = document.createElement("button");
      button.type = "button";
      button.className = "command-item" + (index === commandIndex ? " is-active" : "");
      if (item.disabled) {
        button.disabled = true;
      }
      const title = paletteMode === "models" ? (item.displayName || item.model || item.name) : (item.displayName || item.name);
      const desc = paletteMode === "models" ? (item.description || item.model || "") : (item.description || "");
      button.innerHTML = '<div class="command-name">' + title + '</div><div class="command-desc">' + desc + '</div>';
      button.addEventListener("click", () => {
        if (item.disabled) {
          return;
        }
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
      if (paletteMode === "models") {
        await selectModel(item);
        return;
      }
      if (paletteMode === "skills") {
        await selectSkill(item);
        return;
      }
      if (paletteMode === "sessions") {
        await selectResumeSession(item);
        return;
      }
      if (paletteMode === "delete_sessions") {
        await selectDeleteSession(item);
        return;
      }
      if (paletteMode === "fast") {
        await selectFastOption(item);
        return;
      }
      await item.action();
      autoResize();
      scrollToBottom();
    } catch (err) {
      footerState.textContent = "error";
      footerDetail.textContent = err && err.message ? err.message : "命令执行失败";
    }
  }

  function renderMessage(message, options) {
    const settings = Object.assign({ draft: false, animate: false }, options || {});
    const key = messageKey(message);
    let node = findMessageNode(key);
    if (!node && settings.draft) {
      node = findDraftNode();
    }
    const isNew = !node;

    if (!node) {
      node = template.content.firstElementChild.cloneNode(true);
      timeline.appendChild(node);
    }
    node.dataset.messageId = key;

    node.classList.remove("role-user", "role-assistant", "role-system", "is-draft", "is-streaming", "is-working");
    node.classList.add("role-" + message.role);
    if (key === WORKING_MESSAGE_ID) {
      node.classList.add("is-working");
    }
    if (settings.draft) {
      node.classList.add("is-draft");
      node.classList.add("is-streaming");
      activeDraftId = key;
    } else if (activeDraftId === key || message.role === "assistant") {
      activeDraftId = "";
    }

    node.querySelector(".bubble-meta").textContent = key === WORKING_MESSAGE_ID ? "" : metaForMessage(message);
    renderImages(node, message.imageUrls || []);

    const bubble = node.querySelector(".bubble");
    if (key === WORKING_MESSAGE_ID) {
      renderWorkingBubble(node, message);
    } else if (settings.draft) {
      streamTo(node, message.content || "");
    } else if (settings.animate && isNew) {
      bubble.textContent = "";
      revealBody(node, message.content || "", message.role === "assistant");
    } else {
      stopStream(node);
      if (shouldRenderMarkdown(message)) {
        renderMarkdown(node, message.content || "");
      } else if (bubble.textContent !== (message.content || "")) {
        bubble.textContent = message.content || "";
      }
    }

    if (settings.draft) {
      footerState.textContent = "Working";
      footerDetail.textContent = compact(message.content || "Codex 正在输出");
    }
    scrollToBottom();
    return node;
  }

  function metaForMessage(message) {
    return formatTime(message.createdAt);
  }

  function renderImages(node, imageUrls) {
    const existing = node.querySelector(".bubble-images");
    if (existing) existing.remove();
    if (!imageUrls || !imageUrls.length) {
      return;
    }
    const wrap = document.createElement("div");
    wrap.className = "bubble-images";
    imageUrls.forEach((url) => {
      const img = document.createElement("img");
      img.className = "bubble-image";
      img.src = url;
      img.alt = "upload";
      wrap.appendChild(img);
    });
    node.insertBefore(wrap, node.querySelector(".bubble"));
  }

  function revealBody(node, text, animated) {
    const bubble = node.querySelector(".bubble");
    if (!animated) {
      bubble.textContent = text;
      return;
    }

    const chunks = text.match(/.{1,22}(\s|$)|.+$/g) || [text];
    let i = 0;
    let timer = null;

    function step() {
      bubble.textContent = chunks.slice(0, i + 1).join("");
      scrollToBottom();
      i += 1;
      if (i < chunks.length) {
        timer = setTimeout(step, 18);
      }
    }

    stopStream(node);
    streamStates.set(node.dataset.messageId || crypto.randomUUID(), {
      timer,
      node,
      target: text,
      displayed: text,
      mode: "reveal",
    });
    step();
  }

  function replaceTimeline(messages, draftMessage) {
    timeline.innerHTML = "";
    activeDraftId = "";
    const items = (messages || []).slice().sort((a, b) => new Date(a.createdAt) - new Date(b.createdAt));
    if (!items.length) {
      renderEmpty();
    }
    items.forEach((message) => renderMessage(message, { animate: false }));
    if (draftMessage) {
      renderMessage(draftMessage, { draft: true, animate: false });
    }
  }

  function renderEmpty() {
    timeline.innerHTML = "";
  }

  function renderWorkingBubble(node, message) {
    const bubble = node.querySelector(".bubble");
    bubble.innerHTML = "";
    const text = document.createElement("span");
    text.className = "working-text";
    const content = String(message.content || "Working...");
    for (const [index, ch] of Array.from(content).entries()) {
      const span = document.createElement("span");
      span.className = "working-char";
      span.style.animationDelay = (index * 0.08) + "s";
      span.textContent = ch;
      text.appendChild(span);
    }
    const elapsed = document.createElement("span");
    elapsed.className = "working-elapsed";
    bubble.appendChild(text);
    bubble.appendChild(elapsed);
    if (!workingStartedAt) {
      workingStartedAt = message.createdAt ? new Date(message.createdAt).getTime() : Date.now();
    }
    updateWorkingElapsed();
    startWorkingTimer();
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
    const node = findMessageNode(WORKING_MESSAGE_ID);
    if (!node || !workingStartedAt) return;
    const elapsed = node.querySelector(".working-elapsed");
    if (!elapsed) return;
    const seconds = Math.max(0, (Date.now() - workingStartedAt) / 1000);
    elapsed.textContent = Math.floor(seconds) + "s";
  }

  function ensureWorkingPlaceholder() {
    if (findMessageNode(WORKING_MESSAGE_ID) || findDraftNode()) return;
    if (!workingStartedAt) {
      workingStartedAt = Date.now();
    }
    const node = renderMessage({
      id: WORKING_MESSAGE_ID,
      role: "assistant",
      content: "Working...",
      createdAt: new Date().toISOString(),
    }, { animate: false });
    placeWorkingPlaceholderAfterLastUser(node);
    scrollToBottom();
  }

  function removeWorkingPlaceholder() {
    const node = findMessageNode(WORKING_MESSAGE_ID);
    if (node) {
      stopStream(node);
      node.remove();
    }
    stopWorkingTimer();
  }

  function placeWorkingPlaceholderAfterLastUser(node) {
    const workingNode = node || findMessageNode(WORKING_MESSAGE_ID);
    const lastUser = findLastUserNode();
    if (!workingNode || !lastUser) return;
    if (workingNode === lastUser.nextElementSibling) return;
    timeline.insertBefore(workingNode, lastUser.nextElementSibling);
  }

  function connect() {
    clearTimeout(reconnectTimer);
    setTransportState("connecting");

    const protocol = location.protocol === "https:" ? "wss:" : "ws:";
    ws = new WebSocket(protocol + "//" + location.host + "/ws");

    ws.addEventListener("open", () => {
      setTransportState("connected");
      ws.send(JSON.stringify({ type: "hello", sessionId: currentSessionId }));
    });

    ws.addEventListener("message", (evt) => {
      const data = JSON.parse(evt.data);
      if (data.type === "snapshot" && data.session) {
        setSession(data.session.id);
        setMeta(data.meta);
        replaceTimeline(data.session.messages || [], data.session.draftMessage || null);
        setTaskState(Boolean(data.running));
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
        const node = renderMessage(data.message, { animate: false });
        if (data.message.role === "user") {
          placeWorkingPlaceholderAfterLastUser();
        }
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
        footerState.textContent = "error";
        footerDetail.textContent = data.error;
      }
    });

    ws.addEventListener("close", () => {
      setTransportState("reconnecting");
      reconnectTimer = setTimeout(connect, 1500);
    });

    ws.addEventListener("error", () => {
      setTransportState("error");
    });
  }

  async function createSession(workdir) {
    const res = await fetch("/api/session/new", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ workdir: String(workdir || "").trim() }),
    });
    if (!res.ok) {
      throw new Error(await res.text());
    }
    const data = await res.json();
    setSession(data.sessionId);
    replaceTimeline([]);
    setTaskState(false);
    footerState.textContent = "ready";
    footerDetail.textContent = "等待输入";
    if (ws) ws.close();
  }

  async function submitPrompt(raw) {
    const content = (raw == null ? input.value : raw).trim();
    if ((!content && !pendingImages.length) || !ws || ws.readyState !== WebSocket.OPEN) return;
    const commandToken = commandQuery(content);
    const exactCommand = commands.find((item) => item.name === commandToken || (item.aliases || []).includes(commandToken));
    if (isRunning && (!exactCommand || exactCommand.name !== "/stop")) return;
    if (exactCommand) {
      await executeCommand(exactCommand);
      return;
    }
    hideCommandPalette();
    const formData = new FormData();
    formData.append("sessionId", currentSessionId);
    formData.append("content", content);
    pendingImages.forEach((item) => formData.append("images", item.file, item.file.name));

    setTaskState(true);
    footerDetail.textContent = compact(content || "发送图片");
    ensureWorkingPlaceholder();
    try {
      const res = await fetch("/api/send", { method: "POST", body: formData });
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
      footerState.textContent = "error";
      footerDetail.textContent = "发送失败";
    }
  }

  function compact(text) {
    return String(text || "").replace(/\s+/g, " ").trim().slice(0, 120) || "等待输入";
  }

  function shouldRenderMarkdown(message) {
    return Boolean(message && message.role === "assistant");
  }

  function renderMarkdown(node, text) {
    const bubble = node.querySelector(".bubble");
    const source = String(text || "");
    const markedLib = window.marked;
    const purifier = window.DOMPurify;
    if (!markedLib || !purifier) {
      bubble.textContent = source;
      return;
    }
    markedLib.setOptions({
      breaks: true,
      gfm: true,
    });
    const html = markedLib.parse(source);
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

  function remainText(window) {
    const used = Number(window.usedPercent || 0);
    const remain = Math.max(0, 100 - used);
    return remain + "% left, used " + used + "%, reset " + formatReset(window.resetsAt);
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
    const res = await fetch("/api/status?sessionId=" + encodeURIComponent(currentSessionId));
    if (!res.ok) {
      throw new Error(await res.text());
    }
    const data = await res.json();
    applyStatus(data);
  }

  function scheduleStatusRefresh(delay) {
    if (!isAuthenticated) return;
    clearTimeout(statusRefreshTimer);
    statusRefreshTimer = setTimeout(() => {
      refreshSidebarStatus().catch(() => {});
    }, delay == null ? 250 : delay);
  }

  async function checkAuth() {
    const res = await fetch("/api/auth", { credentials: "same-origin" });
    if (!res.ok) {
      throw new Error("auth check failed");
    }
    const data = await res.json();
    return Boolean(data.authenticated);
  }

  async function submitLogin(password) {
    const res = await fetch("/api/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      credentials: "same-origin",
      body: JSON.stringify({ password }),
    });
    if (!res.ok) {
      throw new Error("密码错误");
    }
  }

  async function logout() {
    await fetch("/api/logout", {
      method: "POST",
      credentials: "same-origin",
    }).catch(() => {});
    if (ws) {
      const current = ws;
      ws = null;
      current.close();
    }
    input.value = "";
    pendingImages.forEach((item) => URL.revokeObjectURL(item.url));
    pendingImages = [];
    renderAttachmentTray();
    updateSendState();
    showLoginScreen();
  }

  function startStatusInterval() {
    if (statusIntervalStarted) return;
    statusIntervalStarted = true;
    setInterval(() => {
      scheduleStatusRefresh(0);
    }, 30000);
  }

  function enterApp() {
    hideSessionChooser();
    hideLoginScreen();
    autoResize();
    renderAttachmentTray();
    updateSendState();
    scheduleStatusRefresh(0);
    startStatusInterval();
    connect();
  }

  async function openSessionChooser() {
    hideLoginScreen();
    showSessionChooser();
    const items = await fetchSessions().catch(() => null);
    if (!items) {
      resumeSessionChoice.disabled = true;
      resumeEmpty.hidden = false;
      return;
    }
    if (!items.length) {
      resumeSessionChoice.disabled = true;
      resumeEmpty.hidden = false;
      return;
    }
    resumeSessionChoice.disabled = false;
    resumeSessionChoice.dataset.ready = "true";
    resumeSessionChoice.dataset.items = JSON.stringify(items);
  }

  async function boot() {
    const authenticated = await checkAuth().catch(() => false);
    if (!authenticated) {
      showLoginScreen();
      return;
    }
    const items = await fetchSessions().catch(() => []);
    const saved = String(currentSessionId || "").trim();
    const matched = saved ? items.find((item) => item && item.id === saved) : null;
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

  function shortSession(id) {
    return id ? String(id).slice(0, 8) : "unknown";
  }

  function resumeSummary(item) {
    const text = compact(item.lastMessage || "");
    const count = Number(item.messageCount || 0);
    const updated = item.updatedAt ? formatTime(item.updatedAt) : "--:--";
    const workdir = compact(item.workdir || "");
    return [workdir || "无工作目录", text || "无消息记录", count + " 条消息", updated].join(" · ");
  }

  async function resolveSessionId(prefix) {
    const query = String(prefix || "").trim();
    if (!query) {
      throw new Error("缺少会话 ID");
    }
    const items = await fetchSessions();
    const match = items.find((item) => String(item.id || "").startsWith(query));
    if (!match) {
      throw new Error("没有找到匹配的会话");
    }
    return match.id;
  }

  async function fetchSessions() {
    const res = await fetch("/api/sessions");
    if (!res.ok) {
      throw new Error(await res.text());
    }
    const data = await res.json();
    return (data.items || []).filter((item) => item && item.id);
  }

  function extractCommandArgs(raw, names) {
    const text = String(raw || "").trim();
    const allNames = Array.isArray(names) ? names : [names];
    for (const name of allNames) {
      const escaped = name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
      const next = text.replace(new RegExp("^" + escaped + "\\b"), "").trim();
      if (next !== text) {
        return next;
      }
    }
    return "";
  }

  async function openModelPalette() {
    paletteMode = "models";
    const res = await fetch("/api/models");
    if (!res.ok) {
      throw new Error(await res.text());
    }
    const data = await res.json();
    commandItems = (data.items || []).map((item) => ({
      name: item.model || item.id,
      model: item.model || item.id,
      displayName: item.displayName || item.model || item.id,
      description: item.description || "",
      isCurrent: (item.model || item.id) === data.current,
    }));
    if (!commandItems.length) {
      hideCommandPalette();
      return;
    }
    commandIndex = Math.max(0, commandItems.findIndex((item) => item.isCurrent));
    renderCommandPalette();
  }

  async function openSkillsPalette() {
    paletteMode = "skills";
    const res = await fetch("/api/skills");
    if (!res.ok) {
      throw new Error(await res.text());
    }
    const data = await res.json();
    commandItems = (data.items || []).map((item) => ({
      name: item.name,
      displayName: item.name,
      description: item.description || "",
      path: item.path || "",
    }));
    if (!commandItems.length) {
      hideCommandPalette();
      return;
    }
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
    const res = await fetch("/api/sessions");
    if (!res.ok) {
      throw new Error(await res.text());
    }
    const data = await res.json();
    commandItems = (data.items || [])
      .filter((item) => item && item.id && item.id !== currentSessionId)
      .map((item) => ({
        id: item.id,
        name: "/resume " + item.id,
        displayName: shortSession(item.id),
        description: resumeSummary(item),
        updatedAt: item.updatedAt || "",
      }));
    if (!commandItems.length) {
      commandItems = [{
        name: "",
        displayName: "没有可恢复的历史会话",
        description: "当前没有其它历史会话可切换",
        disabled: true,
      }];
      commandIndex = 0;
      renderCommandPalette();
      return;
    }
    commandIndex = 0;
    renderCommandPalette();
  }

  async function openDeletePalette() {
    paletteMode = "delete_sessions";
    const res = await fetch("/api/sessions");
    if (!res.ok) {
      throw new Error(await res.text());
    }
    const data = await res.json();
    commandItems = (data.items || [])
      .filter((item) => item && item.id && item.id !== currentSessionId)
      .map((item) => ({
        id: item.id,
        name: "/delete " + item.id,
        displayName: "删除 " + shortSession(item.id),
        description: resumeSummary(item),
      }));
    if (!commandItems.length) {
      commandItems = [{
        name: "",
        displayName: "没有可删除的历史会话",
        description: "当前没有其它历史会话可删除",
        disabled: true,
      }];
      commandIndex = 0;
      renderCommandPalette();
      return;
    }
    commandIndex = 0;
    renderCommandPalette();
  }

  async function selectModel(item) {
    hideCommandPalette();
    const res = await fetch("/api/command", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ sessionId: currentSessionId, command: "/model", args: item.model || item.name }),
    });
    if (!res.ok) {
      throw new Error(await res.text());
    }
    const data = await res.json();
    if (data.model) {
      modelBadge.textContent = data.model;
    }
    input.value = "";
    autoResize();
    renderMessage({
      id: "model-" + Date.now(),
      role: "system",
      content: "已切换模型到 " + data.model,
      createdAt: new Date().toISOString(),
    }, { animate: false });
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
    await commands.find((command) => command.name === "/fast").action();
  }

  async function selectResumeSession(item) {
    hideCommandPalette();
    if (!item || !item.id) {
      throw new Error("无效的会话");
    }
    await switchSession(item.id);
  }

  async function selectDeleteSession(item) {
    hideCommandPalette();
    if (!item || !item.id) {
      throw new Error("无效的会话");
    }
    const res = await fetch("/api/command", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ sessionId: currentSessionId, command: "/delete", args: item.id }),
    });
    if (!res.ok) {
      throw new Error(await res.text());
    }
    input.value = "";
    autoResize();
    renderMessage({
      id: "delete-" + Date.now(),
      role: "system",
      content: "已删除会话 " + shortSession(item.id),
      createdAt: new Date().toISOString(),
    }, { animate: false });
  }

  async function switchSession(sessionId, connectNow) {
    const nextId = String(sessionId || "").trim();
    if (!nextId) {
      throw new Error("缺少会话 ID");
    }
    if (nextId === currentSessionId) {
      input.value = "";
      autoResize();
      return;
    }
    removeWorkingPlaceholder();
    streamStates.forEach((state) => {
      if (state && state.timer) {
        clearTimeout(state.timer);
      }
    });
    streamStates.clear();
    activeDraftId = "";
    pendingImages.forEach((item) => URL.revokeObjectURL(item.url));
    pendingImages = [];
    renderAttachmentTray();
    renderEmpty();
    input.value = "";
    autoResize();
    setTaskState(false);
    setSession(nextId);
    footerState.textContent = "ready";
    footerDetail.textContent = "已恢复会话 " + shortSession(nextId);
    if (connectNow === false) {
      return;
    }
    if (ws) {
      ws.close();
      return;
    }
    connect();
  }

  form.addEventListener("submit", (evt) => {
    evt.preventDefault();
    submitPrompt();
  });

  input.addEventListener("input", () => {
    autoResize();
    updateCommandPalette();
    updateSendState();
  });

  input.addEventListener("keydown", (evt) => {
    if (!commandPalette.hidden && (evt.key === "ArrowDown" || evt.key === "ArrowUp")) {
      evt.preventDefault();
      if (!commandItems.length) return;
      if (evt.key === "ArrowDown") {
        commandIndex = (commandIndex + 1) % commandItems.length;
      } else {
        commandIndex = (commandIndex - 1 + commandItems.length) % commandItems.length;
      }
      renderCommandPalette();
      return;
    }
    if (!commandPalette.hidden && evt.key === "Enter" && !evt.shiftKey && commandItems.length) {
      evt.preventDefault();
      executeCommand(commandItems[commandIndex]);
      return;
    }
    if (!commandPalette.hidden && evt.key === "Escape") {
      evt.preventDefault();
      hideCommandPalette();
      return;
    }
    if (evt.key === "Enter" && !evt.shiftKey) {
      evt.preventDefault();
      submitPrompt();
    }
  });

  imageBtn.addEventListener("click", () => {
    imageInput.click();
  });

  document.addEventListener("click", (evt) => {
    if (!commandPalette.hidden && !commandPalette.contains(evt.target) && evt.target !== input) {
      hideCommandPalette();
    }
  });

  imageInput.addEventListener("change", async () => {
    const files = Array.from(imageInput.files || []);
    imageInput.value = "";
    for (const file of files) {
      pendingImages.push({
        file,
        url: URL.createObjectURL(file),
      });
    }
    renderAttachmentTray();
    updateSendState();
  });

  loginForm.addEventListener("submit", async (evt) => {
    evt.preventDefault();
    loginError.textContent = "";
    try {
      await submitLogin(passwordInput.value);
      await openSessionChooser();
    } catch (err) {
      loginError.textContent = err && err.message ? err.message : "登录失败";
      passwordInput.select();
    }
  });

  newSessionChoice.addEventListener("click", async () => {
    try {
      resumeEmpty.hidden = true;
      await createSession(workdirInput.value);
      enterApp();
    } catch (err) {
      resumeEmpty.hidden = false;
      resumeEmpty.textContent = err && err.message ? err.message : "新建会话失败";
    }
  });

  resumeSessionChoice.addEventListener("click", async () => {
    const raw = resumeSessionChoice.dataset.items || "[]";
    const items = JSON.parse(raw);
    resumeList.innerHTML = "";
    resumeList.hidden = false;
    resumeEmpty.hidden = items.length > 0;
    items.forEach((item) => {
      const row = document.createElement("div");
      row.className = "resume-item";

      const openButton = document.createElement("button");
      openButton.type = "button";
      openButton.className = "resume-open";
      openButton.innerHTML = '<div class="resume-item-title">' + shortSession(item.id) + '</div><div class="resume-item-desc">' + resumeSummary(item) + '</div>';
      openButton.addEventListener("click", async () => {
        await switchSession(item.id, false);
        enterApp();
      });

      const deleteButton = document.createElement("button");
      deleteButton.type = "button";
      deleteButton.className = "resume-delete";
      deleteButton.textContent = "×";
      deleteButton.setAttribute("aria-label", "删除会话");
      deleteButton.addEventListener("click", async (evt) => {
        evt.stopPropagation();
        const res = await fetch("/api/command", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ sessionId: currentSessionId || item.id, command: "/delete", args: item.id }),
        });
        if (!res.ok) {
          resumeEmpty.hidden = false;
          resumeEmpty.textContent = await res.text();
          return;
        }
        row.remove();
        const remaining = Array.from(resumeList.querySelectorAll(".resume-item")).length;
        if (!remaining) {
          resumeEmpty.hidden = false;
          resumeEmpty.textContent = "没有可恢复的历史会话";
          resumeList.hidden = true;
          resumeSessionChoice.disabled = true;
        }
        const nextItems = items.filter((session) => session.id !== item.id);
        resumeSessionChoice.dataset.items = JSON.stringify(nextItems);
      });

      row.appendChild(openButton);
      row.appendChild(deleteButton);
      resumeList.appendChild(row);
    });
  });

  boot();

  function renderAttachmentTray() {
    attachmentTray.innerHTML = "";
    attachmentTray.style.display = pendingImages.length ? "flex" : "none";
    pendingImages.forEach((item, index) => {
      const node = attachmentTemplate.content.firstElementChild.cloneNode(true);
      node.querySelector(".attachment-thumb").src = item.url;
      node.querySelector(".attachment-remove").addEventListener("click", () => {
        URL.revokeObjectURL(item.url);
        pendingImages.splice(index, 1);
        renderAttachmentTray();
        updateSendState();
      });
      attachmentTray.appendChild(node);
    });
  }

  function messageKey(message) {
    return message.id || [message.role, message.createdAt, message.content].join(":");
  }

  function findMessageNode(id) {
    return timeline.querySelector('.bubble-row[data-message-id="' + id + '"]');
  }

  function findDraftNode() {
    return timeline.querySelector(".bubble-row.is-draft");
  }

  function findLastUserNode() {
    const nodes = timeline.querySelectorAll(".bubble-row.role-user");
    return nodes.length ? nodes[nodes.length - 1] : null;
  }

  function removeOtherDrafts(finalId) {
    timeline.querySelectorAll(".bubble-row.is-draft").forEach((node) => {
      if (node.dataset.messageId !== finalId) {
        node.remove();
      }
    });
  }

  function streamTo(node, targetText) {
    const key = node.dataset.messageId || crypto.randomUUID();
    node.dataset.messageId = key;
    const bubble = node.querySelector(".bubble");
    const previous = streamStates.get(key);

    if (previous && previous.mode === "typing") {
      previous.bubble = bubble;
      previous.node = node;
      previous.target = targetText;
      streamStates.set(key, previous);
      if (!previous.timer) {
        scheduleStreamTick(key, previous);
      }
      return;
    }

    const state = {
      node,
      bubble,
      mode: "typing",
      displayed: bubble.textContent || "",
      target: targetText,
      timer: null,
    };

    streamStates.set(key, state);
    scheduleStreamTick(key, state);
  }

  function stopStream(node) {
    const key = node && node.dataset ? node.dataset.messageId : "";
    if (!key) return;
    const state = streamStates.get(key);
    if (!state) return;
    if (state.timer) clearTimeout(state.timer);
    streamStates.delete(key);
  }

  function scheduleStreamTick(key, state) {
    state.timer = setTimeout(() => tickStream(key), 14);
    streamStates.set(key, state);
  }

  function tickStream(key) {
    const state = streamStates.get(key);
    if (!state || !state.node || !document.body.contains(state.node)) {
      streamStates.delete(key);
      return;
    }

    if (state.displayed === state.target) {
      state.timer = null;
      streamStates.set(key, state);
      return;
    }

    if (!state.target.startsWith(state.displayed)) {
      state.displayed = "";
    }

    const nextLength = Math.min(state.displayed.length + 1, state.target.length);
    state.displayed = state.target.slice(0, nextLength);
    state.bubble.textContent = state.displayed;
    scrollToBottom();
    scheduleStreamTick(key, state);
  }

  function scrollToBottom() {
    requestAnimationFrame(() => {
      window.scrollTo(0, document.body.scrollHeight);
      timeline.scrollTop = timeline.scrollHeight;
    });
  }
})();
