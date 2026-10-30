(() => {
  const bindDialog = ({dialog, openSelector, closeSelector, submitSelector, busyText}) => {
    if (!dialog) return;
    let trigger = null;
    const cleanup = () => {
      document.body.classList.remove("dialog-open");
      if (trigger && trigger.isConnected) {
        trigger.setAttribute("aria-expanded", "false");
        trigger.focus();
      }
    };
    const close = () => {
      if (typeof dialog.close === "function" && dialog.open) {
        dialog.close();
      } else {
        dialog.removeAttribute("open");
        cleanup();
      }
    };
    const open = (opener) => {
      trigger = opener;
      if (trigger) trigger.setAttribute("aria-expanded", "true");
      if (typeof dialog.showModal === "function") {
        if (!dialog.open) dialog.showModal();
      } else {
        dialog.setAttribute("open", "");
      }
      document.body.classList.add("dialog-open");
      const cancel = dialog.querySelector(closeSelector);
      if (cancel) cancel.focus();
    };

    document.querySelectorAll(openSelector).forEach((button) => {
      button.addEventListener("click", (event) => {
        event.preventDefault();
        open(button);
      });
    });
    dialog.querySelectorAll(closeSelector).forEach((button) => {
      button.addEventListener("click", () => close());
    });
    dialog.addEventListener("click", (event) => {
      if (event.target === dialog) close();
    });
    dialog.addEventListener("cancel", (event) => {
      event.preventDefault();
      close();
    });
    dialog.addEventListener("close", cleanup);
    const form = dialog.querySelector("form");
    const submit = dialog.querySelector(submitSelector);
    if (form) {
      form.addEventListener("submit", () => {
        if (submit) {
          submit.disabled = true;
          submit.textContent = busyText;
        }
      });
    }
  };

  bindDialog({
    dialog: document.querySelector("[data-registry-purge-dialog]"),
    openSelector: "[data-registry-purge-open]",
    closeSelector: "[data-registry-purge-close]",
    submitSelector: "[data-registry-purge-submit]",
    busyText: "Purging versions…",
  });
  bindDialog({
    dialog: document.querySelector("[data-registry-gc-dialog]"),
    openSelector: "[data-registry-gc-open]",
    closeSelector: "[data-registry-gc-close]",
    submitSelector: "[data-registry-gc-submit]",
    busyText: "Starting garbage collection…",
  });
})();
