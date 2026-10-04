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

  document.querySelectorAll("[data-managed-databases-open]").forEach((button) => {
    button.addEventListener("click", () => {
      openDialog(button);
    });
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

// Enabled overview: reuse the native dialog and existing tab interactions.
(() => {
  document.querySelectorAll("[data-managed-dialog]").forEach((dialog) => {
    let returnFocus = null;
    const closeDialog = () => dialog.close();
    const openDialog = (source) => {
      returnFocus = source;
      if (!dialog.open) dialog.showModal();
      document.body.classList.add("dialog-open");
      const target = dialog.querySelector("input:not([type=hidden]), [data-managed-dialog-close]");
      if (target) target.focus();
    };
    document.querySelectorAll("[data-managed-dialog-open]").forEach((link) => {
      if (link.getAttribute("data-managed-dialog-open") !== dialog.id) return;
      link.addEventListener("click", (event) => {
        event.preventDefault();
        openDialog(link);
      });
      if (link.getAttribute("role") === "switch") {
        link.addEventListener("keydown", (event) => {
          if (event.key === " ") {
            event.preventDefault();
            link.click();
          }
        });
      }
    });
    dialog.querySelectorAll("[data-managed-dialog-close]").forEach((link) => {
      link.addEventListener("click", (event) => {
        event.preventDefault();
        closeDialog();
      });
    });
    dialog.addEventListener("click", (event) => {
      if (event.target === dialog) closeDialog();
    });
    dialog.addEventListener("close", () => {
      document.body.classList.remove("dialog-open");
      // Query-based links provide a working fallback without JavaScript.
      const url = new URL(window.location.href);
      url.searchParams.delete("create");
      url.searchParams.delete("disable");
      window.history.replaceState(null, "", url);
      if (returnFocus) returnFocus.focus();
    });
    dialog.querySelector("form").addEventListener("submit", () => {
      const button = dialog.querySelector("button[type=submit]");
      if (button) {
        button.disabled = true;
        button.textContent = "Working…";
      }
    });
    if (dialog.open) {
      dialog.removeAttribute("open");
      openDialog(document.querySelector(`[data-managed-dialog-open="${dialog.id}"]`));
    }
  });

  document.querySelectorAll("[data-managed-tab-link]").forEach((link) => {
    link.addEventListener("click", (event) => {
      const tab = document.getElementById(link.getAttribute("data-managed-tab-link"));
      if (!tab) return;
      event.preventDefault();
      tab.click();
      tab.focus();
      tab.scrollIntoView({ block: "nearest" });
    });
  });

  document.querySelectorAll("[data-managed-drop]").forEach((form) => {
    form.addEventListener("submit", (event) => {
      const message = form.action.endsWith("/disable")
        ? "Disable managed databases? Applications will lose access. Data and backups are retained."
        : "Remove this managed database or user? This action cannot be undone.";
      if (!window.confirm(message)) event.preventDefault();
    });
  });
})();
