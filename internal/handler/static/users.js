(() => {
  document.querySelectorAll('[data-user-dialog]').forEach((dialog) => {
    const action = dialog.dataset.userDialog;
    let opener = null;
    const close = () => dialog.close();
    const show = () => {
      if (dialog.open) dialog.close();
      dialog.showModal();
      document.body.classList.add('dialog-open');
      const first = dialog.querySelector('input:not([type="hidden"]), [data-user-close]');
      if (first) first.focus();
    };
    document.querySelectorAll(`[data-user-action="${action}"]`).forEach((link) => {
      link.addEventListener('click', (event) => {
        event.preventDefault();
        opener = link;
        link.setAttribute('aria-expanded', 'true');
        const form = dialog.querySelector('form');
        form.reset();
        const id = dialog.querySelector('input[name="id"]');
        if (id) id.value = link.dataset.userId || '';
        dialog.querySelectorAll('[data-user-display]').forEach((span) => { span.textContent = link.dataset.userEmail || ''; });
        dialog.querySelectorAll('[role="alert"]').forEach((alert) => { alert.hidden = true; });
        show();
      });
    });
    dialog.querySelectorAll('[data-user-close]').forEach((link) => link.addEventListener('click', (event) => { event.preventDefault(); close(); }));
    dialog.addEventListener('click', (event) => { if (event.target === dialog) close(); });
    dialog.addEventListener('close', () => {
      if (dialog.open) return;
      document.body.classList.remove('dialog-open');
      dialog.querySelectorAll('input[type="password"]').forEach((input) => { input.value = ''; });
      if (opener) { opener.setAttribute('aria-expanded', 'false'); opener.focus(); }
    });
    dialog.querySelector('form').addEventListener('submit', () => {
      const submit = dialog.querySelector('button[type="submit"]');
      submit.disabled = true;
      submit.setAttribute('aria-busy', 'true');
    });
    if (dialog.open) show();
  });
})();
