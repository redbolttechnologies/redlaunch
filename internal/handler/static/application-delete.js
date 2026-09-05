(() => {
  const openButton = document.querySelector("[data-application-delete-open]");
  const dialog = document.querySelector("[data-application-delete-dialog]");
  if (!openButton || !dialog) {
    return;
  }

  const applicationName = dialog.dataset.applicationName || "";
  const form = dialog.querySelector("form");
  const confirmation = dialog.querySelector("[data-application-delete-confirmation]");
  const submitButton = dialog.querySelector("[data-application-delete-submit]");
  const closeButtons = dialog.querySelectorAll("[data-application-delete-close]");
  let returnFocus = null;

  const syncSubmitState = () => {
    if (!confirmation || !submitButton) {
      return;
    }
    confirmation.setCustomValidity("");
    submitButton.disabled = confirmation.value !== applicationName;
  };

  const cleanup = () => {
    document.body.classList.remove("dialog-open");
    openButton.setAttribute("aria-expanded", "false");
    if (returnFocus) {
      returnFocus.focus();
    }
  };

  const closeDialog = () => {
    if (typeof dialog.close === "function" && dialog.open) {
      dialog.close();
    } else {
      dialog.removeAttribute("open");
      cleanup();
    }
  };

  const openDialog = () => {
    returnFocus = openButton;
    if (typeof dialog.showModal === "function") {
      if (!dialog.open) {
        dialog.showModal();
      }
    } else {
      dialog.setAttribute("open", "");
    }
    document.body.classList.add("dialog-open");
    openButton.setAttribute("aria-expanded", "true");
    if (confirmation) {
      confirmation.value = "";
      confirmation.setCustomValidity("");
      confirmation.focus();
    }
    syncSubmitState();
  };

  openButton.addEventListener("click", openDialog);
  closeButtons.forEach((closeButton) => {
    closeButton.addEventListener("click", closeDialog);
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
  if (confirmation) {
    confirmation.addEventListener("input", syncSubmitState);
  }
  if (form) {
    form.addEventListener("submit", (event) => {
      if (!confirmation || confirmation.value !== applicationName) {
        event.preventDefault();
        if (confirmation) {
          confirmation.setCustomValidity("Enter the application name exactly as shown.");
          confirmation.reportValidity();
        }
        return;
      }
      if (submitButton) {
        submitButton.disabled = true;
        submitButton.textContent = "Deleting application…";
      }
    });
  }
  syncSubmitState();
})();
