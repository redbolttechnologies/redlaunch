(() => {
  const dialog = document.querySelector("[data-ssh-key-create-dialog]");
  if (dialog) {
    const nameInput = dialog.querySelector("#ssh-key-display-name");
    const form = dialog.querySelector("form");
    const submitButton = dialog.querySelector("[data-ssh-key-create-submit]");
    const closeButtons = dialog.querySelectorAll("[data-ssh-key-create-close]");
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

    document.querySelectorAll("[data-ssh-key-add]").forEach((button) => {
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

    if (dialog.hasAttribute("data-ssh-key-create-open")) {
      showDialog();
    }
  }
})();

(() => {
  const dialog = document.querySelector("[data-ssh-key-setup-dialog]");
  if (dialog) {
    const doneButton = dialog.querySelector("[data-ssh-key-setup-done]");
    const copyButton = dialog.querySelector("[data-copy-target]");
    const closeButtons = dialog.querySelectorAll("[data-ssh-key-setup-close]");

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

    if (dialog.hasAttribute("data-ssh-key-setup-open")) {
      showDialog();
    }
  }
})();

(() => {
  const dialog = document.querySelector("[data-ssh-key-delete-dialog]");
  if (dialog) {
    const idInput = dialog.querySelector("[data-ssh-key-delete-id-input]");
    const nameDisplay = dialog.querySelector("[data-ssh-key-delete-name-display]");
    const cancelButton = dialog.querySelector("[data-ssh-key-delete-cancel]");
    const form = dialog.querySelector("form");
    const submitButton = dialog.querySelector("[data-ssh-key-delete-submit]");
    const closeButtons = dialog.querySelectorAll("[data-ssh-key-delete-close]");
    let returnFocus = null;

    const setKey = (id, name) => {
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
      setKey(button.dataset.sshKeyId || "", button.dataset.sshKeyName || "");
      showDialog();
    };

    document.querySelectorAll("[data-ssh-key-revoke]").forEach((button) => {
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

    if (dialog.hasAttribute("data-ssh-key-delete-open")) {
      setKey(idInput ? idInput.value : "", nameDisplay ? nameDisplay.textContent : "");
      showDialog();
    }
  }

  document.querySelectorAll("[data-ssh-key-download]").forEach((button) => {
    button.addEventListener("click", () => {
      const sourceID = button.dataset.sshKeySource || "";
      const source = sourceID ? document.getElementById(sourceID) : null;
      if (!source) {
        return;
      }
      const value = source.value || source.textContent || "";
      if (!value) {
        return;
      }
      const filename = button.dataset.sshKeyFilename || "redlaunch-ssh-key.key";
      const blob = new Blob([value.endsWith("\n") ? value : value + "\n"], { type: "text/plain" });
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url;
      link.download = filename;
      document.body.appendChild(link);
      link.click();
      link.remove();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    });
  });
})();
