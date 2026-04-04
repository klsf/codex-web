function appConfig() {
  return window.__APP_CONFIG || {};
}

function availableProviders() {
  return Array.isArray(appConfig().providers) ? appConfig().providers : [];
}

function providerDisplayName(providerID) {
  var current = String(providerID || currentProvider || "").trim().toLowerCase();
  var matched = availableProviders().find(function (item) {
    return item && String(item.id || "").trim().toLowerCase() === current;
  });
  if (matched && matched.name) {
    return String(matched.name).trim();
  }
  return current || "Claude";
}

function currentProviderConfig(providerID) {
  var current = String(providerID || currentProvider || "").trim().toLowerCase();
  return availableProviders().find(function (item) {
    return item && String(item.id || "").trim().toLowerCase() === current;
  }) || null;
}

function providerModels(providerID) {
  var provider = currentProviderConfig(providerID);
  return provider && Array.isArray(provider.models) ? provider.models.slice() : [];
}

function setNodeText(node, value) {
  if (!node) return;
  node.textContent = value;
}

function providerIcon(providerID) {
  var id = String(providerID || "").trim().toLowerCase();
  if (id === "claude") {
    return '' +
      '<svg viewBox="0 0 48 48" aria-hidden="true">' +
        '<circle cx="24" cy="24" r="10"></circle>' +
        '<path d="M24 5v7M24 36v7M5 24h7M36 24h7M11 11l5 5M32 32l5 5M11 37l5-5M32 16l5-5"></path>' +
      '</svg>';
  }
  return '' +
    '<svg viewBox="0 0 48 48" aria-hidden="true">' +
      '<path d="M34 12c-2.5-3-6-4.5-10.5-4.5C14.4 7.5 8 13.7 8 24s6.4 16.5 15.5 16.5c4.5 0 8-1.5 10.5-4.5"></path>' +
      '<path d="M30 16h8v16h-8"></path>' +
      '<path d="M22 16a8 8 0 1 0 0 16"></path>' +
    '</svg>';
}

function syncProviderPicker() {
  return;
}

function setCurrentProvider(providerID) {
  var nextProvider = String(providerID || "").trim().toLowerCase();
  if (!nextProvider) return;
  currentProvider = nextProvider;
  syncProviderPicker();
  syncChooserProviderPicker();
  setNodeText(statusProvider, providerDisplayName(nextProvider));
  populateModelSelect(nextProvider);
}

function populateProviderSelect() {
  var items = availableProviders().filter(function (item) { return item; });
  if (providerPickerChooser) {
    providerPickerChooser.innerHTML = "";
  }
  items.forEach(function (item) {
    if (providerPickerChooser) {
      providerPickerChooser.appendChild(createProviderButton(item, false));
    }
  });
  syncProviderPicker();
  syncChooserProviderPicker();
}

function createProviderButton(item, switchNow) {
  var button = document.createElement("button");
  button.type = "button";
  button.className = "provider-option";
  button.dataset.providerId = item.id;
  button.setAttribute("role", "radio");
  button.setAttribute("aria-label", item.name || item.id);
  button.innerHTML =
    '<span class="provider-option-icon">' + providerIcon(item.id) + '</span>' +
    '<span class="provider-option-name">' + (item.name || item.id) + '</span>';
  button.addEventListener("click", function () {
    setCurrentProvider(item.id);
  });
  return button;
}

function syncChooserProviderPicker() {
  if (!providerPickerChooser) return;
  Array.from(providerPickerChooser.querySelectorAll("[data-provider-id]")).forEach(function (button) {
    var selected = String(button.dataset.providerId || "") === String(currentProvider || "");
    button.classList.toggle("is-selected", selected);
    button.setAttribute("aria-checked", selected ? "true" : "false");
  });
}

function populateModelSelect(providerID, currentModel) {
  if (!modelSelect) return;
  var models = providerModels(providerID);
  var selected = String(currentModel || modelSelect.value || "").trim();
  if (selected && models.indexOf(selected) === -1) {
    models.push(selected);
  }
  if (!models.length) {
    models.push(String(appConfig().model || "unknown"));
  }
  modelSelect.innerHTML = "";
  models.forEach(function (model) {
    var option = document.createElement("option");
    option.value = model;
    option.textContent = model;
    modelSelect.appendChild(option);
  });
  modelSelect.value = selected && models.indexOf(selected) !== -1 ? selected : models[0];
}

