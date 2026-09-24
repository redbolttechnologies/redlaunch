(() => {
  const dialog = document.querySelector("[data-service-catalog-dialog]");
  const triggers = document.querySelectorAll("[data-service-catalog-trigger]");
  if (!dialog || triggers.length === 0) {
    return;
  }

  const searchInput = dialog.querySelector("[data-service-catalog-search]");
  const filterButtons = Array.from(dialog.querySelectorAll("[data-service-catalog-filter]"));
  const cards = Array.from(dialog.querySelectorAll("[data-service-catalog-card]"));
  const count = dialog.querySelector("[data-service-catalog-count]");
  const empty = dialog.querySelector("[data-service-catalog-empty]");
  const closeButtons = dialog.querySelectorAll("[data-service-catalog-close]");
  let returnFocus = null;
  let activeCategory = "all";

  const applyFilters = () => {
    const query = searchInput ? searchInput.value.trim().toLowerCase() : "";
    let shown = 0;
    cards.forEach((card) => {
      const category = card.getAttribute("data-category") || "";
      const haystack = (card.getAttribute("data-search") || "").toLowerCase();
      const categoryMatch = activeCategory === "all" || category === activeCategory;
      const queryMatch = query === "" || haystack.includes(query);
      const visible = categoryMatch && queryMatch;
      card.hidden = !visible;
      if (visible) {
        shown += 1;
      }
    });
    if (count) {
      if (shown === cards.length) {
        count.textContent = `${cards.length} services available.`;
      } else {
        count.textContent = `${shown} of ${cards.length} services shown.`;
      }
    }
    if (empty) {
      empty.hidden = shown !== 0;
    }
  };

  const setCategory = (category) => {
    activeCategory = category;
    filterButtons.forEach((button) => {
      const selected = button.getAttribute("data-service-catalog-filter") === category;
      button.setAttribute("aria-pressed", selected ? "true" : "false");
    });
    applyFilters();
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
    if (searchInput) {
      searchInput.focus();
    }
  };

  triggers.forEach((trigger) => {
    trigger.addEventListener("click", () => {
      const menu = trigger.closest("details");
      if (menu) {
        menu.removeAttribute("open");
      }
      returnFocus = trigger;
      trigger.setAttribute("aria-expanded", "true");
      if (searchInput) {
        searchInput.value = "";
      }
      setCategory("all");
      showDialog();
    });
  });

  filterButtons.forEach((button) => {
    button.addEventListener("click", () => {
      setCategory(button.getAttribute("data-service-catalog-filter") || "all");
    });
  });

  if (searchInput) {
    searchInput.addEventListener("input", applyFilters);
  }

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

  applyFilters();
})();
