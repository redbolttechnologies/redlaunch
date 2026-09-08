(() => {
  document.querySelectorAll("[data-github-actions-revoke]").forEach((form) => {
    form.addEventListener("submit", (event) => {
      if (!window.confirm("Revoke this repository's GitHub Actions key? Existing workflows will stop authenticating.")) {
        event.preventDefault();
      }
    });
  });
})();
