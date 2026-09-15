(() => {
  const dialog = document.querySelector("[data-service-action-error-dialog]");
  if (!dialog) {
    return;
  }

  const closeButtons = dialog.querySelectorAll("[data-service-action-error-close]");

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
    const firstClose = dialog.querySelector("[data-service-action-error-close]");
    if (firstClose) {
      firstClose.focus();
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

  if (dialog.hasAttribute("data-service-action-error-open")) {
    showDialog();
  }
})();
