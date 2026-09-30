(() => {
  const openButton = document.querySelector('[data-postgres-credentials-open]');
  const dialog = document.querySelector('[data-postgres-credentials-dialog]');
  if (!openButton || !dialog) {
    return;
  }

  const form = dialog.querySelector('[data-postgres-credentials-form]');
  const password = dialog.querySelector('[data-postgres-credentials-password]');
  const confirm = dialog.querySelector('[data-postgres-credentials-confirm]');
  const error = dialog.querySelector('[data-postgres-credentials-error]');
  const submit = dialog.querySelector('[data-postgres-credentials-submit]');
  const submitLabel = submit ? submit.textContent : '';
  let returnFocus = null;
  let submitting = false;

  const showError = (message) => {
    if (!error) {
      return;
    }
    error.textContent = message;
    error.hidden = false;
  };

  const clearError = () => {
    if (!error) {
      return;
    }
    error.textContent = '';
    error.hidden = true;
  };

  const showDialog = () => {
    returnFocus = openButton;
    clearError();
    if (typeof dialog.showModal === 'function') {
      if (!dialog.open) {
        dialog.showModal();
      }
    } else {
      dialog.setAttribute('open', '');
    }
    document.body.classList.add('dialog-open');
    const user = dialog.querySelector('#postgres-credentials-user');
    if (user) {
      user.focus();
    }
  };

  const cleanup = () => {
    document.body.classList.remove('dialog-open');
    if (!submitting && returnFocus && document.body.contains(returnFocus)) {
      returnFocus.focus();
    }
    returnFocus = null;
    submitting = false;
  };

  const closeDialog = () => {
    if (typeof dialog.close === 'function' && dialog.open) {
      dialog.close();
    } else {
      dialog.removeAttribute('open');
      cleanup();
    }
  };

  openButton.addEventListener('click', showDialog);

  dialog.querySelectorAll('[data-postgres-credentials-close]').forEach((button) => {
    button.addEventListener('click', closeDialog);
  });
  dialog.addEventListener('click', (event) => {
    if (event.target === dialog) {
      closeDialog();
    }
  });
  dialog.addEventListener('cancel', (event) => {
    event.preventDefault();
    closeDialog();
  });
  dialog.addEventListener('close', cleanup);

  if (form) {
    form.addEventListener('submit', (event) => {
      clearError();
      if (password && confirm && password.value !== confirm.value) {
        event.preventDefault();
        showError('The new passwords do not match.');
        confirm.focus();
        return;
      }
      submitting = true;
      if (submit) {
        submit.disabled = true;
        submit.textContent = 'Updating…';
      }
    });
  }
})();
