(() => {
  const dialog = document.querySelector("[data-variable-edit-dialog]");
  if (!dialog) {
    return;
  }

  const nameInput = dialog.querySelector("#variable-edit-name");
  const valueInput = dialog.querySelector("#variable-edit-value");
  const operationInput = dialog.querySelector("[name=operation]");
  const title = dialog.querySelector("[data-variable-edit-title]");
  const originalNameInput = dialog.querySelector("[name=original_name]");
  const form = dialog.querySelector("form");
  const submitButton = dialog.querySelector("[data-variable-edit-submit]");
  const closeButtons = dialog.querySelectorAll("[data-variable-edit-close]");
  let returnFocus = null;

  const cleanup = () => {
    document.body.classList.remove("dialog-open");
    if (returnFocus && document.body.contains(returnFocus)) {
      returnFocus.setAttribute("aria-expanded", "false");
      returnFocus.focus();
    }
    returnFocus = null;
  };

  const closeDialog = () => {
    if (typeof dialog.close === "function" && dialog.open) {
      dialog.close();
    } else {
      dialog.removeAttribute("open");
      cleanup();
    }
  };

  const showDialog = () => {
    if (typeof dialog.showModal === "function") {
      if (!dialog.open) {
        dialog.showModal();
      }
    } else {
      dialog.setAttribute("open", "");
    }
    document.body.classList.add("dialog-open");
    if (nameInput) {
      nameInput.focus();
      nameInput.select();
    }
  };

  const setMode = (mode) => {
    const adding = mode === "add";
    if (operationInput) {
      operationInput.value = adding ? "add" : "edit";
    }
    if (title) {
      title.textContent = adding ? "Add variable" : "Edit variable";
    }
    if (submitButton) {
      submitButton.disabled = false;
      submitButton.textContent = "Save";
    }
  };

  const openEditDialog = (button) => {
    returnFocus = button;
    button.setAttribute("aria-expanded", "true");
    setMode("edit");
    if (originalNameInput) {
      originalNameInput.value = button.dataset.variableKey || "";
    }
    if (nameInput) {
      nameInput.value = button.dataset.variableKey || "";
    }
    if (valueInput) {
      valueInput.value = button.dataset.variableValue || "";
    }
    showDialog();
  };

  const openAddDialog = (button) => {
    returnFocus = button;
    button.setAttribute("aria-expanded", "true");
    setMode("add");
    if (originalNameInput) {
      originalNameInput.value = "";
    }
    if (nameInput) {
      nameInput.value = "";
    }
    if (valueInput) {
      valueInput.value = "";
    }
    showDialog();
  };

  document.querySelectorAll("[data-variable-edit]").forEach((button) => {
    button.addEventListener("click", () => openEditDialog(button));
  });

  document.querySelectorAll("[data-variable-add]").forEach((button) => {
    button.addEventListener("click", () => openAddDialog(button));
  });

  closeButtons.forEach((button) => {
    button.addEventListener("click", closeDialog);
  });

  dialog.addEventListener("click", (event) => {
    if (event.target === dialog) {
      closeDialog();
    }
  });

  dialog.addEventListener("cancel", (event) => {
    event.preventDefault();
    closeDialog();
  });

  dialog.addEventListener("close", cleanup);

  if (form) {
    form.addEventListener("submit", () => {
      if (submitButton) {
        submitButton.disabled = true;
        submitButton.textContent = "Saving…";
      }
    });
  }

  if (dialog.hasAttribute("data-variable-edit-open")) {
    setMode(operationInput && operationInput.value === "add" ? "add" : "edit");
    showDialog();
  }
})();

