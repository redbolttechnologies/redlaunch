(() => {
  const dialog = document.querySelector("[data-compose-import-dialog]");
  const triggers = document.querySelectorAll("[data-compose-import-trigger]");
  if (!dialog || triggers.length === 0) {
    return;
  }

  const form = dialog.querySelector("form");
  const fileInput = dialog.querySelector("[name=compose_file]");
  const submitButton = dialog.querySelector("[data-compose-import-submit]");
  const closeButtons = dialog.querySelectorAll("[data-compose-import-close]");
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
    if (fileInput) {
      fileInput.focus();
    }
  };

  triggers.forEach((trigger) => {
    trigger.addEventListener("click", () => {
      returnFocus = trigger;
      trigger.setAttribute("aria-expanded", "true");
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

  if (form) {
    form.addEventListener("submit", () => {
      if (submitButton) {
        submitButton.disabled = true;
        submitButton.textContent = "Importing…";
      }
    });
  }

  if (dialog.hasAttribute("data-compose-import-dialog-open")) {
    showDialog();
  }
})();
