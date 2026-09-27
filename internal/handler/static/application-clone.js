(() => {
  const openButton = document.querySelector("[data-application-clone-open]");
  const dialog = document.querySelector("[data-application-clone-dialog]");
  if (!openButton || !dialog) {
    return;
  }

  const form = dialog.querySelector("form");
  const submitButton = dialog.querySelector("[data-application-clone-submit]");
  const closeButtons = dialog.querySelectorAll("[data-application-clone-close]");
  let returnFocus = null;

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

  const showDialog = () => {
    if (typeof dialog.showModal === "function") {
      if (!dialog.open) {
        dialog.showModal();
      }
    } else {
      dialog.setAttribute("open", "");
    }
    document.body.classList.add("dialog-open");
    openButton.setAttribute("aria-expanded", "true");
    const nameInput = dialog.querySelector("#application-clone-name");
    if (nameInput) {
      nameInput.focus();
      nameInput.select();
    }
  };

  const openDialog = () => {
    returnFocus = openButton;
    showDialog();
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
  if (form) {
    form.addEventListener("submit", () => {
      if (submitButton) {
        submitButton.disabled = true;
        submitButton.textContent = "Cloning application…";
      }
    });
  }

  if (dialog.hasAttribute("data-application-clone-open")) {
    returnFocus = openButton;
    showDialog();
  }
})();
