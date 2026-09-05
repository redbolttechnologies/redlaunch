(() => {
  document.querySelectorAll('[data-backup-schedule-form]').forEach((form) => {
    const type = form.querySelector('[data-backup-schedule-type]');
    const hour = form.querySelector('[data-backup-schedule-field="hour"]');
    const weekday = form.querySelector('[data-backup-schedule-field="weekday"]');
    const weekdaySelect = weekday ? weekday.querySelector('select') : null;
    const minuteLabel = form.querySelector('[data-backup-minute-label]');

    const updateFields = () => {
      const hourly = type.value === 'hourly';
      const weekly = type.value === 'weekly';
      hour.hidden = hourly;
      weekday.hidden = false;
      weekdaySelect.disabled = !weekly;
      minuteLabel.textContent = hourly ? 'Minute past each hour' : 'Minute';
    };

    type.addEventListener('change', updateFields);
    updateFields();
  });

  const toggle = document.querySelector('[data-backup-schedule-toggle]');
  const scheduleDialog = document.querySelector('[data-backup-schedule-dialog]');
  const disableDialog = document.querySelector('[data-backup-disable-dialog]');
  if (toggle && scheduleDialog && disableDialog) {

    const scheduleForm = scheduleDialog.querySelector('[data-backup-schedule-form]');
    const disableForm = disableDialog.querySelector('[data-backup-disable-form]');
    const scheduleSubmit = scheduleDialog.querySelector('[data-backup-schedule-submit]');
    const disableSubmit = disableDialog.querySelector('[data-backup-disable-submit]');
    let committedEnabled = toggle.checked;
    let pendingEnabled = committedEnabled;
    let activeDialog = null;
    let returnFocus = null;
    let submitting = false;

    const showDialog = (dialog, focusTarget) => {
      activeDialog = dialog;
      if (typeof dialog.showModal === 'function') {
        if (!dialog.open) {
          dialog.showModal();
        }
      } else {
        dialog.setAttribute('open', '');
      }
      document.body.classList.add('dialog-open');
      toggle.setAttribute('aria-expanded', 'true');
      if (focusTarget) {
        focusTarget.focus();
      }
    };

    const cleanup = (dialog) => {
      if (activeDialog !== dialog) {
        return;
      }
      document.body.classList.remove('dialog-open');
      toggle.setAttribute('aria-expanded', 'false');
      if (!submitting) {
        toggle.checked = committedEnabled;
      }
      if (returnFocus && document.body.contains(returnFocus)) {
        returnFocus.focus();
      }
      activeDialog = null;
      returnFocus = null;
      submitting = false;
    };

    const closeDialog = (dialog) => {
      if (activeDialog !== dialog) {
        return;
      }
      if (typeof dialog.close === 'function' && dialog.open) {
        dialog.close();
      } else {
        dialog.removeAttribute('open');
        cleanup(dialog);
      }
    };

    const addDialogBehavior = (dialog, closeSelector) => {
      dialog.querySelectorAll(closeSelector).forEach((button) => {
        button.addEventListener('click', () => closeDialog(dialog));
      });
      dialog.addEventListener('click', (event) => {
        if (event.target === dialog) {
          closeDialog(dialog);
        }
      });
      dialog.addEventListener('cancel', (event) => {
        event.preventDefault();
        closeDialog(dialog);
      });
      dialog.addEventListener('close', () => cleanup(dialog));
    };

    const openScheduleDialog = () => {
      pendingEnabled = true;
      returnFocus = toggle;
      showDialog(scheduleDialog, scheduleDialog.querySelector('[data-backup-schedule-type]'));
    };

    const openDisableDialog = () => {
      pendingEnabled = false;
      returnFocus = toggle;
      showDialog(disableDialog, disableDialog.querySelector('[data-backup-disable-close]'));
    };

    toggle.addEventListener('change', () => {
      if (toggle.checked) {
        openScheduleDialog();
      } else {
        openDisableDialog();
      }
    });

    addDialogBehavior(scheduleDialog, '[data-backup-schedule-close]');
    addDialogBehavior(disableDialog, '[data-backup-disable-close]');

    if (scheduleForm) {
      scheduleForm.addEventListener('submit', () => {
        submitting = true;
        committedEnabled = pendingEnabled;
        if (scheduleSubmit) {
          scheduleSubmit.disabled = true;
          scheduleSubmit.textContent = 'Saving…';
        }
      });
    }
    if (disableForm) {
      disableForm.addEventListener('submit', () => {
        submitting = true;
        committedEnabled = pendingEnabled;
        if (disableSubmit) {
          disableSubmit.disabled = true;
          disableSubmit.textContent = 'Turning off…';
        }
      });
    }
  }

  document.querySelectorAll('[data-backup-restore]').forEach((form) => {
    form.addEventListener('submit', (event) => {
      if (!window.confirm('Restore this backup? The current database will be overwritten.')) {
        event.preventDefault();
      }
    });
  });

  document.querySelectorAll('[data-backup-delete]').forEach((form) => {
    form.addEventListener('submit', (event) => {
      if (!window.confirm('Delete this backup? The SQL file will be permanently removed.')) {
        event.preventDefault();
      }
    });
  });
})();
