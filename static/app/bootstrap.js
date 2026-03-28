form.addEventListener("submit", function (evt) {
  evt.preventDefault();
  submitPrompt();
});

input.addEventListener("input", function () {
  autoResize();
  updateCommandPalette();
  updateSendState();
});

input.addEventListener("keydown", function (evt) {
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

imageBtn.addEventListener("click", function () {
  imageInput.click();
});

document.addEventListener("click", function (evt) {
  if (!commandPalette.hidden && !commandPalette.contains(evt.target) && evt.target !== input) {
    hideCommandPalette();
  }
});

imageInput.addEventListener("change", function () {
  var files = Array.from(imageInput.files || []);
  imageInput.value = "";
  addPendingImageFiles(files);
});

input.addEventListener("paste", function (evt) {
  if (isRunning || !evt.clipboardData) {
    return;
  }
  var files = [];
  Array.from(evt.clipboardData.items || []).forEach(function (item) {
    if (!item || item.kind !== "file") {
      return;
    }
    var file = item.getAsFile();
    if (file) {
      files.push(file);
    }
  });
  if (!files.length) {
    return;
  }
  if (addPendingImageFiles(files)) {
    evt.preventDefault();
  }
});

loginForm.addEventListener("submit", async function (evt) {
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

newSessionChoice.addEventListener("click", async function () {
  try {
    resumeEmpty.hidden = true;
    await createSession(workdirInput.value);
    enterApp();
  } catch (err) {
    resumeEmpty.hidden = false;
    resumeEmpty.textContent = err && err.message ? err.message : "新建会话失败";
  }
});

resumeSessionChoice.addEventListener("click", function () {
  var raw = resumeSessionChoice.dataset.items || "[]";
  var items = JSON.parse(raw);
  resumeList.innerHTML = "";
  resumeList.hidden = false;
  resumeEmpty.hidden = items.length > 0;
  items.forEach(function (item) {
    var row = document.createElement("div");
    row.className = "resume-item";

    var openButton = document.createElement("button");
    openButton.type = "button";
    openButton.className = "resume-open";
    openButton.innerHTML = '<div class="resume-item-title">' + shortSession(item.id) + '</div><div class="resume-item-desc">' + resumeSummary(item) + '</div>';
    openButton.addEventListener("click", async function () {
      await switchSession(item.id, false);
      enterApp();
    });

    var deleteButton = document.createElement("button");
    deleteButton.type = "button";
    deleteButton.className = "resume-delete";
    deleteButton.textContent = "×";
    deleteButton.setAttribute("aria-label", "删除会话");
    deleteButton.addEventListener("click", async function (evt) {
      evt.stopPropagation();
      var res = await fetch("/api/command", {
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
      var remaining = Array.from(resumeList.querySelectorAll(".resume-item")).length;
      if (!remaining) {
        resumeEmpty.hidden = false;
        resumeEmpty.textContent = "没有可恢复的历史会话";
        resumeList.hidden = true;
        resumeSessionChoice.disabled = true;
      }
      var nextItems = items.filter(function (session) {
        return session.id !== item.id;
      });
      resumeSessionChoice.dataset.items = JSON.stringify(nextItems);
    });

    row.appendChild(openButton);
    row.appendChild(deleteButton);
    resumeList.appendChild(row);
  });
});

boot();
