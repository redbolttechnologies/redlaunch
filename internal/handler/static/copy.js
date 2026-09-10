(() => {
  const copyText = async (value) => {
    if (navigator.clipboard && typeof navigator.clipboard.writeText === 'function') {
      await navigator.clipboard.writeText(value);
      return;
    }

    const textarea = document.createElement('textarea');
    textarea.value = value;
    textarea.setAttribute('readonly', '');
    textarea.style.position = 'fixed';
    textarea.style.opacity = '0';
    document.body.appendChild(textarea);
    textarea.select();
    const copied = document.execCommand('copy');
    textarea.remove();
    if (!copied) {
      throw new Error('Copy command was not available');
    }
  };

  const showButtonFeedback = (button, label) => {
    const labelElement = button.querySelector('span');
    if (labelElement) {
      const previousLabel = labelElement.textContent;
      labelElement.textContent = label;
      window.setTimeout(() => {
        labelElement.textContent = previousLabel;
      }, 1600);
      return;
    }

    const previousAriaLabel = button.getAttribute('aria-label');
    const previousTitle = button.getAttribute('title');
    button.setAttribute('aria-label', label);
    button.setAttribute('title', label);
    window.setTimeout(() => {
      if (previousAriaLabel === null) {
        button.removeAttribute('aria-label');
      } else {
        button.setAttribute('aria-label', previousAriaLabel);
      }
      if (previousTitle === null) {
        button.removeAttribute('title');
      } else {
        button.setAttribute('title', previousTitle);
      }
    }, 1600);
  };

  window.redlaunchClipboard = { copyText, showButtonFeedback };

  document.querySelectorAll('[data-copy-value]').forEach((button) => {
    button.addEventListener('click', async () => {
      try {
        await copyText(button.dataset.copyValue || '');
        showButtonFeedback(button, 'Copied');
      } catch {
        showButtonFeedback(button, 'Copy failed');
      }
    });
  });

  document.querySelectorAll('[data-copy-target]').forEach((button) => {
    button.addEventListener('click', async () => {
      const target = document.getElementById(button.dataset.copyTarget || '');
      try {
        if (!target) {
          throw new Error('Copy target was not found');
        }
        await copyText(target.value || target.textContent || '');
        showButtonFeedback(button, 'Copied');
      } catch {
        showButtonFeedback(button, 'Copy failed');
      }
    });
  });
})();