(() => {
  const dialog = document.querySelector("[data-variable-delete-dialog]");
  if (!dialog) {
    return;
  }

  const nameInput = dialog.querySelector("[data-variable-delete-name-input]");
  const nameDisplay = dialog.querySelector("[data-variable-delete-name-display]");
  const cancelButton = dialog.querySelector("[data-variable-delete-cancel]");
  const form = dialog.querySelector("form");
  const submitButton = dialog.querySelector("[data-variable-delete-submit]");
  const closeButtons = dialog.querySelectorAll("[data-variable-delete-close]");
  let returnFocus = null;

  const setName = (name) => {
    if (nameInput) {
      nameInput.value = name;
    }
    if (nameDisplay) {
      nameDisplay.textContent = name;
    }
  };

  const cleanup = () => {
    document.body.classList.remove("dialog-open");
    if (returnFocus && document.body.contains(returnFocus)) {
      returnFocus.setAttribute("aria-expanded", "false");
      returnFocus.focus();
    }
    returnFocus = null;
  };

  const closeDialog = () => {
    if (typeof dialog.close === "function" && dialog.open) {
      dialog.close();
    } else {
      dialog.removeAttribute("open");
      cleanup();
    }
  };

  const showDialog = () => {
    if (typeof dialog.showModal === "function") {
      if (!dialog.open) {
        dialog.showModal();
      }
    } else {
      dialog.setAttribute("open", "");
    }
    document.body.classList.add("dialog-open");
    if (cancelButton) {
      cancelButton.focus();
    }
  };

  const openDialog = (button) => {
    returnFocus = button;
    button.setAttribute("aria-expanded", "true");
    setName(button.dataset.variableKey || "");
    showDialog();
  };

  document.querySelectorAll("[data-variable-delete]").forEach((button) => {
    button.addEventListener("click", () => openDialog(button));
  });

  closeButtons.forEach((button) => {
    button.addEventListener("click", closeDialog);
  });

  dialog.addEventListener("click", (event) => {
    if (event.target === dialog) {
      closeDialog();
    }
  });

  dialog.addEventListener("cancel", (event) => {
    event.preventDefault();
    closeDialog();
  });

  dialog.addEventListener("close", cleanup);

  if (form) {
    form.addEventListener("submit", () => {
      if (submitButton) {
        submitButton.disabled = true;
        submitButton.textContent = "Deleting…";
      }
    });
  }

  if (dialog.hasAttribute("data-variable-delete-open")) {
    setName(nameInput ? nameInput.value : "");
    showDialog();
  }
})();

