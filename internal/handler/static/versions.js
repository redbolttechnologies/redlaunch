(() => {
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
    const state = dialogs.get(dialog);
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
    const focusTarget = dialog.querySelector("select, [data-versions-source]") || dialog.querySelector("button");
    if (focusTarget) {
      focusTarget.focus();
    }
  };

  document.querySelectorAll("[data-versions-dialog]").forEach((dialog) => {
    dialogs.set(dialog, {openButton: null, returnFocus: null});
    dialog.querySelectorAll("[data-versions-close]").forEach((button) => {
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
    const form = dialog.querySelector("form");
    const submitButton = dialog.querySelector("[data-versions-submit]");
    if (form) {
      form.addEventListener("submit", () => {
        if (submitButton) {
          submitButton.disabled = true;
          submitButton.textContent = "Deploying…";
        }
      });
    }
  });

  document.querySelectorAll("[data-versions-open]").forEach((openButton) => {
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

  // The application-wide dialog previews which services the selected tag
  // applies to and which are skipped, from server-rendered option data.
  const source = document.querySelector("[data-versions-source]");
  const preview = document.querySelector("[data-versions-preview]");
  if (source && preview) {
    const renderPreview = () => {
      const option = source.options[source.selectedIndex];
      if (!option) {
        preview.textContent = "";
        return;
      }
      const applies = option.getAttribute("data-versions-services") || "";
      const skips = option.getAttribute("data-versions-missing") || "";
      const parts = [];
      if (applies) {
        parts.push("Deploys to: " + applies + ".");
      } else {
        parts.push("No service has this tag in its repository.");
      }
      if (skips) {
        parts.push("Skipped: " + skips + ".");
      }
      preview.textContent = parts.join(" ");
    };
    source.addEventListener("change", renderPreview);
    renderPreview();
  }
})();
