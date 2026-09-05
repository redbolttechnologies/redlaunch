(() => {
  const dialog = document.querySelector("[data-domain-edit-dialog]");
  if (!dialog) {
    return;
  }

  const nameInput = dialog.querySelector("#domain-edit-name");
  const form = dialog.querySelector("form");
  const submitButton = dialog.querySelector("[data-domain-edit-submit]");
  const closeButtons = dialog.querySelectorAll("[data-domain-edit-close]");
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

  const openDialog = (button) => {
    returnFocus = button;
    if (button) {
      button.setAttribute("aria-expanded", "true");
    }
    if (nameInput) {
      nameInput.value = "";
    }
    showDialog();
  };

  document.querySelectorAll("[data-domain-add]").forEach((button) => {
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
        submitButton.textContent = "Saving…";
      }
    });
  }

  if (dialog.hasAttribute("data-domain-edit-open")) {
    showDialog();
  }
})();

(() => {
  const dialog = document.querySelector("[data-domain-delete-dialog]");
  if (!dialog) {
    return;
  }

  const nameInput = dialog.querySelector("[data-domain-delete-name-input]");
  const nameDisplay = dialog.querySelector("[data-domain-delete-name-display]");
  const cancelButton = dialog.querySelector("[data-domain-delete-cancel]");
  const form = dialog.querySelector("form");
  const submitButton = dialog.querySelector("[data-domain-delete-submit]");
  const closeButtons = dialog.querySelectorAll("[data-domain-delete-close]");
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
    setName(button.dataset.domainName || "");
    showDialog();
  };

  document.querySelectorAll("[data-domain-delete]").forEach((button) => {
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

  if (dialog.hasAttribute("data-domain-delete-open")) {
    setName(nameInput ? nameInput.value : "");
    showDialog();
  }
})();
