(() => {
  const dialog = document.querySelector("[data-compose-import-dialog]");
  const triggers = document.querySelectorAll("[data-compose-import-trigger]");
  if (!dialog || triggers.length === 0) {
    return;
  }

  const form = dialog.querySelector("[data-compose-import-form]");
  const fileInput = dialog.querySelector("[name=compose_file]");
  const submitButton = dialog.querySelector("[data-compose-import-submit]");
  const closeButtons = dialog.querySelectorAll("[data-compose-import-close]");
  const previewContainer = dialog.querySelector("[data-compose-import-preview-container]");
  const previewURL = form ? form.getAttribute("data-compose-import-preview-url") : "";
  const submitLabel = submitButton ? submitButton.textContent : "Import project";
  let returnFocus = null;
  let previewRequest = 0;

  const setSubmitState = (disabled, label) => {
    if (!submitButton) {
      return;
    }
    submitButton.disabled = disabled;
    submitButton.textContent = label || submitLabel;
  };

  const renderPreviewMessage = (message, isError) => {
    if (!previewContainer) {
      return;
    }
    previewContainer.innerHTML = "";
    if (!message) {
      return;
    }
    const alert = document.createElement("div");
    alert.className = "application-alert application-dialog-alert";
    alert.setAttribute("role", isError ? "alert" : "status");
    const text = document.createElement("span");
    text.textContent = message;
    alert.appendChild(text);
    previewContainer.appendChild(alert);
  };

  const updateSubmitForPreview = () => {
    if (!previewContainer || !submitButton) {
      return;
    }
    const preview = previewContainer.querySelector("[data-compose-import-preview]");
    if (!preview) {
      setSubmitState(false, submitLabel);
      return;
    }
    if (preview.querySelector('[role="alert"]')) {
      // The preview already explains a policy rejection; the final import
      // would reject the same file, so keep it disabled until fixed.
      setSubmitState(true, submitLabel);
      return;
    }
    const serviceBoxes = preview.querySelectorAll('input[name="import_service"]');
    if (serviceBoxes.length === 0) {
      setSubmitState(true, submitLabel);
      return;
    }
    const anyServiceChecked = Array.from(serviceBoxes).some((box) => box.checked);
    setSubmitState(!anyServiceChecked, anyServiceChecked ? submitLabel : "Select at least one service");
  };

  const requestPreview = () => {
    if (!form || !fileInput || !previewContainer || !previewURL) {
      return;
    }
    const file = fileInput.files && fileInput.files[0];
    if (!file) {
      previewContainer.innerHTML = "";
      setSubmitState(false, submitLabel);
      return;
    }
    const requestID = ++previewRequest;
    renderPreviewMessage("Reading the Compose file…", false);
    setSubmitState(true, "Reading…");

    const csrfInput = form.querySelector('[name="csrf_token"]');
    const body = new FormData();
    body.append("compose_file", file, file.name);
    if (csrfInput && csrfInput.value) {
      body.append("csrf_token", csrfInput.value);
    }
    fetch(previewURL, { method: "POST", body, credentials: "same-origin" })
      .then((response) => response.text().then((html) => ({ ok: response.ok, html })))
      .then(({ ok, html }) => {
        if (requestID !== previewRequest) {
          return;
        }
        previewContainer.innerHTML = html;
        const preview = previewContainer.querySelector("[data-compose-import-preview]");
        if (preview) {
          preview.querySelectorAll('input[name="import_service"]').forEach((box) => {
            box.addEventListener("change", updateSubmitForPreview);
          });
        }
        if (!ok && !preview) {
          setSubmitState(false, submitLabel);
          return;
        }
        updateSubmitForPreview();
      })
      .catch(() => {
        if (requestID !== previewRequest) {
          return;
        }
        renderPreviewMessage("The Compose file could not be read. Check the file and try again.", true);
        setSubmitState(false, submitLabel);
      });
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
    previewRequest += 1;
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
    if (fileInput) {
      fileInput.focus();
    }
  };

  triggers.forEach((trigger) => {
    trigger.addEventListener("click", () => {
      returnFocus = trigger;
      trigger.setAttribute("aria-expanded", "true");
      showDialog();
    });
  });

  closeButtons.forEach((button) => {
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

  dialog.addEventListener("close", cleanup);

  if (fileInput) {
    fileInput.addEventListener("change", requestPreview);
  }

  if (form) {
    form.addEventListener("submit", (event) => {
      const preview = previewContainer ? previewContainer.querySelector("[data-compose-import-preview]") : null;
      if (preview) {
        const checked = preview.querySelectorAll('input[name="import_service"]:checked');
        if (checked.length === 0) {
          event.preventDefault();
          renderPreviewMessage("Select at least one service to import.", true);
          return;
        }
        if (preview.querySelector('[role="alert"]')) {
          // The preview already explains a policy rejection; stop the
          // import so no files or metadata are touched.
          event.preventDefault();
          return;
        }
      }
      if (submitButton) {
        submitButton.disabled = true;
        submitButton.textContent = "Importing…";
      }
    });
  }

  if (dialog.hasAttribute("data-compose-import-dialog-open")) {
    showDialog();
  }
})();
