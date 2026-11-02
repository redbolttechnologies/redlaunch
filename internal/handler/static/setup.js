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

  const showPollingRetry = (progress) => {
    if (!progress) {
      return;
    }
    const error = progress.querySelector("[data-progress-refresh-error]");
    if (error) {
      error.hidden = false;
    }
  };

  const pollable = (state) => state === "running" || state === "handoff";

  let pollingFailures = 0;
  const pollingMaxDelay = 5000;

  const scheduleRetry = (progress) => {
    if (!progress || !pollable(progress.dataset.state)) {
      focusDialogAction(progress);
      return;
    }
    // The manager restart intentionally interrupts the status endpoint.
    // Keep polling with bounded backoff until the replacement reports its
    // verified state or the user closes the dialog.
    showPollingRetry(progress);
    pollingFailures += 1;
    const delay = Math.min(pollingDelay * 2 ** Math.min(pollingFailures, 3), pollingMaxDelay);
    window.setTimeout(poll, delay);
  };

  const poll = async () => {
    const dialog = document.querySelector(progressSelector);
    if (!dialog || !pollable(dialog.dataset.state)) {
      focusDialogAction(dialog);
      return;
    }

    let response;
    try {
      response = await fetch(dialog.dataset.statusUrl, {
        headers: {
          Accept: "text/html",
          "X-Requested-With": "XMLHttpRequest"
        },
        cache: "no-store"
      });
    } catch (_) {
      scheduleRetry(document.querySelector(progressSelector));
      return;
    }
    if (!response.ok) {
      // Reverse proxies commonly answer with 502/503/504 while the manager
      // container is being recreated. Retry those transient responses, but
      // stop on definitive errors such as a missing operation.
      if (response.status === 502 || response.status === 503 || response.status === 504) {
        scheduleRetry(document.querySelector(progressSelector));
        return;
      }
      showPollingError(document.querySelector(progressSelector));
      return;
    }

    let contents;
    try {
      contents = await response.text();
    } catch (_) {
      scheduleRetry(document.querySelector(progressSelector));
      return;
    }
    dialog.outerHTML = contents;
    pollingFailures = 0;
    const updatedDialog = document.querySelector(progressSelector);
    if (updatedDialog) {
      bindDialogControls(updatedDialog);
    }
    if (updatedDialog && pollable(updatedDialog.dataset.state)) {
      focusProgressDialog(updatedDialog);
      window.setTimeout(poll, pollingDelay);
      return;
    }
    focusDialogAction(updatedDialog);
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

  if (pollable(dialog.dataset.state)) {
    focusProgressDialog(dialog);
    window.setTimeout(poll, 150);
  } else {
    focusDialogAction(dialog);
  }
})();
