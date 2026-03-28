function renderMessage(message, options) {
  var settings = Object.assign({ draft: false, animate: false }, options || {});
  var key = messageKey(message);
  var node = findMessageNode(key);
  if (!node && settings.draft) {
    node = findDraftNode();
  }
  var isNew = !node;

  if (!node) {
    node = template.content.firstElementChild.cloneNode(true);
    timeline.appendChild(node);
  }
  node.dataset.messageId = key;

  node.classList.remove("role-user", "role-assistant", "role-system", "is-draft", "is-streaming", "is-working");
  node.classList.add("role-" + message.role);
  if (settings.draft) {
    node.classList.add("is-draft");
    node.classList.add("is-streaming");
    activeDraftId = key;
  } else if (activeDraftId === key || message.role === "assistant") {
    activeDraftId = "";
  }

  node.querySelector(".bubble-meta").textContent = metaForMessage(message);
  renderImages(node, message.imageUrls || []);

  var bubble = node.querySelector(".bubble");
  if (settings.draft) {
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
    setFooterStatus("Working", compact(message.content || "Codex 正在输出"));
  } else if (message.role === "assistant") {
    removeWorkingPlaceholder();
  }
  scrollToBottom();
  return node;
}

function eventKey(event) {
  return "event-" + String(event && event.id ? event.id : crypto.randomUUID());
}

function metaForEvent(event) {
  var label = eventBadge(event);
  var time = formatTime(event && event.createdAt);
  return label ? label + " · " + time : time;
}

function eventBadge(event) {
  var category = eventCategory(event);
  var kind = String((event && event.kind) || "").toLowerCase();
  if (category === "command" || kind === "command") {
    if (eventStepType(event) === "shellcommand") return "shell";
    return "tool";
  }
  if (category) {
    return category;
  }
  if (kind === "status") {
    return "status";
  }
  return kind || "event";
}

function eventBody(event) {
  var parts = [];
  var title = String((event && event.title) || "").trim();
  var body = String((event && event.body) || "").trim();
  if (title) parts.push(title);
  if (body) parts.push(body);
  return parts.join("\n\n");
}

function shouldHideEvent(event) {
  var kind = String((event && event.kind) || "").toLowerCase();
  var category = eventCategory(event);
  var stepType = eventStepType(event);
  var phase = String((event && event.phase) || "").toLowerCase();
  if (kind === "command") {
    return false;
  }
  if (category === "turn" || category === "task") {
    return true;
  }
  if (phase === "completed" || phase === "dequeued") {
    return true;
  }
  if (stepType === "reasoning" || stepType === "agentmessage" || stepType === "usermessage") {
    return true;
  }
  return false;
}

function renderEvent(event, options) {
  if (!event) return null;
  if (shouldHideEvent(event)) return null;
  if (shouldGroupEvent(event)) {
    return renderTraceEvent(event);
  }
  var key = eventKey(event);
  var node = findMessageNode(key);
  if (!node) {
    node = template.content.firstElementChild.cloneNode(true);
    timeline.appendChild(node);
  }

  var kind = String(event.kind || "").toLowerCase();
  node.dataset.messageId = key;
  node.classList.remove(
    "role-user",
    "role-assistant",
    "role-system",
    "is-draft",
    "is-streaming",
    "is-working",
    "is-event",
    "event-status",
    "event-command",
    "event-generic"
  );
  node.classList.add("role-system", "is-event");
  if (kind === "command") {
    node.classList.add("event-command");
  } else if (kind === "status") {
    node.classList.add("event-status");
  } else {
    node.classList.add("event-generic");
  }

  node.querySelector(".bubble-meta").textContent = metaForEvent(event);
  var bubble = node.querySelector(".bubble");
  var title = String(event.title || "").trim();
  var body = String(event.body || "").trim();
  bubble.innerHTML = "";

  if (title) {
    var titleEl = document.createElement("div");
    titleEl.className = "event-title";
    titleEl.textContent = title;
    bubble.appendChild(titleEl);
  }

  if (body) {
    if (kind === "command") {
      renderCommandEventBody(bubble, body);
    } else {
      var bodyEl = document.createElement("div");
      bodyEl.className = "event-body";
      bodyEl.textContent = body;
      bubble.appendChild(bodyEl);
    }
  }

  if (!title && !body) {
    bubble.textContent = eventBody(event);
  }

  removeWorkingPlaceholder();
  scrollToBottom();
  return node;
}

