(() => {
  const resetRow = (row) => {
    row.querySelectorAll("input").forEach((input) => {
      input.value = input.dataset.defaultValue || "";
    });
    row.querySelectorAll("select").forEach((select) => {
      const defaultOption = select.querySelector("option[selected]");
      select.value = defaultOption ? defaultOption.value : "";
    });
  };

  const addRow = (list, template) => {
    const row = template.content.firstElementChild.cloneNode(true);
    list.appendChild(row);
    const firstControl = row.querySelector("input, select, textarea");
    if (firstControl) {
      firstControl.focus();
    }
  };

  const initializeRepeatableList = (addButton) => {
    const list = document.getElementById(addButton.dataset.repeatableAdd);
    const template = document.getElementById(addButton.dataset.repeatableTemplate);
    if (!list || !template) {
      return;
    }

    addButton.addEventListener("click", () => addRow(list, template));
    list.addEventListener("click", (event) => {
      const removeButton = event.target.closest("[data-repeatable-remove]");
      if (!removeButton) {
        return;
      }
      const row = removeButton.closest("[data-repeatable-row], .repeatable-row");
      if (!row) {
        return;
      }
      if (list.children.length === 1) {
        resetRow(row);
        return;
      }
      row.remove();
    });
  };

  const initialize = () => {
    document.querySelectorAll("[data-repeatable-add]").forEach(initializeRepeatableList);
  };

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", initialize);
  } else {
    initialize();
  }
})();
