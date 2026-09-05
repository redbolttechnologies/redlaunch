(() => {
  const controls = [];

  const positionOverlay = (popup, trigger) => {
    const viewportPadding = 8;
    const triggerRect = trigger.getBoundingClientRect();
    const popupWidth = popup.offsetWidth;
    const popupHeight = popup.offsetHeight;
    const maxLeft = Math.max(viewportPadding, window.innerWidth - popupWidth - viewportPadding);
    const maxTop = Math.max(viewportPadding, window.innerHeight - popupHeight - viewportPadding);

    let left = triggerRect.right - popupWidth;
    let top = triggerRect.bottom + viewportPadding;
    if (top + popupHeight > window.innerHeight - viewportPadding) {
      top = triggerRect.top - popupHeight - viewportPadding;
    }

    popup.style.setProperty("--service-actions-menu-left", `${Math.min(Math.max(left, viewportPadding), maxLeft)}px`);
    popup.style.setProperty("--service-actions-menu-top", `${Math.min(Math.max(top, viewportPadding), maxTop)}px`);
  };

  const registerMenu = (container, menu, trigger, overlay = false) => {
    const popup = overlay ? menu.querySelector(".service-actions-options") : null;

    const syncExpandedState = () => {
      trigger.setAttribute("aria-expanded", menu.open ? "true" : "false");
    };

    const resetOverlay = () => {
      if (!popup) {
        return;
      }
      menu.classList.remove("service-actions-menu-overlay");
      popup.style.removeProperty("--service-actions-menu-left");
      popup.style.removeProperty("--service-actions-menu-top");
    };

    const close = () => {
      if (!menu.open) {
        return;
      }
      menu.removeAttribute("open");
      resetOverlay();
      syncExpandedState();
    };

    const reposition = () => {
      if (menu.open && popup) {
        positionOverlay(popup, trigger);
      }
    };

    menu.addEventListener("toggle", () => {
      if (menu.open) {
        controls.forEach((control) => {
          if (control.menu !== menu && control.menu.open) {
            control.close();
          }
        });
        if (popup) {
          menu.classList.add("service-actions-menu-overlay");
          reposition();
        }
      } else {
        resetOverlay();
      }
      syncExpandedState();
    });

    controls.push({close, container, menu, reposition, syncExpandedState, trigger});
    syncExpandedState();
  };

  document.querySelectorAll(".service-split-button").forEach((splitButton) => {
    const menu = splitButton.querySelector(".service-split-menu");
    const mainButton = splitButton.querySelector(".service-split-button-main");
    if (!menu || !mainButton) {
      return;
    }

    mainButton.addEventListener("click", () => {
      menu.open = !menu.open;
    });
    registerMenu(splitButton, menu, mainButton);
  });

  document.querySelectorAll(".service-actions-menu").forEach((actionsMenu) => {
    const trigger = actionsMenu.querySelector(".service-actions-trigger");
    if (!trigger) {
      return;
    }
    registerMenu(actionsMenu, actionsMenu, trigger, true);
  });

  document.querySelectorAll("[data-service-delete-open]").forEach((openButton) => {
    const dialogID = openButton.getAttribute("aria-controls");
    const dialog = dialogID ? document.getElementById(dialogID) : null;
    if (!dialog) {
      return;
    }

    const serviceName = dialog.dataset.serviceName || "";
    const form = dialog.querySelector("form");
    const confirmation = dialog.querySelector("[data-service-delete-confirmation]");
    const submitButton = dialog.querySelector("[data-service-delete-submit]");
    const closeButtons = dialog.querySelectorAll("[data-service-delete-close]");
    let returnFocus = null;

    const syncSubmitState = () => {
      if (!confirmation || !submitButton) {
        return;
      }
      confirmation.setCustomValidity("");
      submitButton.disabled = confirmation.value !== serviceName;
    };

    const cleanup = () => {
      document.body.classList.remove("dialog-open");
      openButton.setAttribute("aria-expanded", "false");
      if (returnFocus) {
        returnFocus.focus();
      }
    };

    const closeDialog = () => {
      if (typeof dialog.close === "function" && dialog.open) {
        dialog.close();
      } else {
        dialog.removeAttribute("open");
        cleanup();
      }
    };

    const openDialog = () => {
      returnFocus = openButton;
      const menu = openButton.closest(".service-actions-menu");
      if (menu) {
        menu.removeAttribute("open");
      }
      if (typeof dialog.showModal === "function") {
        if (!dialog.open) {
          dialog.showModal();
        }
      } else {
        dialog.setAttribute("open", "");
      }
      document.body.classList.add("dialog-open");
      openButton.setAttribute("aria-expanded", "true");
      if (confirmation) {
        confirmation.value = "";
        confirmation.setCustomValidity("");
        confirmation.focus();
      }
      syncSubmitState();
    };

    openButton.addEventListener("click", openDialog);
    closeButtons.forEach((closeButton) => {
      closeButton.addEventListener("click", closeDialog);
    });
    dialog.addEventListener("click", (event) => {
      if (event.target === dialog) {
        closeDialog();
      }
    });
    dialog.addEventListener("cancel", (event) => {
      event.preventDefault();
      closeDialog();
    });
    dialog.addEventListener("close", cleanup);
    if (confirmation) {
      confirmation.addEventListener("input", syncSubmitState);
    }
    if (form) {
      form.addEventListener("submit", (event) => {
        if (!confirmation || confirmation.value !== serviceName) {
          event.preventDefault();
          if (confirmation) {
            confirmation.setCustomValidity("Enter the service name exactly as shown.");
            confirmation.reportValidity();
          }
          return;
        }
        if (submitButton) {
          submitButton.disabled = true;
          submitButton.textContent = "Deleting service…";
        }
      });
    }
    syncSubmitState();
  });

  if (!controls.length) {
    return;
  }

  document.addEventListener("click", (event) => {
    controls.forEach(({close, container, menu}) => {
      if (menu.open && !container.contains(event.target)) {
        close();
      }
    });
  });

  const repositionOverlays = () => {
    controls.forEach(({reposition}) => reposition());
  };
  window.addEventListener("resize", repositionOverlays);
  window.addEventListener("scroll", repositionOverlays, true);

  document.addEventListener("keydown", (event) => {
    if (event.key !== "Escape") {
      return;
    }
    const openControl = controls.find((control) => control.menu.open);
    if (!openControl) {
      return;
    }
    event.preventDefault();
    openControl.close();
    openControl.trigger.focus();
  });
})();