function formatTime(value) {
  if (!value) return "--:--";
  return new Date(value).toLocaleString("zh-CN", {
    hour12: false,
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit"
  });
}

function shortSession(id) {
  return id ? String(id).slice(0, 8) : "unknown";
}

function fullSession(id) {
  return String(id || "").trim() || "unknown";
}

function compact(text) {
  return String(text || "").replace(/\s+/g, " ").trim().slice(0, 120) || "等待输入";
}

function renderMarkdown(node, text) {
  var bubble = node.querySelector(".bubble");
  var source = String(text || "");
  if (!window.marked || !window.DOMPurify) {
    bubble.textContent = source;
    return;
  }
  window.marked.setOptions({ breaks: true, gfm: true });
  bubble.innerHTML = window.DOMPurify.sanitize(window.marked.parse(source));
}

function messageKey(message, draft) {
  if (!message) return "";
  if (draft) return String(message.id || "draft");
  return String(message.id || [message.role, message.createdAt, message.content].join(":"));
}

function findMessageNode(id) {
  return timeline.querySelector('.bubble-row[data-message-id="' + id + '"]');
}

function eventKey(event) {
  if (!event) return "";
  return "event-" + String(event.id || [event.kind, event.title, event.createdAt].join(":"));
}

function renderMessage(message, options) {
  if (!message) return null;
  var settings = options || {};
  var key = messageKey(message, settings.draft);
  var node = findMessageNode(key);
  if (!node) {
    node = template.content.firstElementChild.cloneNode(true);
    timeline.appendChild(node);
  }
  node.dataset.messageId = key;
  node.classList.remove("role-user", "role-assistant", "role-system", "is-draft");
  node.classList.add("role-" + message.role);
  if (settings.draft) {
    node.classList.add("is-draft");
  }
  node.querySelector(".bubble-meta").textContent = formatTime(message.createdAt);
  renderImages(node, message.imageUrls || []);
  if (message.role === "assistant") {
    renderMarkdown(node, message.content || "");
  } else {
    node.querySelector(".bubble").textContent = message.content || "";
  }
  node.querySelector(".bubble").hidden = !String(message.content || "").trim() && Array.isArray(message.imageUrls) && message.imageUrls.length > 0;
  scrollToBottom();
  return node;
}

function shouldHideEvent(event) {
  if (!event) return true;
  var stepType = normalizeEventToken(event.stepType);
  var title = normalizeEventToken(event.title);
  var target = normalizeEventToken(event.target);
  if (stepType === "result") {
    return true;
  }
  if (stepType === "thread" || stepType === "turn" || title === "thread" || title === "turn") {
    return true;
  }
  if (target === "thread" || target === "turn") {
    return true;
  }
  return !String(event.title || event.body || event.target || "").trim();
}

function renderEvent(event) {
  if (!event || shouldHideEvent(event)) return null;
  var key = eventKey(event);
  var node = findMessageNode(key);
  if (!node) {
    node = template.content.firstElementChild.cloneNode(true);
    timeline.appendChild(node);
  }
  node.dataset.messageId = key;
  node.classList.remove("role-user", "role-assistant", "role-system", "is-event", "event-command", "event-status", "event-reasoning");
  node.classList.add("role-system", "is-event");
  if (isReasoningEvent(event)) {
    node.classList.add("event-reasoning");
  } else {
    node.classList.add(String(event.kind || "").toLowerCase() === "command" ? "event-command" : "event-status");
  }
  node.querySelector(".bubble-meta").textContent = eventMeta(event);

  var bubble = node.querySelector(".bubble");
  bubble.innerHTML = "";
  bubble.classList.add("event-collapsible");

  var detailsNode = document.createElement("details");
  detailsNode.className = "event-details";
  var summaryNode = document.createElement("summary");
  summaryNode.className = "event-summary";
  var summaryTextNode = document.createElement("span");
  summaryTextNode.className = "event-summary-text";
  summaryTextNode.textContent = eventSummaryText(event);
  summaryNode.appendChild(summaryTextNode);
  detailsNode.appendChild(summaryNode);

  var contentNode = document.createElement("div");
  contentNode.className = "event-content";

  if (event.title) {
    var titleNode = document.createElement("div");
    titleNode.className = "event-title";
    titleNode.textContent = eventTitleText(event);
    contentNode.appendChild(titleNode);
  }
  if (event.target && !isReasoningEvent(event)) {
    var targetNode = document.createElement("div");
    targetNode.className = "event-target";
    targetNode.textContent = event.target;
    contentNode.appendChild(targetNode);
  }
  if (event.body) {
    var bodyNode = document.createElement(isReasoningEvent(event) ? "div" : "pre");
    bodyNode.className = isReasoningEvent(event) ? "event-reasoning-body" : "event-body";
    bodyNode.textContent = reasoningBodyText(event);
    contentNode.appendChild(bodyNode);
  }
  detailsNode.appendChild(contentNode);
  bubble.appendChild(detailsNode);
  scrollToBottom();
  return node;
}