(() => {
  const dialog = document.querySelector("[data-secret-edit-dialog]");
  if (!dialog) {
    return;
  }

  const nameInput = dialog.querySelector("#secret-edit-name");
  const valueInput = dialog.querySelector("#secret-edit-value");
  const clearInput = dialog.querySelector("[data-secret-clear]");
  const clearWrap = dialog.querySelector("[data-secret-clear-wrap]");
  const operationInput = dialog.querySelector("[name=operation]");
  const originalNameInput = dialog.querySelector("[name=original_name]");
  const title = dialog.querySelector("[data-secret-edit-title]");
  const submitButton = dialog.querySelector("[data-secret-edit-submit]");
  const toggleButton = dialog.querySelector("[data-secret-toggle]");
  const showIcon = dialog.querySelector("[data-secret-show-icon]");
  const hideIcon = dialog.querySelector("[data-secret-hide-icon]");
  const copyButton = dialog.querySelector("[data-secret-copy]");
  const statusElement = dialog.querySelector("[data-secret-status]");
  const generateButton = dialog.querySelector("[data-secret-generate]");
  const form = dialog.querySelector("form");
  const closeButtons = dialog.querySelectorAll("[data-secret-edit-close]");
  let returnFocus = null;
  let revealedFromServer = false;
  let showingMock = false;
  let editHasValue = false;
  let pendingController = null;
  let statusTimer = 0;

  const MOCK_SECRET_VALUE = "••••••••";

  const setIconHidden = (icon, hidden) => {
    if (icon) {
      icon.toggleAttribute("hidden", hidden);
    }
  };

  const setSecretVisibility = (visible) => {
    if (valueInput) {
      valueInput.type = visible ? "text" : "password";
    }
    if (toggleButton) {
      toggleButton.setAttribute("aria-label", visible ? "Hide secret" : "Show secret");
      toggleButton.setAttribute("aria-pressed", visible ? "true" : "false");
    }
    setIconHidden(showIcon, visible);
    setIconHidden(hideIcon, !visible);
  };

  const setStatus = (message) => {
    if (statusTimer) {
      window.clearTimeout(statusTimer);
      statusTimer = 0;
    }
    if (!statusElement) {
      return;
    }
    if (!message) {
      statusElement.textContent = "";
      statusElement.setAttribute("hidden", "");
      return;
    }
    statusElement.textContent = message;
    statusElement.removeAttribute("hidden");
    statusTimer = window.setTimeout(() => {
      if (statusElement.textContent === message) {
        statusElement.textContent = "";
        statusElement.setAttribute("hidden", "");
      }
      statusTimer = 0;
    }, 3000);
  };

  const showMockValue = () => {
    if (!valueInput) {
      return;
    }
    valueInput.value = MOCK_SECRET_VALUE;
    showingMock = true;
    revealedFromServer = false;
    setSecretVisibility(false);
  };

  const clearMockForEdit = () => {
    if (showingMock && valueInput) {
      valueInput.value = "";
    }
    showingMock = false;
  };

  const isMockShown = () => showingMock && valueInput && valueInput.value === MOCK_SECRET_VALUE;

  const restoreHiddenState = () => {
    // Hiding a revealed secret restores the mock dots in edit mode so the
    // field keeps showing the usual password dots instead of going blank.
    // Truly empty secrets (no existing value) restore to empty.
    revealedFromServer = false;
    if (!isAddMode() && editHasValue) {
      showMockValue();
      return;
    }
    showingMock = false;
    if (valueInput) {
      valueInput.value = "";
    }
    setSecretVisibility(false);
  };

  const setBusy = (busy) => {
    if (toggleButton) {
      toggleButton.disabled = busy;
    }
    if (copyButton) {
      copyButton.disabled = busy;
    }
  };

  const abortPending = () => {
    if (pendingController && typeof pendingController.abort === "function") {
      pendingController.abort();
    }
    pendingController = null;
  };

  const isAddMode = () => operationInput && operationInput.value === "add";

  const currentTargetName = () => {
    if (originalNameInput && originalNameInput.value) {
      return originalNameInput.value.trim();
    }
    return "";
  };

  const revealBaseURL = () => {
    if (dialog.dataset && dialog.dataset.secretRevealBase) {
      return dialog.dataset.secretRevealBase;
    }
    if (form && form.getAttribute("action")) {
      return `${form.getAttribute("action")}/value`;
    }
    return "";
  };

  const fetchSecretValue = async (name, signal) => {
    const base = revealBaseURL();
    if (!base) {
      throw new Error("reveal unavailable");
    }
    const response = await fetch(`${base}?name=${encodeURIComponent(name)}`, {
      headers: { Accept: "application/json" },
      credentials: "same-origin",
      cache: "no-store",
      signal,
    });
    if (!response.ok) {
      throw new Error(`reveal failed: ${response.status}`);
    }
    const data = await response.json();
    if (!data || typeof data.value !== "string") {
      throw new Error("reveal invalid");
    }
    return data.value;
  };

  const copyText = async (text) => {
    if (!navigator.clipboard || typeof navigator.clipboard.writeText !== "function") {
      setStatus("Copy is unavailable in this browser.");
      return false;
    }
    try {
      await navigator.clipboard.writeText(text);
    } catch (error) {
      setStatus("Could not copy the secret. Try again.");
      return false;
    }
    setStatus("Copied to clipboard.");
    return true;
  };

  const cleanup = () => {
    document.body.classList.remove("dialog-open");
    if (returnFocus && document.body.contains(returnFocus)) {
      returnFocus.setAttribute("aria-expanded", "false");
      returnFocus.focus();
    }
    returnFocus = null;
  };

  const closeDialog = () => {
    abortPending();
    revealedFromServer = false;
    showingMock = false;
    setSecretVisibility(false);
    setBusy(false);
    setStatus("");
    if (valueInput) {
      valueInput.value = "";
    }
    if (typeof dialog.close === "function" && dialog.open) {
      dialog.close();
    } else {
      dialog.removeAttribute("open");
      cleanup();
    }
  };

  const showDialog = () => {
    if (typeof dialog.showModal === "function") {
      if (!dialog.open) {
        dialog.showModal();
      }
    } else {
      dialog.setAttribute("open", "");
    }
    document.body.classList.add("dialog-open");
    if (nameInput) {
      nameInput.focus();
      nameInput.select();
    }
  };

  const setMode = (mode) => {
    const adding = mode === "add";
    if (operationInput) {
      operationInput.value = adding ? "add" : "edit";
    }
    if (title) {
      title.textContent = adding ? "Add secret" : "Edit secret";
    }
    if (submitButton) {
      submitButton.disabled = false;
      submitButton.textContent = "Save";
    }
    abortPending();
    revealedFromServer = false;
    showingMock = false;
    setSecretVisibility(false);
    setBusy(false);
    setStatus("");
    if (valueInput) {
      valueInput.value = "";
    }
    if (clearInput) {
      clearInput.checked = false;
    }
    if (clearWrap) {
      clearWrap.toggleAttribute("hidden", adding);
    }
  };

  const openEditDialog = (button) => {
    returnFocus = button;
    button.setAttribute("aria-expanded", "true");
    setMode("edit");
    editHasValue = button.dataset.secretHasValue !== "false";
    if (originalNameInput) {
      originalNameInput.value = button.dataset.secretKey || "";
    }
    if (nameInput) {
      nameInput.value = button.dataset.secretKey || "";
    }
    if (editHasValue) {
      showMockValue();
    } else if (valueInput) {
      valueInput.value = "";
      showingMock = false;
      setSecretVisibility(false);
    }
    if (clearInput) {
      clearInput.checked = false;
    }
    showDialog();
  };

  const openAddDialog = (button) => {
    returnFocus = button;
    button.setAttribute("aria-expanded", "true");
    setMode("add");
    editHasValue = false;
    if (originalNameInput) {
      originalNameInput.value = "";
    }
    if (nameInput) {
      nameInput.value = "";
    }
    if (valueInput) {
      valueInput.value = "";
    }
    showingMock = false;
    showDialog();
  };

  const generateSecret = () => {
    if (!window.crypto || typeof window.crypto.getRandomValues !== "function" || typeof window.btoa !== "function") {
      return "";
    }
    const bytes = new Uint8Array(32);
    window.crypto.getRandomValues(bytes);
    let binary = "";
    bytes.forEach((byte) => {
      binary += String.fromCharCode(byte);
    });
    return window.btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  };

  document.querySelectorAll("[data-secret-edit]").forEach((button) => {
    button.addEventListener("click", () => openEditDialog(button));
  });

  document.querySelectorAll("[data-secret-add]").forEach((button) => {
    button.addEventListener("click", () => openAddDialog(button));
  });

  if (toggleButton) {
    toggleButton.addEventListener("click", async () => {
      if (!valueInput) {
        return;
      }
      if (valueInput.type === "text") {
        restoreHiddenState();
        return;
      }
      // A mock value is not a real secret: Show must fetch instead of
      // merely unmasking the dots.
      if (valueInput.value !== "" && !isMockShown()) {
        setSecretVisibility(true);
        valueInput.focus();
        return;
      }
      if (isAddMode()) {
        setSecretVisibility(true);
        return;
      }
      const name = currentTargetName();
      if (!name) {
        return;
      }
      abortPending();
      const controller = typeof AbortController !== "undefined" ? new AbortController() : null;
      pendingController = controller;
      setBusy(true);
      setStatus("Loading secret…");
      try {
        const secret = await fetchSecretValue(name, controller ? controller.signal : undefined);
        if (pendingController !== controller) {
          return;
        }
        if (secret === "") {
          editHasValue = false;
          showingMock = false;
          valueInput.value = "";
          setSecretVisibility(false);
          setStatus("This secret is empty.");
          valueInput.focus();
          return;
        }
        valueInput.value = secret;
        showingMock = false;
        revealedFromServer = true;
        if (clearInput) {
          clearInput.checked = false;
        }
        setSecretVisibility(true);
        setStatus("");
        valueInput.focus();
      } catch (error) {
        if (error && error.name === "AbortError") {
          return;
        }
        setStatus("Could not load the secret. Try again.");
      } finally {
        if (pendingController === controller) {
          pendingController = null;
        }
        setBusy(false);
      }
    });
  }

  if (copyButton) {
    copyButton.addEventListener("click", async () => {
      // Never copy the mock dots: they stand in for the hidden value and
      // must not leak into the clipboard or be mistaken for the secret.
      if (valueInput && valueInput.value !== "" && !isMockShown()) {
        setBusy(true);
        try {
          await copyText(valueInput.value);
        } finally {
          setBusy(false);
        }
        return;
      }
      if (isAddMode()) {
        setStatus("Enter a value before copying.");
        return;
      }
      const name = currentTargetName();
      if (!name) {
        return;
      }
      abortPending();
      const controller = typeof AbortController !== "undefined" ? new AbortController() : null;
      pendingController = controller;
      setBusy(true);
      setStatus("Loading secret…");
      let secret = "";
      try {
        secret = await fetchSecretValue(name, controller ? controller.signal : undefined);
        if (pendingController !== controller) {
          return;
        }
        if (secret === "") {
          setStatus("This secret is empty.");
          return;
        }
        // Intentionally not assigned to the input or the DOM: copy only.
        await copyText(secret);
      } catch (error) {
        if (error && error.name === "AbortError") {
          return;
        }
        setStatus("Could not copy the secret. Try again.");
      } finally {
        secret = "";
        if (pendingController === controller) {
          pendingController = null;
        }
        setBusy(false);
      }
    });
  }

  if (generateButton) {
    generateButton.disabled = !window.crypto || typeof window.crypto.getRandomValues !== "function" || typeof window.btoa !== "function";
    generateButton.addEventListener("click", () => {
      const secret = generateSecret();
      if (valueInput && secret) {
        valueInput.value = secret;
        showingMock = false;
        revealedFromServer = false;
        if (clearInput) {
          clearInput.checked = false;
        }
        setSecretVisibility(false);
        setStatus("");
        valueInput.focus();
      }
    });
  }

  if (valueInput) {
    valueInput.addEventListener("focus", () => {
      // Clear the mock dots so typing starts from an empty field instead
      // of appending to the placeholder value.
      if (isMockShown()) {
        valueInput.value = "";
        showingMock = false;
      }
    });
    valueInput.addEventListener("blur", () => {
      // Restore the mock dots when the user leaves an untouched empty
      // field in edit mode.
      if (!isAddMode() && editHasValue && valueInput.value === "" && !revealedFromServer && valueInput.type !== "text") {
        showMockValue();
      }
    });
    valueInput.addEventListener("input", () => {
      // Programmatic fills via fetch do not fire input events, so any input
      // event means the value is now a user edit, not a pristine reveal.
      // If the user typed over a selected mock, drop the mock remainder.
      if (showingMock) {
        if (valueInput.value.includes(MOCK_SECRET_VALUE)) {
          valueInput.value = valueInput.value.split(MOCK_SECRET_VALUE).join("");
        }
        showingMock = valueInput.value === MOCK_SECRET_VALUE;
      }
      revealedFromServer = false;
      if (clearInput && valueInput.value !== "" && !isMockShown()) {
        clearInput.checked = false;
      }
    });
  }

  closeButtons.forEach((button) => {
    button.addEventListener("click", closeDialog);
  });

  dialog.addEventListener("click", (event) => {
    if (event.target === dialog) {
      closeDialog();
    }
  });

  dialog.addEventListener("cancel", (event) => {
    event.preventDefault();
    closeDialog();
  });

  dialog.addEventListener("close", cleanup);

  if (form) {
    form.addEventListener("submit", () => {
      abortPending();
      // The mock dots stand in for "keep the existing value" and must
      // never be submitted as the secret itself.
      if (isMockShown()) {
        valueInput.value = "";
      }
      showingMock = false;
      setSecretVisibility(false);
      if (submitButton) {
        submitButton.disabled = true;
        submitButton.textContent = "Saving…";
      }
    });
  }

  if (dialog.hasAttribute("data-secret-edit-open")) {
    const reopenedMode = operationInput && operationInput.value === "add" ? "add" : "edit";
    setMode(reopenedMode);
    if (reopenedMode === "edit") {
      const originalName = originalNameInput ? originalNameInput.value : "";
      const escapedName = originalName && typeof CSS !== "undefined" && typeof CSS.escape === "function"
        ? CSS.escape(originalName)
        : originalName.replace(/["\\]/g, "\\$&");
      const trigger = originalName
        ? document.querySelector(`[data-secret-edit][data-secret-key="${escapedName}"]`)
        : null;
      editHasValue = !trigger || trigger.dataset.secretHasValue !== "false";
      if (editHasValue && valueInput && valueInput.value === "") {
        showMockValue();
      }
    }
    showDialog();
  }
})();

(() => {
  const dialog = document.querySelector("[data-secret-delete-dialog]");
  if (!dialog) {
    return;
  }

  const nameInput = dialog.querySelector("[data-secret-delete-name-input]");
  const nameDisplay = dialog.querySelector("[data-secret-delete-name-display]");
  const cancelButton = dialog.querySelector("[data-secret-delete-cancel]");
  const form = dialog.querySelector("form");
  const submitButton = dialog.querySelector("[data-secret-delete-submit]");
  const closeButtons = dialog.querySelectorAll("[data-secret-delete-close]");
  let returnFocus = null;

  const setName = (name) => {
    if (nameInput) {
      nameInput.value = name;
    }
    if (nameDisplay) {
      nameDisplay.textContent = name;
    }
  };

  const cleanup = () => {
    document.body.classList.remove("dialog-open");
    if (returnFocus && document.body.contains(returnFocus)) {
      returnFocus.setAttribute("aria-expanded", "false");
      returnFocus.focus();
    }
    returnFocus = null;
  };

  const closeDialog = () => {
    if (typeof dialog.close === "function" && dialog.open) {
      dialog.close();
    } else {
      dialog.removeAttribute("open");
      cleanup();
    }
  };

  const showDialog = () => {
    if (typeof dialog.showModal === "function") {
      if (!dialog.open) {
        dialog.showModal();
      }
    } else {
      dialog.setAttribute("open", "");
    }
    document.body.classList.add("dialog-open");
    if (cancelButton) {
      cancelButton.focus();
    }
  };

  const openDialog = (button) => {
    returnFocus = button;
    button.setAttribute("aria-expanded", "true");
    setName(button.dataset.secretKey || "");
    showDialog();
  };

  document.querySelectorAll("[data-secret-delete]").forEach((button) => {
    button.addEventListener("click", () => openDialog(button));
  });

  closeButtons.forEach((button) => {
    button.addEventListener("click", closeDialog);
  });

  dialog.addEventListener("click", (event) => {
    if (event.target === dialog) {
      closeDialog();
    }
  });

  dialog.addEventListener("cancel", (event) => {
    event.preventDefault();
    closeDialog();
  });

  dialog.addEventListener("close", cleanup);

  if (form) {
    form.addEventListener("submit", () => {
      if (submitButton) {
        submitButton.disabled = true;
        submitButton.textContent = "Deleting…";
      }
    });
  }

  if (dialog.hasAttribute("data-secret-delete-open")) {
    setName(nameInput ? nameInput.value : "");
    showDialog();
  }
})();
