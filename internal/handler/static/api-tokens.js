(() => {
  const dialog = document.querySelector("[data-api-token-create-dialog]");
  if (dialog) {
    const nameInput = dialog.querySelector("#api-token-display-name");
    const applicationInput = dialog.querySelector("#api-token-application");
    const form = dialog.querySelector("form");
    const submitButton = dialog.querySelector("[data-api-token-create-submit]");
    const closeButtons = dialog.querySelectorAll("[data-api-token-create-close]");
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
      if (applicationInput) {
        applicationInput.value = "";
      }
      showDialog();
    };

    document.querySelectorAll("[data-api-token-add]").forEach((button) => {
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
          submitButton.textContent = "Creating…";
        }
      });
    }

    if (dialog.hasAttribute("data-api-token-create-open")) {
      showDialog();
    }
  }
})();

(() => {
  const dialog = document.querySelector("[data-api-token-setup-dialog]");
  if (dialog) {
    const doneButton = dialog.querySelector("[data-api-token-setup-done]");
    const copyButton = dialog.querySelector("[data-copy-target]");
    const closeButtons = dialog.querySelectorAll("[data-api-token-setup-close]");

    const cleanup = () => {
      document.body.classList.remove("dialog-open");
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
      if (copyButton) {
        copyButton.focus();
      } else if (doneButton) {
        doneButton.focus();
      }
    };

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

    if (dialog.hasAttribute("data-api-token-setup-open")) {
      showDialog();
    }
  }
})();

(() => {
  const dialog = document.querySelector("[data-api-token-delete-dialog]");
  if (dialog) {
    const idInput = dialog.querySelector("[data-api-token-delete-id-input]");
    const nameDisplay = dialog.querySelector("[data-api-token-delete-name-display]");
    const cancelButton = dialog.querySelector("[data-api-token-delete-cancel]");
    const form = dialog.querySelector("form");
    const submitButton = dialog.querySelector("[data-api-token-delete-submit]");
    const closeButtons = dialog.querySelectorAll("[data-api-token-delete-close]");
    let returnFocus = null;

    const setToken = (id, name) => {
      if (idInput) {
        idInput.value = id;
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
      setToken(button.dataset.apiTokenId || "", button.dataset.apiTokenName || "");
      showDialog();
    };

    document.querySelectorAll("[data-api-token-revoke]").forEach((button) => {
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
          submitButton.textContent = "Revoking…";
        }
      });
    }

    if (dialog.hasAttribute("data-api-token-delete-open")) {
      setToken(idInput ? idInput.value : "", nameDisplay ? nameDisplay.textContent : "");
      showDialog();
    }
  }
})();
