// Run with: node --test internal/handler/testdata/service-menu.test.cjs
const assert = require("node:assert/strict");
const { readFileSync } = require("node:fs");
const { join } = require("node:path");
const { test } = require("node:test");
const { runInNewContext } = require("node:vm");

const source = readFileSync(join(__dirname, "../static/service-menu.js"), "utf8");

function element() {
  const listeners = new Map();
  const attributes = new Map();
  return {
    value: "", dataset: {}, disabled: false,
    addEventListener(name, callback) { listeners.set(name, callback); },
    fire(name, event = {}) { listeners.get(name)?.(event); },
    setAttribute(name, value) { attributes.set(name, value); },
    removeAttribute(name) { attributes.delete(name); },
    getAttribute(name) { return attributes.get(name); },
    focus() {}, setCustomValidity(message) { this.validationMessage = message; },
    reportValidity() {},
  };
}

function fixture() {
  const trigger = element();
  trigger.setAttribute("aria-controls", "delete-dialog");
  trigger.setAttribute("hx-get", "/applications/7/services/db/delete/volumes");
  trigger.closest = () => null;
  const confirmation = element();
  const submit = element();
  const cancel = element();
  const form = element();
  const checkbox = element();
  checkbox.checked = true;
  const volumes = element();
  volumes.querySelectorAll = () => [checkbox];
  volumes.replaceChildren = (...children) => { volumes.children = children; };
  const dialog = element();
  dialog.dataset.serviceName = "db";
  dialog.showModal = () => { dialog.open = true; };
  dialog.close = () => { dialog.open = false; dialog.fire("close"); };
  const fields = new Map([
    ["form", form], ["fieldset", volumes],
    ["[data-service-delete-confirmation]", confirmation],
    ["[data-service-delete-submit]", submit],
  ]);
  dialog.querySelector = (selector) => fields.get(selector);
  dialog.querySelectorAll = () => [cancel];
  runInNewContext(source, {
    document: {
      querySelectorAll: (selector) => selector === "[data-service-delete-open]" ? [trigger] : [],
      getElementById: () => dialog,
      createElement: element,
      body: { classList: { add() {}, remove() {} } },
    },
  });
  return { trigger, confirmation, submit, form, checkbox, volumes, dialog, cancel };
}

test("requires exact confirmation and waits for volume choices before submitting", () => {
  const f = fixture();
  f.trigger.fire("click");
  assert.equal(f.dialog.open, true);
  assert.equal(f.checkbox.checked, false);
  assert.equal(f.checkbox.disabled, true);
  f.confirmation.value = "db";
  f.confirmation.fire("input");
  assert.equal(f.submit.disabled, true);
  let prevented = false;
  f.form.fire("submit", { preventDefault() { prevented = true; } });
  assert.equal(prevented, true);
  f.trigger.fire("htmx:afterRequest", { detail: { successful: true } });
  assert.equal(f.submit.disabled, false);
  f.confirmation.value = "DB";
  f.confirmation.fire("input");
  assert.equal(f.submit.disabled, true);
  f.cancel.fire("click");
  assert.equal(f.dialog.open, false);
});

test("failed volume loading clears choices and explains saved retry behavior", () => {
  const f = fixture();
  f.trigger.fire("click");
  f.trigger.fire("htmx:afterRequest", { detail: { successful: false } });
  assert.match(f.volumes.children[1].textContent, /retain all volume data/);
  assert.match(f.volumes.children[1].textContent, /original volume selections/);
  f.confirmation.value = "db";
  f.confirmation.fire("input");
  assert.equal(f.submit.disabled, false);
});
