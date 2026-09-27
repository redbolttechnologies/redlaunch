(() => {
  // Validation failures return 422 with a swappable title fragment. Other
  // error statuses keep the default behavior (no swap, error event).
  if (window.htmx && htmx.config && Array.isArray(htmx.config.responseHandling)) {
    const swapsUnprocessable = htmx.config.responseHandling.some((rule) => rule.code === "422" && rule.swap);
    if (!swapsUnprocessable) {
      htmx.config.responseHandling = [
        { code: "204", swap: false },
        { code: "[23]..", swap: true },
        { code: "422", swap: true },
        { code: "[45]..", swap: false, error: true },
      ];
    }
  }

  const blockSelector = "[data-application-title-block]";

  const partsOf = (block) => ({
    view: block.querySelector("[data-application-title-view]"),
    form: block.querySelector("[data-application-name-form]"),
    input: block.querySelector("[data-application-name-input]"),
    editButton: block.querySelector("[data-application-name-edit]"),
  });

  const syncDocumentTitle = (block) => {
    const name = block.dataset.applicationName || "";
    if (name) {
      document.title = "Redlaunch · " + name;
    }
  };

  const openEditor = (block) => {
    const { view, form, input } = partsOf(block);
    if (!view || !form || !input) {
      return;
    }
    view.hidden = true;
    form.hidden = false;
    input.value = block.dataset.applicationName || "";
    input.focus();
    input.select();
  };

  const closeEditor = (block, refocus) => {
    const { view, form, input, editButton } = partsOf(block);
    if (!view || !form || !input) {
      return;
    }
    form.hidden = true;
    view.hidden = false;
    input.value = block.dataset.applicationName || "";
    const error = block.querySelector(".application-title-error");
    if (error) {
      error.remove();
    }
    if (refocus && editButton) {
      editButton.focus();
    }
  };

  document.addEventListener("click", (event) => {
    const button = event.target.closest("[data-application-name-edit]");
    if (!button) {
      return;
    }
    const block = button.closest(blockSelector);
    if (block) {
      openEditor(block);
    }
  });

  document.addEventListener("keydown", (event) => {
    if (event.key !== "Escape") {
      return;
    }
    const input = event.target.closest ? event.target.closest("[data-application-name-input]") : null;
    if (!input) {
      return;
    }
    const block = input.closest(blockSelector);
    if (block) {
      event.preventDefault();
      closeEditor(block, true);
    }
  });

  // Blur cancels the edit. A submit in flight sets the flag below so a slow
  // response is never mistaken for a cancel; node removal after a swap is
  // ignored by checking that the block is still in the document.
  document.addEventListener("focusout", (event) => {
    const input = event.target.closest ? event.target.closest("[data-application-name-input]") : null;
    if (!input) {
      return;
    }
    const block = input.closest(blockSelector);
    if (!block || !document.contains(block) || block.dataset.submitting === "true") {
      return;
    }
    closeEditor(block, false);
  });

  document.addEventListener("submit", (event) => {
    const form = event.target.closest ? event.target.closest("[data-application-name-form]") : null;
    if (!form) {
      return;
    }
    const block = form.closest(blockSelector);
    if (block) {
      block.dataset.submitting = "true";
    }
  });

  const clearSubmitting = (event) => {
    const block = event.target.closest ? event.target.closest(blockSelector) : null;
    if (block) {
      delete block.dataset.submitting;
    }
  };

  document.addEventListener("htmx:responseError", clearSubmitting);
  document.addEventListener("htmx:sendError", clearSubmitting);
  document.addEventListener("htmx:swapError", clearSubmitting);

  document.addEventListener("htmx:afterSwap", (event) => {
    const block = event.target.closest ? event.target.closest(blockSelector) : null;
    if (!block || !document.contains(block)) {
      return;
    }
    delete block.dataset.submitting;
    syncDocumentTitle(block);
    const { form, input } = partsOf(block);
    if (form && input && !form.hidden) {
      input.focus();
      input.setSelectionRange(input.value.length, input.value.length);
    }
  });
})();