function eventMeta(event) {
  var labels = [];
  if (isReasoningEvent(event)) {
    labels.push("reasoning");
  } else {
    if (event.kind) labels.push(String(event.kind));
    if (event.phase && event.phase !== "started") labels.push(String(event.phase));
  }
  labels.push(formatTime(event.createdAt));
  return labels.join(" · ");
}

function normalizeEventToken(value) {
  return String(value || "").trim().toLowerCase().replace(/[\s_-]+/g, "");
}

function isReasoningEvent(event) {
  var stepType = normalizeEventToken(event && event.stepType);
  var title = normalizeEventToken(event && event.title);
  var body = normalizeEventToken(event && event.body);
  return stepType === "reasoning" || title === "reasoning" || body.indexOf("reasoning") === 0;
}

function eventTitleText(event) {
  if (isReasoningEvent(event)) {
    return "思考中";
  }
  return String(event && event.title || "");
}

function reasoningBodyText(event) {
  var text = String(event && event.body || "").trim();
  if (!isReasoningEvent(event)) {
    return text;
  }
  if (!text) {
    return "正在整理思路与下一步动作";
  }
  text = text.replace(/^reasoning\s*:?\s*/i, "").trim();
  if (text.length > 160) {
    text = text.slice(0, 160).trim() + "...";
  }
  return text || "正在整理思路与下一步动作";
}

function eventSummaryText(event) {
  var parts = [];
  var title = String(eventTitleText(event) || "").trim();
  var target = String(event && event.target || "").trim();
  var body = String(reasoningBodyText(event) || "").trim();
  if (title) {
    parts.push(title);
  }
  if (target && !isReasoningEvent(event)) {
    parts.push(target);
  } else if (body) {
    parts.push(body);
  }
  return compact(parts.join(" · ")) || "查看详情";
}

function removeDraftMessages(finalID) {
  Array.from(timeline.querySelectorAll(".bubble-row.is-draft")).forEach(function (node) {
    if (finalID && node.dataset.messageId === finalID) return;
    node.remove();
  });
}

function showLoginScreen() {
  isAuthenticated = false;
  document.body.classList.add("auth-required");
  loginScreen.hidden = false;
  sessionChooser.hidden = true;
  loginError.textContent = "";
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
  resumeEmpty.hidden = true;
}

function hideSessionChooser() {
  document.body.classList.remove("auth-required");
  sessionChooser.hidden = true;
  resumeEmpty.hidden = true;
}

function showSessionModal() {
  if (!sessionModal) return;
  sessionModal.hidden = false;
}

function hideSessionModal() {
  if (!sessionModal) return;
  sessionModal.hidden = true;
}

function showActionModal() {
  if (!actionModal) return;
  actionModal.hidden = false;
}

function hideActionModal() {
  if (!actionModal) return;
  actionModal.hidden = true;
}

function replaceTimeline(messages, events, draftMessage) {
  timeline.innerHTML = "";
  var items = [];
  (messages || []).forEach(function (message) {
    items.push({ kind: "message", createdAt: message.createdAt, value: message, order: items.length });
  });
  (events || []).forEach(function (event) {
    items.push({ kind: "event", createdAt: event.createdAt, value: event, order: items.length });
  });
  items.sort(function (left, right) {
    var leftTime = new Date(left.createdAt).getTime();
    var rightTime = new Date(right.createdAt).getTime();
    if (leftTime !== rightTime) {
      return leftTime - rightTime;
    }
    var leftRank = timelineItemRank(left);
    var rightRank = timelineItemRank(right);
    if (leftRank !== rightRank) {
      return leftRank - rightRank;
    }
    return Number(left.order || 0) - Number(right.order || 0);
  });
  if (!items.length && !draftMessage) {
    renderEmpty();
    return;
  }
  items.forEach(function (item) {
    if (item.kind === "message") {
      renderMessage(item.value);
      return;
    }
    renderEvent(item.value);
  });
  if (draftMessage) {
    renderMessage(draftMessage, { draft: true });
  }
}

