(() => {
  const { copyText, showButtonFeedback } = window.redlaunchClipboard;

  const logs = document.querySelector('[data-service-logs]');
  if (!logs) {
    return;
  }

  const lines = Array.from(logs.querySelectorAll('li'));
  const rawLogs = lines.map((line) => line.textContent || '').join('\n');
  const filter = document.querySelector('[data-log-filter]');
  const filterEmpty = document.querySelector('.service-log-filter-empty');
  const filterToggle = document.querySelector('[data-log-filter-toggle]');

  const applyFilter = () => {
    const query = (filter.value || '').trim().toLowerCase();
    let visibleLines = 0;
    lines.forEach((line) => {
      const visible = !query || (line.textContent || '').toLowerCase().includes(query);
      line.hidden = !visible;
      if (visible) {
        visibleLines += 1;
      }
    });
    filterEmpty.hidden = visibleLines > 0;
  };

  filterToggle.addEventListener('click', () => {
    const isHidden = filter.hidden;
    filter.hidden = !isHidden;
    filterToggle.setAttribute('aria-expanded', String(isHidden));
    if (isHidden) {
      filter.focus();
    } else {
      filter.value = '';
      applyFilter();
    }
  });
  filter.addEventListener('input', applyFilter);

  document.querySelector('[data-log-download]').addEventListener('click', () => {
    const link = document.createElement('a');
    link.href = URL.createObjectURL(new Blob([rawLogs], { type: 'text/plain;charset=utf-8' }));
    link.download = 'service-logs.txt';
    link.click();
    window.setTimeout(() => URL.revokeObjectURL(link.href), 0);
  });

  document.querySelector('[data-log-copy]').addEventListener('click', async (event) => {
    const button = event.currentTarget;
    try {
      await copyText(rawLogs);
      showButtonFeedback(button, 'Copied');
    } catch {
      showButtonFeedback(button, 'Copy failed');
    }
  });
})();
