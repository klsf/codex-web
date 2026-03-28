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
    footerState.textContent = "Working";
    footerDetail.textContent = compact(message.content || "Codex 正在输出");
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
  var kind = String((event && event.kind) || "").toLowerCase();
  var title = String((event && event.title) || "").toLowerCase();
  if (kind === "command") {
    if (title.includes("shell command")) return "shell";
    return "tool";
  }
  if (kind === "status") {
    if (title.startsWith("turn ")) return "turn";
    if (title.startsWith("task ")) return "task";
    if (title.startsWith("thread ")) return "thread";
    if (title.startsWith("review ")) return "review";
    if (title.startsWith("item ")) return "step";
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
  var title = String((event && event.title) || "").trim().toLowerCase();
  return kind === "status" && title.startsWith("turn ");
}

function renderEvent(event, options) {
  if (!event) return null;
  if (shouldHideEvent(event)) return null;
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
    var bodyEl = document.createElement(kind === "command" ? "pre" : "div");
    bodyEl.className = kind === "command" ? "event-body event-body-command" : "event-body";
    bodyEl.textContent = body;
    bubble.appendChild(bodyEl);
  }

  if (!title && !body) {
    bubble.textContent = eventBody(event);
  }

  scrollToBottom();
  return node;
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

function renderWorkingLabel(text) {
  if (!workingLabel) return;
  workingLabel.innerHTML = "";
  var content = String(text || "Working...");
  Array.from(content).forEach(function (ch, index) {
    var span = document.createElement("span");
    span.className = "working-char";
    span.style.animationDelay = index * 0.08 + "s";
    span.textContent = ch;
    workingLabel.appendChild(span);
  });
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