function shouldGroupEvent(event) {
  return eventCategory(event) === "step";
}

function renderTraceEvent(event) {
  var node = findActiveTraceNode();
  if (!node) {
    node = template.content.firstElementChild.cloneNode(true);
    node.dataset.traceGroup = "status";
    node.classList.remove(
      "role-user",
      "role-assistant",
      "role-system",
      "is-draft",
      "is-streaming",
      "is-working",
      "is-event",
      "event-status",
      "event-command",
      "event-generic"
    );
    node.classList.add("role-system", "is-event", "event-trace");
    node.querySelector(".bubble").innerHTML = "<div class=\"event-trace-list\"></div><div class=\"event-trace-more\" hidden></div>";
    timeline.appendChild(node);
  }

  node.querySelector(".bubble-meta").textContent = metaForTraceEvent(event);
  appendTraceItem(node, event);
  removeWorkingPlaceholder();
  scrollToBottom();
  return node;
}

function findActiveTraceNode() {
  var node = timeline.lastElementChild;
  if (!node) return null;
  if (node.dataset && node.dataset.traceGroup === "status") {
    return node;
  }
  return null;
}

function metaForTraceEvent(event) {
  var time = formatTime(event && event.createdAt);
  return time ? "trace · " + time : "trace";
}

function appendTraceItem(node, event) {
  var list = node.querySelector(".event-trace-list");
  if (!list) return;
  var stepType = eventStepType(event);
  var target = String((event && (event.target || event.body)) || "").trim();
  var last = list.lastElementChild;
  if (last && last.dataset && last.dataset.stepType === stepType) {
    mergeTraceItem(last, target);
  } else {
    list.appendChild(createTraceItem(stepType, target));
  }
  trimTraceItems(node, 4);
}

function createTraceItem(stepType, target) {
  var item = document.createElement("div");
  item.className = "event-trace-item";
  item.dataset.stepType = stepType;
  item.dataset.count = "1";

  var titleEl = document.createElement("span");
  titleEl.className = "event-trace-title";
  titleEl.textContent = traceStepLabel(stepType, 1);
  item.appendChild(titleEl);

  var bodyEl = document.createElement("span");
  bodyEl.className = "event-trace-body";
  bodyEl.textContent = target;
  item.appendChild(bodyEl);

  return item;
}

function mergeTraceItem(item, target) {
  var count = Number(item.dataset.count || 1) + 1;
  item.dataset.count = String(count);
  var titleEl = item.querySelector(".event-trace-title");
  if (titleEl) {
    titleEl.textContent = traceStepLabel(item.dataset.stepType || "", count);
  }
  var bodyEl = item.querySelector(".event-trace-body");
  if (!bodyEl) return;
  var existing = String(bodyEl.textContent || "").trim();
  if (!target) {
    return;
  }
  if (!existing) {
    bodyEl.textContent = target;
    return;
  }
  if (existing.split(" • ").indexOf(target) !== -1) {
    return;
  }
  var parts = existing.split(" • ").filter(Boolean);
  parts.push(target);
  if (parts.length > 3) {
    parts.splice(0, parts.length - 3);
  }
  bodyEl.textContent = parts.join(" • ");
}

function traceStepLabel(stepType, count) {
  var base = stepTypeLabel(stepType);
  if (count > 1) {
    return base + " ×" + count;
  }
  return base;
}

