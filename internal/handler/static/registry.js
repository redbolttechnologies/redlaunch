(() => {
  const openButtons = document.querySelectorAll("[data-registry-purge-open]");
  if (!openButtons.length) {
    return;
  }

  const dialogs = new Map();

  const cleanup = (dialog, openButton, returnFocus) => {
    document.body.classList.remove("dialog-open");
    if (openButton) {
      openButton.setAttribute("aria-expanded", "false");
    }
    if (returnFocus && document.body.contains(returnFocus)) {
      returnFocus.focus();
    }
  };

  const closeDialog = (dialog) => {
    const state = dialogs.get(dialog);
    if (typeof dialog.close === "function" && dialog.open) {
      dialog.close();
    } else {
      dialog.removeAttribute("open");
      if (state) {
        cleanup(dialog, state.openButton, state.returnFocus);
        state.returnFocus = null;
      }
    }
  };

  const openDialog = (dialog, openButton) => {
    let state = dialogs.get(dialog);
    if (!state) {
      return;
    }
    state.returnFocus = openButton;
    if (typeof dialog.showModal === "function") {
      if (!dialog.open) {
        dialog.showModal();
      }
    } else {
      dialog.setAttribute("open", "");
    }
    document.body.classList.add("dialog-open");
    if (openButton) {
      openButton.setAttribute("aria-expanded", "true");
    }
    const keepInput = dialog.querySelector("[data-registry-purge-keep]");
    if (keepInput) {
      keepInput.focus();
      keepInput.select();
    }
  };

  document.querySelectorAll("[data-registry-purge-dialog]").forEach((dialog) => {
    const closeButtons = dialog.querySelectorAll("[data-registry-purge-close]");
    const form = dialog.querySelector("form");
    const submitButton = dialog.querySelector("[data-registry-purge-submit]");
    dialogs.set(dialog, {openButton: null, returnFocus: null});

    closeButtons.forEach((button) => {
      button.addEventListener("click", () => closeDialog(dialog));
    });
    dialog.addEventListener("click", (event) => {
      if (event.target === dialog) {
        closeDialog(dialog);
      }
    });
    dialog.addEventListener("cancel", (event) => {
      event.preventDefault();
      closeDialog(dialog);
    });
    dialog.addEventListener("close", () => {
      const state = dialogs.get(dialog);
      if (state) {
        cleanup(dialog, state.openButton, state.returnFocus);
        state.returnFocus = null;
      }
    });
    if (form) {
      form.addEventListener("submit", () => {
        if (submitButton) {
          submitButton.disabled = true;
          submitButton.textContent = "Purging images…";
        }
      });
    }
  });

  openButtons.forEach((openButton) => {
    const dialogID = openButton.getAttribute("aria-controls");
    const dialog = dialogID ? document.getElementById(dialogID) : null;
    if (!dialog || !dialogs.has(dialog)) {
      return;
    }
    dialogs.get(dialog).openButton = openButton;
    openButton.addEventListener("click", (event) => {
      event.preventDefault();
      event.stopPropagation();
      openDialog(dialog, openButton);
    });
  });

  // Reopen the dialog that failed validation so the server-side error stays
  // visible inside the modal.
  const errorDialog = document.querySelector("[data-registry-purge-dialog][data-registry-purge-open-dialog]");
  if (errorDialog && dialogs.has(errorDialog)) {
    const state = dialogs.get(errorDialog);
    if (errorDialog.hasAttribute("open")) {
      errorDialog.removeAttribute("open");
    }
    openDialog(errorDialog, state.openButton);
  }
})();
