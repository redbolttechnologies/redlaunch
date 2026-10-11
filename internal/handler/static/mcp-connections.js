(() => {
  const dialog = document.querySelector('[data-mcp-dialog]');
  if (!dialog || typeof dialog.showModal !== 'function') return;
  const form = dialog.querySelector('form');
  const cancel = dialog.querySelector('[data-mcp-close]');
  const submit = form.querySelector('button[type="submit"]');
  let opener = null;
  const show = () => {
    if (dialog.open) dialog.close();
    dialog.showModal();
    document.body.classList.add('dialog-open');
    cancel.focus();
  };
  document.querySelectorAll('[data-mcp-revoke]').forEach((link) => {
    link.addEventListener('click', (event) => {
      event.preventDefault();
      opener = link;
      form.reset();
      submit.disabled = false;
      submit.removeAttribute('aria-busy');
      submit.textContent = 'Revoke connection';
      form.querySelector('input[name="id"]').value = link.dataset.mcpId;
      dialog.querySelector('[data-mcp-display]').textContent = link.dataset.mcpName;
      dialog.querySelector('[data-mcp-resource-display]').textContent = link.dataset.mcpResource;
      dialog.querySelectorAll('[role="alert"]').forEach((alert) => { alert.hidden = true; });
      show();
      link.setAttribute('aria-expanded', 'true');
    });
  });
  cancel.addEventListener('click', (event) => { event.preventDefault(); dialog.close(); });
  dialog.addEventListener('click', (event) => { if (event.target === dialog) dialog.close(); });
  dialog.addEventListener('close', () => {
    if (dialog.open) return;
    document.body.classList.remove('dialog-open');
    if (opener) { opener.setAttribute('aria-expanded', 'false'); opener.focus(); }
  });
  form.addEventListener('submit', () => {
    submit.disabled = true;
    submit.setAttribute('aria-busy', 'true');
    submit.textContent = 'Revoking…';
  });
  if (dialog.open) show();
})();
