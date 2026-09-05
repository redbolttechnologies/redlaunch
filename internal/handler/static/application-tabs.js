(() => {
  document.querySelectorAll("[data-application-tabs]").forEach((tabs) => {
    const tabButtons = Array.from(tabs.querySelectorAll("[role=tab][data-application-tab]"));
    if (!tabButtons.length) {
      return;
    }

    const activateTab = (selectedTab, moveFocus) => {
      tabButtons.forEach((tab) => {
        const isSelected = tab === selectedTab;
        const panelID = tab.getAttribute("data-application-tab");
        const panel = panelID ? document.getElementById(panelID) : null;

        tab.setAttribute("aria-selected", isSelected ? "true" : "false");
        tab.tabIndex = isSelected ? 0 : -1;
        if (panel) {
          panel.hidden = !isSelected;
        }
      });

      if (moveFocus) {
        selectedTab.focus();
      }
    };

    tabButtons.forEach((tab, index) => {
      tab.addEventListener("click", () => activateTab(tab, false));
      tab.addEventListener("keydown", (event) => {
        let nextIndex = index;
        if (event.key === "ArrowRight") {
          nextIndex = (index + 1) % tabButtons.length;
        } else if (event.key === "ArrowLeft") {
          nextIndex = (index - 1 + tabButtons.length) % tabButtons.length;
        } else if (event.key === "Home") {
          nextIndex = 0;
        } else if (event.key === "End") {
          nextIndex = tabButtons.length - 1;
        } else {
          return;
        }

        event.preventDefault();
        activateTab(tabButtons[nextIndex], true);
      });
    });

    const requestedTab = new URLSearchParams(window.location.search).get("tab");
    const initialTab = tabButtons.find((tab) => tab.id === `${requestedTab}-tab`);
    if (initialTab) {
      activateTab(initialTab, false);
    }
  });
})();
