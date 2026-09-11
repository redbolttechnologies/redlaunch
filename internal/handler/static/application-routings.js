(() => {
  const dialog = document.querySelector("[data-routing-edit-dialog]");
  if (!dialog) {
    return;
  }

  const title = dialog.querySelector("[data-routing-edit-title]");
  const operationInput = dialog.querySelector("[data-routing-edit-operation]");
  const routingIDInput = dialog.querySelector("[data-routing-edit-id]");
  const subdomainInput = dialog.querySelector("#routing-edit-subdomain");
  const pathInput = dialog.querySelector("#routing-edit-path");
  const serviceInput = dialog.querySelector("#routing-edit-service");
  const servicePortInput = dialog.querySelector("#routing-edit-service-port");
  const servicePathInput = dialog.querySelector("#routing-edit-service-path");
  const submitButton = dialog.querySelector("[data-routing-edit-submit]");
  const closeButtons = dialog.querySelectorAll("[data-routing-edit-close]");
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
    if (pathInput) {
      pathInput.focus();
      pathInput.select();
    }
  };

  const setMode = (mode) => {
    const adding = mode === "add";
    if (operationInput) {
      operationInput.value = adding ? "add" : "edit";
    }
    if (title) {
      title.textContent = adding ? "Add routing" : "Edit routing";
    }
    if (submitButton) {
      submitButton.disabled = false;
      submitButton.textContent = "Save";
    }
  };

  const openAddDialog = (button) => {
    returnFocus = button;
    button.setAttribute("aria-expanded", "true");
    setMode("add");
    if (routingIDInput) {
      routingIDInput.value = "";
    }
    if (subdomainInput) {
      subdomainInput.value = "";
    }
    if (pathInput) {
      pathInput.value = "/";
    }
    if (serviceInput) {
      serviceInput.value = "";
    }
    if (servicePortInput) {
      servicePortInput.value = "80";
    }
    if (servicePathInput) {
      servicePathInput.value = "/";
    }
    showDialog();
  };

  const openEditDialog = (button) => {
    returnFocus = button;
    button.setAttribute("aria-expanded", "true");
    setMode("edit");
    if (routingIDInput) {
      routingIDInput.value = button.dataset.routingId || "";
    }
    if (subdomainInput) {
      subdomainInput.value = button.dataset.routingSubdomain || "";
    }
    if (pathInput) {
      pathInput.value = button.dataset.routingPath || "";
    }
    if (serviceInput) {
      serviceInput.value = button.dataset.routingService || "";
    }
    if (servicePortInput) {
      servicePortInput.value = button.dataset.routingServicePort || "80";
    }
    if (servicePathInput) {
      servicePathInput.value = button.dataset.routingServicePath || "";
    }
    showDialog();
  };

  document.querySelectorAll("[data-routing-add]").forEach((button) => {
    button.addEventListener("click", () => openAddDialog(button));
  });

  document.querySelectorAll("[data-routing-edit]").forEach((button) => {
    button.addEventListener("click", () => openEditDialog(button));
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

  const form = dialog.querySelector("form");
  if (form) {
    form.addEventListener("submit", () => {
      if (submitButton) {
        submitButton.disabled = true;
        submitButton.textContent = "Saving…";
      }
    });
  }

  if (dialog.hasAttribute("data-routing-edit-open")) {
    setMode(operationInput && operationInput.value === "edit" ? "edit" : "add");
    showDialog();
  }
})();

(() => {
  const dialog = document.querySelector("[data-routing-delete-dialog]");
  if (!dialog) {
    return;
  }

  const routingIDInput = dialog.querySelector("[data-routing-delete-id]");
  const hostInput = dialog.querySelector("[data-routing-delete-host-input]");
  const hostDisplay = dialog.querySelector("[data-routing-delete-host-display]");
  const submitButton = dialog.querySelector("[data-routing-delete-submit]");
  const closeButtons = dialog.querySelectorAll("[data-routing-delete-close]");
  let returnFocus = null;

  const setRouting = (id, host) => {
    if (routingIDInput) {
      routingIDInput.value = id;
    }
    if (hostInput) {
      hostInput.value = host;
    }
    if (hostDisplay) {
      hostDisplay.textContent = host;
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
    if (submitButton) {
      submitButton.focus();
    }
  };

  document.querySelectorAll("[data-routing-delete]").forEach((button) => {
    button.addEventListener("click", () => {
      returnFocus = button;
      button.setAttribute("aria-expanded", "true");
      setRouting(button.dataset.routingId || "", button.dataset.routingHost || "");
      showDialog();
    });
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

  const form = dialog.querySelector("form");
  if (form) {
    form.addEventListener("submit", () => {
      if (submitButton) {
        submitButton.disabled = true;
        submitButton.textContent = "Deleting…";
      }
    });
  }

  if (dialog.hasAttribute("data-routing-delete-open")) {
    setRouting(routingIDInput ? routingIDInput.value : "", hostInput ? hostInput.value : "");
    showDialog();
  }
})();