function timelineItemRank(item) {
  if (!item || !item.value) return 9;
  if (item.kind === "event") return 1;
  var role = String(item.value.role || "").toLowerCase();
  if (role === "user") return 0;
  if (role === "assistant") return 2;
  return 3;
}

function renderEmpty() {
  timeline.innerHTML = '' +
    '<div class="empty-state">' +
      '<div class="empty-state-title">准备开始</div>' +
      '<div class="empty-state-body">你可以给我分配一个任务，我会尽力完成它。</div>' +
    '</div>';
}

function renderImages(node, imageUrls) {
  var existing = node.querySelector(".bubble-images");
  if (existing) existing.remove();
  if (!imageUrls || !imageUrls.length) {
    return;
  }
  var wrap = document.createElement("div");
  wrap.className = "bubble-images";
  imageUrls.forEach(function (url) {
    var img = document.createElement("img");
    img.className = "bubble-image";
    img.src = url;
    img.alt = "upload";
    wrap.appendChild(img);
  });
  node.insertBefore(wrap, node.querySelector(".bubble"));
}

function renderAttachmentTray() {
  if (!attachmentTray || !attachmentTemplate) return;
  attachmentTray.innerHTML = "";
  attachmentTray.style.display = pendingImages.length ? "flex" : "none";
  pendingImages.forEach(function (item, index) {
    var node = attachmentTemplate.content.firstElementChild.cloneNode(true);
    node.querySelector(".attachment-thumb").src = item.url;
    node.querySelector(".attachment-remove").addEventListener("click", function () {
      URL.revokeObjectURL(item.url);
      pendingImages.splice(index, 1);
      renderAttachmentTray();
      updateSendState();
    });
    attachmentTray.appendChild(node);
  });
}

function scrollToBottom() {
  requestAnimationFrame(function () {
    timeline.scrollTop = timeline.scrollHeight;
  });
}

function setFooterStatus(state, detail) {
  setNodeText(footerState, String(state || "ready"));
  if (detail != null) {
    setNodeText(footerDetail, String(detail));
  }
}

function connectionStateCopy(state) {
  switch (String(state || "").toLowerCase()) {
    case "connecting":
      return { badge: "link", title: "正在建立连接", detail: "正在与当前会话建立实时通道。" };
    case "reconnecting":
      return { badge: "retry", title: "正在恢复连接", detail: "实时连接已断开，系统会自动重试。" };
    case "error":
      return { badge: "error", title: "连接异常", detail: "实时通道暂不可用，可稍后重试。" };
    default:
      return { badge: "live", title: "连接正常", detail: "实时消息会显示在这里。" };
  }
}

function setConnectionBanner(state, detail) {
  if (!connectionBanner) return;
  var next = connectionStateCopy(state);
  var current = String(state || "connected").toLowerCase();
  connectionBanner.hidden = current === "connected";
  connectionBadge.textContent = next.badge;
  connectionTitle.textContent = next.title;
  connectionDetail.textContent = String(detail || next.detail || "").trim();
}

function setTransportState(state) {
  setNodeText(transportBadge, state);
  setNodeText(desktopTransportBadge, state);
  setNodeText(statusTransport, state);
  setConnectionBanner(state);
  if (!isRunning) {
    setFooterStatus(state === "connected" ? "ready" : state, transportDetail(state));
  }
}

function setTaskState(running) {
  isRunning = Boolean(running);
  if (imageBtn) imageBtn.disabled = isRunning;
  if (modelSelect) modelSelect.disabled = isRunning;
  sendBtn.disabled = isRunning || !String(input.value || "").trim();
  setNodeText(statusTask, isRunning ? "running" : "idle");
  if (isRunning) {
    setFooterStatus("working", providerDisplayName() + " 正在生成");
  } else if (String(statusTransport.textContent || "") === "connected") {
    setFooterStatus("ready", "等待输入");
  }
}

function setSession(id) {
  currentSessionId = id;
  localStorage.setItem("sessionId", id);
  setNodeText(sessionBadge, shortSession(id));
  setNodeText(desktopSessionBadge, shortSession(id));
  setNodeText(statusSession, shortSession(id));
  if (sessionBadge) sessionBadge.title = fullSession(id);
  if (desktopSessionBadge) desktopSessionBadge.title = fullSession(id);
  if (statusSession) statusSession.title = fullSession(id);
}

