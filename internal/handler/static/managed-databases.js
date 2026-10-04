(() => {
  const toggle = document.querySelector("[data-managed-databases-toggle]");
  const dialog = document.querySelector("[data-managed-databases-dialog]");
  if (!toggle || !dialog) {
    return;
  }

  const form = dialog.querySelector("[data-managed-databases-form]");
  const submitButton = dialog.querySelector("[data-managed-databases-submit]");
  const closeButtons = dialog.querySelectorAll("[data-managed-databases-close]");
  let returnFocus = null;
  let submitting = false;

  const focusTarget = () =>
    dialog.querySelector("#managed-version") || dialog.querySelector("input, select, button");

  const showDialog = () => {
    if (typeof dialog.showModal === "function") {
      if (!dialog.open) {
        dialog.showModal();
      }
    } else {
      dialog.setAttribute("open", "");
    }
    document.body.classList.add("dialog-open");
    toggle.setAttribute("aria-expanded", "true");
    const target = focusTarget();
    if (target) {
      target.focus();
    }
  };

  const cleanup = () => {
    document.body.classList.remove("dialog-open");
    toggle.setAttribute("aria-expanded", "false");
    if (!submitting) {
      toggle.checked = false;
    }
    if (returnFocus && document.body.contains(returnFocus)) {
      returnFocus.focus();
    }
    returnFocus = null;
    submitting = false;
  };

  const closeDialog = () => {
    if (typeof dialog.close === "function" && dialog.open) {
      dialog.close();
    } else {
      dialog.removeAttribute("open");
      cleanup();
    }
  };

  const openDialog = (source) => {
    returnFocus = source || toggle;
    toggle.checked = true;
    showDialog();
  };

  toggle.addEventListener("change", () => {
    if (toggle.checked) {
      openDialog(toggle);
    } else if (dialog.open || dialog.hasAttribute("open")) {
      closeDialog();
    }
  });

  closeButtons.forEach((button) => {
    button.addEventListener("click", (event) => {
      event.preventDefault();
      closeDialog();
    });
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
      submitting = true;
      if (submitButton) {
        submitButton.disabled = true;
        submitButton.textContent = "Enabling…";
      }
    });
  }

  // Reopen the dialog when the server rendered it with a validation error.
  if (dialog.hasAttribute("data-managed-databases-enable-open") || dialog.hasAttribute("open")) {
    if (dialog.hasAttribute("open")) {
      dialog.removeAttribute("open");
    }
    openDialog(toggle);
  }
})();
