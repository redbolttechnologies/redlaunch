(() => {
  const dialogs = document.querySelectorAll("[data-environment-import-dialog]");
  const triggers = document.querySelectorAll("[data-environment-import-trigger]");
  if (dialogs.length === 0 || triggers.length === 0) {
    return;
  }

  const maxImportBytes = 1 << 20;

  dialogs.forEach((dialog) => {
    const dialogTriggers = Array.from(triggers).filter((trigger) => (
      trigger.getAttribute("aria-controls") === dialog.id
    ));
    if (dialogTriggers.length === 0) {
      return;
    }

    const form = dialog.querySelector("form");
    const fileInput = dialog.querySelector("[data-environment-import-file]") || dialog.querySelector("input[type=file]");
    const contentInput = dialog.querySelector("[data-environment-import-content]");
    const status = dialog.querySelector("[data-environment-import-status]");
    const submitButton = dialog.querySelector("[data-environment-import-submit]");
    const closeButtons = dialog.querySelectorAll("[data-environment-import-close]");
    let returnFocus = null;
    let backdropMouseDown = false;

    const setStatus = (message) => {
      if (!status) {
        return;
      }
      if (!message) {
        status.textContent = "";
        status.setAttribute("hidden", "");
        return;
      }
      status.textContent = message;
      status.removeAttribute("hidden");
    };

    const readFileAsText = (file) => {
      if (typeof file.text === "function") {
        return file.text();
      }
      return new Promise((resolve, reject) => {
        const reader = new FileReader();
        reader.onload = () => resolve(String(reader.result || ""));
        reader.onerror = () => reject(reader.error || new Error("read failed"));
        reader.readAsText(file);
      });
    };

    const loadFileIntoEditor = async () => {
      if (!fileInput || !fileInput.files || fileInput.files.length === 0) {
        return;
      }
      const file = fileInput.files[0];
      if (file.size > maxImportBytes) {
        setStatus("The file is larger than 1 MB. Choose a smaller file.");
        fileInput.value = "";
        return;
      }
      setStatus(`Reading ${file.name || "file"}…`);
      try {
        const text = await readFileAsText(file);
        if (fileInput.files[0] !== file) {
          return;
        }
        if (text.length > maxImportBytes) {
          setStatus("The file is larger than 1 MB. Choose a smaller file.");
          fileInput.value = "";
          return;
        }
        if (contentInput) {
          contentInput.value = text;
        }
        // Clear the file input so submitting the form only sends the edited
        // textbox content. Nothing is saved until Import is chosen.
        fileInput.value = "";
        setStatus("Loaded the file into the editor. Review and edit before importing.");
        if (contentInput) {
          contentInput.focus();
          const end = contentInput.value.length;
          try {
            contentInput.setSelectionRange(end, end);
          } catch (error) {
            // Selection is a progressive enhancement; ignore when unsupported.
          }
        }
      } catch (error) {
        setStatus("The file could not be read. Check the file and try again.");
        fileInput.value = "";
      }
    };

    const cleanup = () => {
      document.body.classList.remove("dialog-open");
      if (returnFocus && document.body.contains(returnFocus)) {
        returnFocus.setAttribute("aria-expanded", "false");
        returnFocus.focus();
      }
      returnFocus = null;
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
      if (contentInput) {
        contentInput.focus();
      } else if (fileInput) {
        fileInput.focus();
      }
    };

    dialogTriggers.forEach((trigger) => {
      trigger.addEventListener("click", () => {
        returnFocus = trigger;
        trigger.setAttribute("aria-expanded", "true");
        showDialog();
      });
    });

    closeButtons.forEach((button) => {
      button.addEventListener("click", closeDialog);
    });

    dialog.addEventListener("mousedown", (event) => {
      backdropMouseDown = event.target === dialog;
    });

    dialog.addEventListener("click", (event) => {
      if (event.target === dialog && backdropMouseDown) {
        closeDialog();
      }
      backdropMouseDown = false;
    });

    dialog.addEventListener("cancel", (event) => {
      event.preventDefault();
      closeDialog();
    });

    dialog.addEventListener("close", cleanup);

    if (fileInput) {
      fileInput.addEventListener("change", loadFileIntoEditor);
    }

    if (form) {
      form.addEventListener("submit", () => {
        if (submitButton) {
          submitButton.disabled = true;
          submitButton.textContent = "Importing…";
        }
      });
    }

    if (dialog.hasAttribute("data-environment-import-dialog-open")) {
      showDialog();
    }
  });
})();