function setMeta(meta) {
  if (!meta) return;
  if (meta.provider) setCurrentProvider(meta.provider);
  if (meta.model) {
    setNodeText(modelBadge, meta.model);
    setNodeText(statusModel, meta.model);
    populateModelSelect(meta.provider || currentProvider, meta.model);
  }
  if (meta.cwd) {
    setNodeText(cwdBadge, meta.cwd);
    setNodeText(statusCwd, meta.cwd);
  }
}

function applyBuildInfo() {
  var config = appConfig();
  var version = String(config.version || "dev").trim() || "dev";
  if (version.charAt(0) !== "v") {
    version = "v" + version;
  }
  document.title = String(config.appName || "Code Web").trim() || "Code Web";
  Array.from(appTitleNodes || []).forEach(function (node) {
    node.textContent = String(config.appName || "Code Web").trim() || "Code Web";
  });
  Array.from(versionNodes || []).forEach(function (node) {
    node.textContent = version;
  });
  populateProviderSelect();
  setCurrentProvider(currentProvider);
}

function showError(message) {
  var text = compact(message || "操作失败");
  setFooterStatus("error", text);
  if (!errorToast) return;
  errorToast.textContent = text;
  errorToast.hidden = false;
  clearTimeout(errorToastTimer);
  errorToastTimer = setTimeout(function () {
    errorToast.hidden = true;
  }, 3200);
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
    if (!canAcceptImageFile(file)) return;
    pendingImages.push({
      file: file,
      url: URL.createObjectURL(file)
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

function transportDetail(state) {
  if (state === "connected") return "等待输入";
  if (state === "connecting") return "正在建立连接";
  if (state === "reconnecting") return "正在恢复连接";
  return "连接不可用";
}

function renderResumeList(items, options) {
  var settings = options || {};
  if (!resumeList) return;
  resumeList.innerHTML = "";
  (items || []).forEach(function (item) {
    var row = document.createElement("div");
    row.className = "resume-item";

    var button = document.createElement("button");
    button.type = "button";
    button.className = "resume-open";

    var title = document.createElement("div");
    title.className = "resume-item-title";
    title.textContent = item.title || shortSession(item.id);
    button.appendChild(title);

    var meta = document.createElement("div");
    meta.className = "resume-item-meta";
    resumeBadges(item).forEach(function (badge) {
      var chip = document.createElement("span");
      chip.className = badge.className;
      chip.textContent = badge.text;
      meta.appendChild(chip);
    });
    button.appendChild(meta);

    var desc = document.createElement("div");
    desc.className = "resume-item-desc";
    desc.textContent = [String(item.provider || "").toUpperCase(), compact(item.workdir || item.lastMessage || item.lastEvent || "")].filter(Boolean).join(" · ");
    button.appendChild(desc);

    var summary = document.createElement("div");
    summary.className = "resume-item-summary";
    summary.textContent = [
      item.updatedAt ? "更新 " + formatTime(item.updatedAt) : "",
      typeof item.messageCount === "number" ? "消息 " + item.messageCount : ""
    ].filter(Boolean).join(" · ");
    button.appendChild(summary);

    button.addEventListener("click", async function () {
      if (typeof settings.onOpen === "function") {
        await settings.onOpen(item, button);
      }
    });

    var actions = document.createElement("div");
    actions.className = "resume-actions";
    var deleteBtn = document.createElement("button");
    deleteBtn.type = "button";
    deleteBtn.className = "resume-delete";
    deleteBtn.textContent = "删除";
    deleteBtn.addEventListener("click", async function (evt) {
      evt.stopPropagation();
      if (typeof settings.onDelete === "function") {
        await settings.onDelete(item, row);
      }
    });
    actions.appendChild(deleteBtn);

    row.appendChild(button);
    row.appendChild(actions);
    resumeList.appendChild(row);
  });
}

function resumeBadges(item) {
  var badges = [];
  badges.push({ text: item && item.restoreRef ? "历史会话" : "当前会话", className: "resume-badge" });
  if (item && item.running) {
    badges.push({ text: "运行中", className: "resume-badge is-running" });
  } else {
    badges.push({ text: "空闲", className: "resume-badge is-idle" });
  }
  return badges;
}
