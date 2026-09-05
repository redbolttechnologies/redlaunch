(() => {
  document.querySelectorAll('[data-public-access-form]').forEach((form) => {
    const toggle = form.querySelector('[data-public-access-toggle]');
    const domain = form.querySelector('[data-public-access-domain]');
    if (!toggle || !domain) {
      return;
    }

    const updateRequirement = () => {
      domain.required = toggle.checked;
    };

    toggle.addEventListener('change', updateRequirement);
    updateRequirement();
  });
})();