function stepTypeLabel(stepType) {
  switch (String(stepType || "").toLowerCase()) {
    case "readfile":
      return "read file";
    case "writefile":
      return "write file";
    case "editfile":
    case "patchfile":
      return "edit file";
    case "searchfiles":
    case "glob":
    case "findfiles":
      return "find files";
    case "grep":
    case "searchtext":
      return "search text";
    case "openfile":
    case "viewimage":
      return "open file";
    case "fetchurl":
    case "openurl":
      return "open url";
    case "shellcommand":
      return "shell";
    case "listdir":
    case "readdir":
      return "list directory";
    case "filechange":
      return "file change";
    case "websearch":
      return "web search";
    default:
      return String(stepType || "step").replace(/_/g, " ");
  }
}

function eventCategory(event) {
  var category = String((event && event.category) || "").toLowerCase();
  if (category) return category;
  var kind = String((event && event.kind) || "").toLowerCase();
  var title = String((event && event.title) || "").trim().toLowerCase();
  if (kind === "command") return "command";
  if (title.startsWith("task ")) return "task";
  if (title.startsWith("turn ")) return "turn";
  if (title.startsWith("thread ")) return "thread";
  if (title.startsWith("review ")) return "review";
  if (kind === "status") return "step";
  return kind;
}

function eventStepType(event) {
  var stepType = String((event && event.stepType) || "").toLowerCase();
  if (stepType) return stepType.replace(/[\s_-]+/g, "");
  var title = String((event && event.title) || "").trim().toLowerCase();
  var body = String((event && event.body) || "").trim().toLowerCase();
  return (body || title).replace(/[\s_-]+/g, "");
}

function trimTraceItems(node, limit) {
  var list = node.querySelector(".event-trace-list");
  var more = node.querySelector(".event-trace-more");
  if (!list || !more) return;

  var hiddenCount = Number(node.dataset.traceHiddenCount || 0);
  while (list.children.length > limit) {
    list.removeChild(list.firstElementChild);
    hiddenCount += 1;
  }

  node.dataset.traceHiddenCount = String(hiddenCount);
  if (hiddenCount > 0) {
    more.hidden = false;
    more.textContent = "+" + hiddenCount + " earlier steps";
  } else {
    more.hidden = true;
    more.textContent = "";
  }
}

function renderCommandEventBody(bubble, body) {
  var parsed = parseCommandEventBody(body);
  if (parsed.command) {
    var commandEl = document.createElement("pre");
    commandEl.className = "event-body event-body-command event-command-line";
    commandEl.textContent = parsed.command;
    bubble.appendChild(commandEl);
  }

  if (parsed.output) {
    var detailsEl = document.createElement("details");
    detailsEl.className = "event-command-output";
    if (!parsed.collapsed) {
      detailsEl.open = true;
    }

    var summaryEl = document.createElement("summary");
    summaryEl.className = "event-command-summary";
    summaryEl.textContent = parsed.collapsed ? "show output" : "output";
    if (parsed.preview) {
      var previewEl = document.createElement("span");
      previewEl.className = "event-command-preview";
      previewEl.textContent = parsed.preview;
      summaryEl.appendChild(previewEl);
    }
    detailsEl.appendChild(summaryEl);

    var outputEl = document.createElement("pre");
    outputEl.className = "event-body event-body-command";
    outputEl.textContent = parsed.output;
    detailsEl.appendChild(outputEl);
    bubble.appendChild(detailsEl);
  }
}

function parseCommandEventBody(body) {
  var text = String(body || "").trim();
  if (!text) {
    return { command: "", output: "", preview: "", collapsed: false };
  }

  var parts = text.split(/\n\s*\n/);
  var command = String(parts.shift() || "").trim();
  var output = String(parts.join("\n\n") || "").trim();
  if (!output && text.includes("\n")) {
    var lines = text.split("\n");
    command = String(lines.shift() || "").trim();
    output = String(lines.join("\n") || "").trim();
  }

  return {
    command: command || text,
    output: output,
    preview: summarizeCommandOutput(output),
    collapsed: output.length > 240 || output.split("\n").length > 8,
  };
}

