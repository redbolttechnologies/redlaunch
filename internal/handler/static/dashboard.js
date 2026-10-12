(() => {
  const dialog = document.getElementById("dashboard-prune-dialog");
  if (!dialog || typeof dialog.showModal !== "function") return;
  let trigger;
  const close = () => {
    dialog.close();
    document.body.classList.remove("dialog-open");
    if (trigger && trigger.isConnected) trigger.focus();
  };
  document.addEventListener("click", (event) => {
    const opener = event.target.closest("[data-dashboard-prune-open]");
    if (opener) {
      event.preventDefault();
      trigger = opener;
      dialog.showModal();
      document.body.classList.add("dialog-open");
    }
    if (event.target.closest("[data-dashboard-prune-close]")) {
      event.preventDefault();
      close();
    }
  });
  dialog.addEventListener("cancel", (event) => { event.preventDefault(); close(); });
  dialog.addEventListener("close", () => document.body.classList.remove("dialog-open"));
  document.addEventListener("htmx:beforeRequest", (event) => {
    if (dialog.open && event.target.id === "dashboard-metrics") event.preventDefault();
  });
  dialog.querySelector("form").addEventListener("submit", () => {
    dialog.querySelector("button[type=submit]").disabled = true;
    dialog.querySelector("[data-dashboard-pruning]").hidden = false;
  });
  if (dialog.hasAttribute("data-prune-confirmation")) {
    dialog.close();
    dialog.showModal();
    document.body.classList.add("dialog-open");
  }
})();
