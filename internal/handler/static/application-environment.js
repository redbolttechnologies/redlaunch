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
  const operationInput = dialog.querySelector("[name=operation]");
  const originalNameInput = dialog.querySelector("[name=original_name]");
  const title = dialog.querySelector("[data-secret-edit-title]");
  const submitButton = dialog.querySelector("[data-secret-edit-submit]");
  const toggleButton = dialog.querySelector("[data-secret-toggle]");
  const showIcon = dialog.querySelector("[data-secret-show-icon]");
  const hideIcon = dialog.querySelector("[data-secret-hide-icon]");
  const generateButton = dialog.querySelector("[data-secret-generate]");
  const form = dialog.querySelector("form");
  const closeButtons = dialog.querySelectorAll("[data-secret-edit-close]");
  let returnFocus = null;

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
      title.textContent = adding ? "Add secret" : "Edit secret";
    }
    if (submitButton) {
      submitButton.disabled = false;
      submitButton.textContent = "Save";
    }
    setSecretVisibility(false);
  };

  const openEditDialog = (button) => {
    returnFocus = button;
    button.setAttribute("aria-expanded", "true");
    setMode("edit");
    if (originalNameInput) {
      originalNameInput.value = button.dataset.secretKey || "";
    }
    if (nameInput) {
      nameInput.value = button.dataset.secretKey || "";
    }
    if (valueInput) {
      valueInput.value = "";
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
    toggleButton.addEventListener("click", () => {
      setSecretVisibility(valueInput && valueInput.type !== "text");
    });
  }

  if (generateButton) {
    generateButton.disabled = !window.crypto || typeof window.crypto.getRandomValues !== "function" || typeof window.btoa !== "function";
    generateButton.addEventListener("click", () => {
      const secret = generateSecret();
      if (valueInput && secret) {
        valueInput.value = secret;
        setSecretVisibility(false);
        valueInput.focus();
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
      setSecretVisibility(false);
      if (submitButton) {
        submitButton.disabled = true;
        submitButton.textContent = "Saving…";
      }
    });
  }

  if (dialog.hasAttribute("data-secret-edit-open")) {
    setMode(operationInput && operationInput.value === "add" ? "add" : "edit");
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
