(() => {
  const pollingDelay = 700;
  const progressSelector = "[data-progress-dialog]";

  const focusDialogAction = (dialog) => {
    const action = dialog && dialog.querySelector("[data-progress-close], [data-setup-close], .button");
    if (action) {
      action.focus();
    }
  };

  const focusProgressDialog = (progress) => {
    const dialog = progress && progress.querySelector(".setup-progress-dialog");
    if (dialog) {
      dialog.focus();
    }
  };

  const closeProgressDialog = (progress) => {
    window.location.assign((progress && progress.dataset.closeUrl) || "/");
  };

  const bindDialogControls = (dialog) => {
    const closeButton = dialog.querySelector("[data-progress-close], [data-setup-close]");
    if (closeButton) {
      closeButton.addEventListener("click", () => closeProgressDialog(dialog));
    }
    dialog.addEventListener("keydown", (event) => {
      if (event.key === "Escape") {
        event.preventDefault();
        closeProgressDialog(dialog);
      }
    });
  };

  const showPollingError = (progress) => {
    if (!progress) {
      return;
    }
    progress.dataset.state = "failed";
    const title = progress.querySelector("[data-progress-title]");
    if (title) {
      title.textContent = "Progress unavailable";
    }
    const error = progress.querySelector("[data-progress-refresh-error]");
    if (error) {
      error.hidden = false;
    }
    const modal = progress.querySelector(".setup-progress-dialog");
    if (modal) {
      modal.setAttribute("aria-busy", "false");
    }
    focusDialogAction(progress);
  };

  const poll = async () => {
    const dialog = document.querySelector(progressSelector);
    if (!dialog || dialog.dataset.state !== "running") {
      focusDialogAction(dialog);
      return;
    }

    try {
      const response = await fetch(dialog.dataset.statusUrl, {
        headers: {
          Accept: "text/html",
          "X-Requested-With": "XMLHttpRequest"
        },
        cache: "no-store"
      });
      if (!response.ok) {
        throw new Error("progress request failed");
      }
      dialog.outerHTML = await response.text();
      const updatedDialog = document.querySelector(progressSelector);
      if (updatedDialog) {
        bindDialogControls(updatedDialog);
      }
      if (updatedDialog && updatedDialog.dataset.state === "running") {
        focusProgressDialog(updatedDialog);
        window.setTimeout(poll, pollingDelay);
        return;
      }
      focusDialogAction(updatedDialog);
    } catch (_) {
      showPollingError(document.querySelector(progressSelector));
    }
  };

  const dialog = document.querySelector(progressSelector);
  if (!dialog) {
    return;
  }

  bindDialogControls(dialog);

  const contentSelector = dialog.dataset.progressContentSelector;
  const progressContent = contentSelector ? document.querySelector(contentSelector) : null;
  if (progressContent) {
    progressContent.setAttribute("inert", "");
  }
  const sidebar = document.querySelector(".sidebar");
  if (sidebar) {
    sidebar.setAttribute("inert", "");
  }

  if (dialog.dataset.state === "running") {
    focusProgressDialog(dialog);
    window.setTimeout(poll, 150);
  } else {
    focusDialogAction(dialog);
  }
})();