function summarizeCommandOutput(output) {
  var text = String(output || "").trim();
  if (!text) return "";
  var lines = text.split("\n").map(function (line) {
    return line.trim();
  }).filter(Boolean);
  if (!lines.length) return "";
  var preview = lines.slice(0, 2).join(" | ");
  if (preview.length > 96) {
    preview = preview.slice(0, 96).trim() + "...";
  } else if (lines.length > 2) {
    preview += " ...";
  }
  return preview;
}

function metaForMessage(message) {
  return formatTime(message.createdAt);
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

function revealBody(node, text, animated) {
  var bubble = node.querySelector(".bubble");
  if (!animated) {
    bubble.textContent = text;
    return;
  }

  var chunks = text.match(/.{1,22}(\s|$)|.+$/g) || [text];
  var i = 0;
  var timer = null;

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
    timer: timer,
    node: node,
    target: text,
    displayed: text,
    mode: "reveal",
  });
  step();
}

function replaceTimeline(messages, events, draftMessage) {
  timeline.innerHTML = "";
  activeDraftId = "";
  var items = [];
  (messages || []).forEach(function (message) {
    items.push({ kind: "message", createdAt: message.createdAt, value: message });
  });
  (events || []).forEach(function (event) {
    items.push({ kind: "event", createdAt: event.createdAt, value: event });
  });
  items.sort(function (a, b) {
    return new Date(a.createdAt) - new Date(b.createdAt);
  });
  if (!items.length) {
    renderEmpty();
  }
  items.forEach(function (item) {
    if (item.kind === "message") {
      renderMessage(item.value, { animate: false });
      return;
    }
    if (!shouldHideEvent(item.value) && eventBody(item.value)) {
      renderEvent(item.value, { animate: false });
    }
  });
  if (draftMessage) {
    renderMessage(draftMessage, { draft: true, animate: false });
  }
}

function renderEmpty() {
  timeline.innerHTML = "";
}

function renderAttachmentTray() {
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

function messageKey(message) {
  return message.id || [message.role, message.createdAt, message.content].join(":");
}

function findMessageNode(id) {
  return timeline.querySelector('.bubble-row[data-message-id="' + id + '"]');
}

function findDraftNode() {
  return timeline.querySelector(".bubble-row.is-draft");
}

function removeOtherDrafts(finalId) {
  timeline.querySelectorAll(".bubble-row.is-draft").forEach(function (node) {
    if (node.dataset.messageId !== finalId) {
      node.remove();
    }
  });
}

function streamTo(node, targetText) {
  var key = node.dataset.messageId || crypto.randomUUID();
  node.dataset.messageId = key;
  var bubble = node.querySelector(".bubble");
  var previous = streamStates.get(key);

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

  var state = {
    node: node,
    bubble: bubble,
    mode: "typing",
    displayed: bubble.textContent || "",
    target: targetText,
    timer: null,
  };

  streamStates.set(key, state);
  scheduleStreamTick(key, state);
}

function stopStream(node) {
  var key = node && node.dataset ? node.dataset.messageId : "";
  if (!key) return;
  var state = streamStates.get(key);
  if (!state) return;
  if (state.timer) clearTimeout(state.timer);
  streamStates.delete(key);
}

function scheduleStreamTick(key, state) {
  state.timer = setTimeout(function () {
    tickStream(key);
  }, 14);
  streamStates.set(key, state);
}

function tickStream(key) {
  var state = streamStates.get(key);
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

  var nextLength = Math.min(state.displayed.length + 1, state.target.length);
  state.displayed = state.target.slice(0, nextLength);
  state.bubble.textContent = state.displayed;
  scrollToBottom();
  scheduleStreamTick(key, state);
}

function scrollToBottom() {
  requestAnimationFrame(function () {
    window.scrollTo(0, document.body.scrollHeight);
    timeline.scrollTop = timeline.scrollHeight;
  });
}
