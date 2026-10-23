(() => {
  const pollingDelay = 1500;
  const refreshDelay = 1500;
  let refreshScheduled = false;

  document.querySelectorAll("[data-toast-notice]").forEach((toast) => {
    const url = new URL(window.location.href);
    url.searchParams.delete("notice");
    window.history.replaceState(window.history.state, "", url);
    const closeButton = toast.querySelector("[data-toast-close]");
    if (closeButton) {
      closeButton.addEventListener("click", () => toast.remove());
    }
  });

  const closeToast = (toast) => {
    const closeURL = (toast && toast.dataset.closeUrl) || window.location.pathname;
    window.location.assign(closeURL);
  };

  const bindToast = (toast) => {
    const closeButton = toast.querySelector("[data-toast-close]");
    if (closeButton) {
      closeButton.addEventListener("click", () => closeToast(toast));
    }
  };

  // Once every toast has finished, refresh automatically so the page shows
  // the new service status without requiring a manual dismiss. Failed toasts
  // are excluded: their error detail must stay visible until dismissed.
  const scheduleRefresh = (toast) => {
    if (refreshScheduled) {
      return;
    }
    if (document.querySelector('[data-toast-job][data-state="running"]')) {
      return;
    }
    // A completed run keeps its output action available until the operator
    // dismisses the toast; refreshing would drop the completed-job query.
    if (toast && toast.dataset.outputAvailable === "true") {
      return;
    }
    refreshScheduled = true;
    const title = toast && toast.querySelector(".toast-title");
    if (title) {
      title.textContent += " — refreshing…";
    }
    const closeURL = (toast && toast.dataset.closeUrl) || window.location.pathname;
    window.setTimeout(() => {
      if (document.querySelector("[data-service-output-dialog][open]")) {
        return;
      }
      window.location.assign(closeURL);
    }, refreshDelay);
  };

  const bindOutputDialog = (dialog) => {
    const container = document.getElementById("service-action-output");
    if (!container) {
      return;
    }
    const closeDialog = () => {
      if (typeof dialog.close === "function" && dialog.open) {
        dialog.close();
      } else {
        dialog.removeAttribute("open");
        container.replaceChildren();
        document.body.classList.remove("dialog-open");
      }
    };
    dialog.querySelectorAll("[data-service-output-close]").forEach((button) => {
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
    dialog.addEventListener("close", () => {
      container.replaceChildren();
      document.body.classList.remove("dialog-open");
    });
    dialog.querySelectorAll("[data-copy-target]").forEach((button) => {
      button.addEventListener("click", async () => {
        const target = document.getElementById(button.dataset.copyTarget || "");
        try {
          if (!target || !window.redlaunchClipboard) {
            throw new Error("copy unavailable");
          }
          await window.redlaunchClipboard.copyText(target.textContent || "");
          window.redlaunchClipboard.showButtonFeedback(button, "Copied");
        } catch {
          if (window.redlaunchClipboard) {
            window.redlaunchClipboard.showButtonFeedback(button, "Copy failed");
          }
        }
      });
    });
    if (typeof dialog.showModal === "function") {
      if (!dialog.open) {
        dialog.showModal();
      }
    } else {
      dialog.setAttribute("open", "");
    }
    document.body.classList.add("dialog-open");
  };

  document.body.addEventListener("htmx:afterSwap", (event) => {
    if (!event.target || event.target.id !== "service-action-output") {
      return;
    }
    const dialog = event.target.querySelector("[data-service-output-dialog]");
    if (dialog) {
      bindOutputDialog(dialog);
    }
  });

  const showPollingError = (toast) => {
    if (!toast) {
      return;
    }
    const error = toast.querySelector("[data-toast-refresh-error]");
    if (error) {
      error.hidden = false;
    }
  };

  const pollToast = async (toast) => {
    if (!toast || toast.dataset.state !== "running") {
      return;
    }
    const statusURL = toast.dataset.statusUrl;
    if (!statusURL) {
      return;
    }
    try {
      const response = await fetch(statusURL, {
        headers: {
          Accept: "text/html",
          "X-Requested-With": "XMLHttpRequest",
        },
        cache: "no-store",
      });
      if (!response.ok) {
        throw new Error("toast refresh failed");
      }
      const html = await response.text();
      const template = document.createElement("template");
      template.innerHTML = html.trim();
      const updated = template.content.querySelector("[data-toast-job]");
      if (!updated) {
        return;
      }
      toast.replaceWith(updated);
      bindToast(updated);
      if (window.htmx && typeof window.htmx.process === "function") {
        window.htmx.process(updated);
      }
      if (updated.dataset.state === "running") {
        window.setTimeout(() => pollToast(updated), pollingDelay);
      } else if (updated.dataset.state === "complete") {
        scheduleRefresh(updated);
      }
    } catch (_) {
      showPollingError(toast);
      window.setTimeout(() => pollToast(toast), pollingDelay * 2);
    }
  };

  const toasts = document.querySelectorAll("[data-toast-job]");
  if (toasts.length === 0) {
    return;
  }
  toasts.forEach((toast) => {
    bindToast(toast);
    if (toast.dataset.state === "running") {
      window.setTimeout(() => pollToast(toast), pollingDelay);
    } else if (toast.dataset.state === "complete") {
      scheduleRefresh(toast);
    }
  });
})();
