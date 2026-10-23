(() => {
  const dialog = document.querySelector("[data-run-parameters-dialog]");
  if (!dialog) {
    return;
  }

  const form = dialog.querySelector("[data-run-parameters-form]");
  const triggers = Array.from(document.querySelectorAll("[data-run-parameters-open]"));
  const closeButtons = dialog.querySelectorAll("[data-run-parameters-close]");
  const submitButton = dialog.querySelector("[data-run-parameters-submit]");
  const serviceNameLabel = dialog.querySelector("[data-run-service-name]");
  const preview = dialog.querySelector("[data-run-preview]");
  const returnTo = dialog.querySelector("[data-run-return-to]");
  const argumentRows = dialog.querySelector("[data-run-argument-rows]");
  const environmentRows = dialog.querySelector("[data-run-environment-rows]");
  const argumentTemplate = dialog.querySelector("[data-run-argument-template]");
  const environmentTemplate = dialog.querySelector("[data-run-environment-template]");
  const argumentAdd = dialog.querySelector("[data-run-argument-add]");
  const environmentAdd = dialog.querySelector("[data-run-environment-add]");
  const applicationID = dialog.dataset.applicationId || "";
  let returnFocus = null;

  const argumentInputs = () => Array.from(argumentRows.querySelectorAll("[data-run-argument-input]"));
  const environmentKeys = () => Array.from(environmentRows.querySelectorAll("[data-run-environment-key]"));
  const environmentValues = () => Array.from(environmentRows.querySelectorAll("[data-run-environment-value]"));

  const quoteArgument = (value) => {
    if (!/\s/.test(value)) {
      return value;
    }
    return JSON.stringify(value);
  };

  const updatePreview = () => {
    const serviceName = dialog.dataset.serviceName || "service";
    const parts = [serviceName];
    argumentInputs().forEach((input) => {
      if (input.value !== "") {
        parts.push(quoteArgument(input.value));
      }
    });
    const environment = environmentKeys().map((key, index) => {
      if (!key.value) {
        return "";
      }
      return `${key.value}=${environmentValues()[index]?.value || ""}`;
    }).filter(Boolean);
    preview.textContent = parts.concat(environment).join(" ");
  };

  const syncRows = () => {
    const args = Array.from(argumentRows.querySelectorAll("[data-run-argument-row]"));
    args.forEach((row, index) => {
      const up = row.querySelector("[data-run-argument-up]");
      const down = row.querySelector("[data-run-argument-down]");
      if (up) {
        up.disabled = index === 0;
      }
      if (down) {
        down.disabled = index === args.length - 1;
      }
    });
    argumentAdd.disabled = args.length >= 64;
    environmentAdd.disabled = environmentRows.querySelectorAll("[data-run-environment-row]").length >= 16;
    updatePreview();
  };

  const bindRow = (row) => {
    row.querySelectorAll("[data-run-argument-up], [data-run-argument-down]").forEach((button) => {
      button.addEventListener("click", () => {
        const current = button.closest("[data-run-argument-row]");
        if (!current) {
          return;
        }
        if (button.hasAttribute("data-run-argument-up") && current.previousElementSibling) {
          current.parentNode.insertBefore(current, current.previousElementSibling);
        } else if (button.hasAttribute("data-run-argument-down") && current.nextElementSibling) {
          current.parentNode.insertBefore(current.nextElementSibling, current);
        }
        syncRows();
        const input = current.querySelector("[data-run-argument-input]");
        if (input) {
          input.focus();
        }
      });
    });
    const remove = row.querySelector("[data-run-argument-remove], [data-run-environment-remove]");
    if (remove) {
      remove.addEventListener("click", () => {
        row.remove();
        syncRows();
        if (argumentRows.querySelectorAll("[data-run-argument-row]").length === 0) {
          addArgumentRow(true);
        }
      });
    }
    row.querySelectorAll("input, select").forEach((field) => {
      field.addEventListener("input", updatePreview);
      field.addEventListener("change", updatePreview);
    });
  };

  const addArgumentRow = (focus = false) => {
    if (argumentRows.querySelectorAll("[data-run-argument-row]").length >= 64) {
      return;
    }
    const row = argumentTemplate.content.firstElementChild.cloneNode(true);
    bindRow(row);
    argumentRows.appendChild(row);
    syncRows();
    if (focus) {
      row.querySelector("[data-run-argument-input]")?.focus();
    }
  };

  const addEnvironmentRow = (focus = false) => {
    if (environmentRows.querySelectorAll("[data-run-environment-row]").length >= 16) {
      return;
    }
    const row = environmentTemplate.content.firstElementChild.cloneNode(true);
    bindRow(row);
    environmentRows.appendChild(row);
    syncRows();
    if (focus) {
      row.querySelector("[data-run-environment-key]")?.focus();
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

  const cleanup = () => {
    document.body.classList.remove("dialog-open");
    if (returnFocus && document.body.contains(returnFocus)) {
      returnFocus.setAttribute("aria-expanded", "false");
      returnFocus.focus();
    }
    returnFocus = null;
  };

  const openDialog = (trigger) => {
    returnFocus = trigger;
    dialog.dataset.serviceName = trigger.dataset.serviceName || "";
    serviceNameLabel.textContent = dialog.dataset.serviceName || "this service";
    form.action = `/applications/${encodeURIComponent(applicationID)}/services/${encodeURIComponent(dialog.dataset.serviceName)}/run-with-parameters`;
    form.reset();
    returnTo.value = trigger.dataset.returnTo || "";
    argumentRows.replaceChildren();
    environmentRows.replaceChildren();
    addArgumentRow(false);
    if (submitButton) {
      submitButton.disabled = false;
      submitButton.textContent = "Run once";
    }
    if (typeof dialog.showModal === "function") {
      if (!dialog.open) {
        dialog.showModal();
      }
    } else {
      dialog.setAttribute("open", "");
    }
    document.body.classList.add("dialog-open");
    trigger.setAttribute("aria-expanded", "true");
    argumentRows.querySelector("[data-run-argument-input]")?.focus();
  };

  triggers.forEach((trigger) => {
    trigger.addEventListener("click", () => {
      const menu = trigger.closest("details");
      if (menu) {
        menu.removeAttribute("open");
      }
      openDialog(trigger);
    });
  });

  [argumentAdd, environmentAdd].forEach((button) => button.addEventListener("click", () => {
    if (button === argumentAdd) {
      addArgumentRow(true);
    } else {
      addEnvironmentRow(true);
    }
  }));

  closeButtons.forEach((button) => button.addEventListener("click", closeDialog));
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
      // Blank rows are form scaffolding, not intentional empty arguments.
      argumentInputs().forEach((input) => {
        if (input.value === "") {
          input.disabled = true;
        }
      });
      environmentKeys().forEach((key, index) => {
        if (!key.value) {
          key.disabled = true;
          const value = environmentValues()[index];
          if (value) {
            value.disabled = true;
          }
        }
      });
      if (submitButton) {
        submitButton.disabled = true;
        submitButton.textContent = "Running…";
      }
    });
  }

  argumentRows.querySelectorAll("[data-run-argument-row]").forEach(bindRow);
  syncRows();
})();
