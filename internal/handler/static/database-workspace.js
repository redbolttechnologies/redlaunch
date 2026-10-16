(() => {
  const workspace = document.querySelector("[data-database-workspace]");
  if (!workspace) return;
  const editor = workspace.querySelector("#database-sql");
  const queryForm = workspace.querySelector("[data-query-form]");
  const menus = Array.from(workspace.querySelectorAll("[data-table-menu]"));
  const highlight = workspace.querySelector("[data-sql-highlight]");
  const lineNumbers = workspace.querySelector("[data-line-numbers]");
  // Tokenization is presentation-only. Never turn SQL into HTML or interpolate
  // user text into markup; highlighted fragments are built with textContent.
  const sqlTokens = /--[^\n]*|\/\*[\s\S]*?\*\/|\$([A-Za-z_][A-Za-z_0-9]*|)\$[\s\S]*?\$\1\$|\bE'(?:''|\\[\s\S]|[^'\\])*'|'(?:''|[^'])*'|"(?:""|[^"])*"|\b\d+(?:\.\d+)?\b|\b[A-Za-z_][A-Za-z_0-9]*\b|[^A-Za-z_0-9'"$-]+|[\s\S]/g;
  const keywords = new Set("SELECT FROM WHERE JOIN LEFT RIGHT INNER OUTER ON AS INSERT INTO UPDATE DELETE CREATE ALTER DROP TABLE SCHEMA SET VALUES RETURNING ORDER GROUP BY HAVING LIMIT OFFSET WITH UNION ALL DISTINCT AND OR NOT NULL IS IN CASE WHEN THEN ELSE END ASC DESC BEGIN COMMIT ROLLBACK".split(" "));

  function tokens() {
    return Array.from(editor.value.matchAll(sqlTokens), (match) => match[0]);
  }
  function syncScroll() {
    highlight.scrollTop = editor.scrollTop;
    highlight.scrollLeft = editor.scrollLeft;
    lineNumbers.scrollTop = editor.scrollTop;
  }
  function renderSQL() {
    const fragment = document.createDocumentFragment();
    tokens().forEach((text) => {
      let kind = "";
      if (text.startsWith("--") || text.startsWith("/*")) kind = "comment";
      else if (/^(?:E?'|"|\$)/i.test(text)) kind = "string";
      else if (/^\d/.test(text)) kind = "number";
      else if (keywords.has(text.toUpperCase())) kind = "keyword";
      if (kind) {
        const span = document.createElement("span");
        span.className = `database-sql-token-${kind}`;
        span.textContent = text;
        fragment.append(span);
      } else fragment.append(document.createTextNode(text));
    });
    fragment.append(document.createTextNode("\n"));
    highlight.replaceChildren(fragment);
    lineNumbers.textContent = editor.value.split("\n").map((_, index) => index + 1).join("\n");
    workspace.querySelector("[data-code-surface]").classList.add("database-syntax-ready");
    syncScroll();
  }
  function formatSQL() {
    // Insert whitespace only between tokens. Preserve quoted values, identifiers,
    // dollar-quoted function bodies and comments exactly as entered.
    const clauses = new Set("SELECT FROM WHERE GROUP ORDER HAVING LIMIT RETURNING SET VALUES".split(" "));
    let formatted = "";
    tokens().forEach((text) => {
      if (clauses.has(text.toUpperCase()) && formatted.trim() && !formatted.endsWith("\n")) {
        formatted = formatted.replace(/[ \t]+$/, "") + "\n";
      }
      formatted += text;
    });
    editor.value = formatted.trim();
    renderSQL();
    editor.focus();
    workspace.querySelector(".database-run-menu").open = false;
  }
  editor.addEventListener("input", renderSQL);
  editor.addEventListener("scroll", syncScroll);
  new ResizeObserver(() => { lineNumbers.style.height = `${editor.clientHeight}px`; }).observe(editor);
  renderSQL();
  workspace.querySelector("[data-format-sql]").addEventListener("click", formatSQL);
  workspace.querySelector("[data-clear-sql]").addEventListener("click", () => {
    editor.value = "";
    renderSQL();
    editor.focus();
    workspace.querySelector(".database-run-menu").open = false;
  });

  function closeMenus(except) {
    menus.forEach((menu) => {
      if (menu !== except) menu.querySelector("[data-table-actions]").open = false;
    });
  }

  function selectTable(table) {
    menus.forEach((item) => item.classList.toggle("database-table-selected", item === table));
  }

  menus.forEach((menu) => {
    const trigger = menu.querySelector("[data-table-name]");
    const form = menu.querySelector("[data-table-context-menu]");
    const button = form.querySelector("button");
    const actions = menu.querySelector("[data-table-actions]");
    function openMenu(event) {
      event.preventDefault();
      closeMenus(menu);
      actions.open = true;
      selectTable(menu);
      button.focus();
    }
    trigger.addEventListener("contextmenu", openMenu);
    trigger.addEventListener("click", () => {
      closeMenus();
      selectTable(menu);
    });
    trigger.addEventListener("keydown", (event) => {
      if (event.key === "ContextMenu" || (event.shiftKey && event.key === "F10")) openMenu(event);
    });
    actions.addEventListener("toggle", () => {
      if (actions.open) closeMenus(menu);
    });
    form.addEventListener("submit", (event) => {
      event.preventDefault();
      if (queryForm.querySelector('button[type="submit"]').disabled) return;
      editor.value = trigger.dataset.sql;
      renderSQL();
      selectTable(menu);
      closeMenus();
      editor.focus();
      // Scripts are generated by the service; only prepared SELECTs auto-run.
      if (/^\s*SELECT\b/i.test(editor.value)) queryForm.requestSubmit();
    });
  });
  document.addEventListener("click", (event) => {
    closeMenus(event.target.closest("[data-table-menu]"));
    document.querySelectorAll(".database-run-menu, .database-download-menu").forEach((menu) => {
      if (!menu.contains(event.target)) menu.open = false;
    });
  });
  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape") {
      const open = menus.find((menu) => menu.querySelector("[data-table-actions]").open);
      if (open) {
        closeMenus();
        open.querySelector("summary").focus();
      }
      document.querySelectorAll(".database-run-menu[open], .database-download-menu[open]").forEach((menu) => {
        menu.open = false;
        menu.querySelector("summary").focus();
      });
    }
  });
  editor.addEventListener("keydown", (event) => {
    if (event.key === "Enter" && (event.ctrlKey || event.metaKey)) {
      event.preventDefault();
      if (!queryForm.querySelector('button[type="submit"]').disabled) queryForm.requestSubmit();
    }
    if (event.key.toLowerCase() === "k" && (event.ctrlKey || event.metaKey)) {
      event.preventDefault();
      formatSQL();
    }
  });

  const schemas = Array.from(workspace.querySelectorAll("[data-schema]"));
  const schemaState = new Map();
  workspace.querySelector("[data-table-search]").addEventListener("input", (event) => {
    const search = event.target.value.trim().toLowerCase();
    let visible = 0;
    schemas.forEach((schema) => {
      if (search && !schemaState.has(schema)) schemaState.set(schema, schema.open);
      const schemaMatches = schema.dataset.schemaName.toLowerCase().includes(search);
      const rows = Array.from(schema.querySelectorAll("[data-table-row]"));
      let matches = 0;
      rows.forEach((row) => {
        row.hidden = search !== "" && !schemaMatches && !row.dataset.tableSearchName.toLowerCase().includes(search);
        if (!row.hidden) matches++;
      });
      schema.hidden = search !== "" && !schemaMatches && matches === 0;
      if (!schema.hidden) visible++;
      if (search) schema.open = true;
      else if (schemaState.has(schema)) { schema.open = schemaState.get(schema); schemaState.delete(schema); }
    });
    workspace.querySelector("[data-table-search-empty]").hidden = visible !== 0;
    closeMenus();
  });

  function initializeResults() {
    const results = document.querySelector("[data-query-results]");
    if (!results || results.dataset.initialized) return;
    results.dataset.initialized = "true";
    const table = results.querySelector("[data-result-table]");
    if (!table) return;
    const rows = Array.from(table.querySelectorAll("[data-result-row]"));
    const columns = Array.from(table.querySelectorAll("thead th"), (cell) => cell.textContent);
    const data = rows.map((row) => Array.from(row.querySelectorAll("[data-result-cell]"), (cell) => cell.dataset.resultCell));
    const pagination = results.querySelector("[data-result-pagination]");
    const pageSize = 25;
    const pageCount = Math.max(1, Math.ceil(rows.length / pageSize));
    let page = 0;
    function showPage(next) {
      page = Math.max(0, Math.min(next, pageCount - 1));
      rows.forEach((row, index) => { row.hidden = index < page * pageSize || index >= (page + 1) * pageSize; });
      results.querySelector("[data-result-page-summary]").textContent = `Showing ${rows.length ? page * pageSize + 1 : 0}–${Math.min((page + 1) * pageSize, rows.length)} of ${rows.length} rows`;
      results.querySelector("[data-result-prev]").disabled = page === 0;
      results.querySelector("[data-result-next]").disabled = page === pageCount - 1;
      const pages = document.createDocumentFragment();
      const indexes = new Set([0, page - 1, page, page + 1, pageCount - 1]);
      Array.from(indexes).filter((index) => index >= 0 && index < pageCount).sort((a, b) => a - b).forEach((index) => {
        const button = document.createElement("button");
        button.type = "button";
        button.className = "icon-button";
        button.textContent = index + 1;
        button.setAttribute("aria-label", `Result page ${index + 1}`);
        if (index === page) button.setAttribute("aria-current", "page");
        button.addEventListener("click", () => {
          showPage(index);
          results.querySelector('[aria-current="page"]').focus();
        });
        pages.append(button);
      });
      results.querySelector("[data-result-pages]").replaceChildren(pages);
    }
    pagination.hidden = false;
    results.querySelector("[data-result-prev]").addEventListener("click", () => showPage(page - 1));
    results.querySelector("[data-result-next]").addEventListener("click", () => showPage(page + 1));
    showPage(0);
    const download = results.querySelector("[data-result-download]");
    download.hidden = false;
    download.querySelectorAll("[data-download-format]").forEach((button) => button.addEventListener("click", () => {
      const format = button.dataset.downloadFormat;
      // Spreadsheet formula protection; JSON retains the exact displayed values.
      const csvCell = (value) => {
        const safe = /^[\t\r\n]|^\s*[=+\-@]/.test(value) && !/^[+\-]?\d+(?:\.\d+)?(?:e[+\-]?\d+)?$/i.test(value) ? `'${value}` : value;
        return `"${safe.replaceAll('"', '""')}"`;
      };
      const contents = format === "csv" ? [columns, ...data].map((row) => row.map(csvCell).join(",")).join("\r\n") : JSON.stringify({columns, rows: data}, null, 2);
      const blob = new Blob([contents], {type: format === "csv" ? "text/csv;charset=utf-8" : "application/json"});
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url;
      link.download = `query-results.${format}`;
      link.click();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
      download.open = false;
    }));
  }
  initializeResults();
  document.addEventListener("htmx:afterSwap", initializeResults);

  const importDialog = document.querySelector("[data-dump-import-dialog]");
  function openImport() {
    importDialog.removeAttribute("open");
    importDialog.showModal();
  }
  document.querySelector("[data-dump-import-open]").addEventListener("click", (event) => { event.preventDefault(); openImport(); });
  document.querySelector("[data-dump-import-close]").addEventListener("click", (event) => { event.preventDefault(); importDialog.close(); });
  if (importDialog.open) openImport();
  document.querySelector("[data-dump-import-form]").addEventListener("submit", (event) => {
    const file = document.querySelector("#database-dump-file").files[0];
    if (!file || file.size === 0 || file.size > 32 * 1024 * 1024) {
      event.preventDefault();
      document.querySelector("#database-dump-file").setCustomValidity("Choose a non-empty dump file up to 32 MiB.");
      document.querySelector("#database-dump-file").reportValidity();
      return;
    }
    document.querySelector("[data-dump-import-submit]").disabled = true;
    document.querySelector("[data-dump-import-progress]").hidden = false;
  });
  document.querySelector("#database-dump-file").addEventListener("change", (event) => event.target.setCustomValidity(""));
  queryForm.addEventListener("htmx:afterRequest", (event) => {
    queryForm.removeAttribute("aria-busy");
    if (!event.detail.successful) {
      const result = document.querySelector("#database-query-result .service-widget-body");
      const alert = document.createElement("div");
      alert.className = "application-alert";
      alert.setAttribute("role", "alert");
      alert.textContent = "The query request failed. Check your connection or reload the page before retrying. Write operations may already have completed.";
      result.replaceChildren(alert);
    }
  });
  queryForm.addEventListener("htmx:beforeRequest", () => queryForm.setAttribute("aria-busy", "true"));
})();
