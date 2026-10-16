(() => {
  const form = document.querySelector('[data-management-access-form]');
  if (!form) return;
  form.addEventListener('submit', () => {
    const button = form.querySelector('button[type="submit"]');
    if (!button) return;
    button.disabled = true;
    button.setAttribute('aria-busy', 'true');
    button.textContent = 'Saving and queuing restart…';
  });
})();
