(() => {
  const pollingDelay = 1500;

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
      if (updated.dataset.state === "running") {
        window.setTimeout(() => pollToast(updated), pollingDelay);
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
    }
  });
})();
