// Run with: bun test ./internal/handler/testdata/selfupdate-progress.test.cjs
// Also supported: node --test internal/handler/testdata/selfupdate-progress.test.cjs
const assert = require("node:assert/strict");
const { readFileSync } = require("node:fs");
const { join } = require("node:path");
const { test } = require("node:test");
const { runInNewContext } = require("node:vm");

const source = readFileSync(join(__dirname, "../static/setup.js"), "utf8");

function dialogFixture(state) {
  const error = { hidden: true };
  const modal = { focused: false, busy: null, focus() { this.focused = true; }, setAttribute(name, value) { if (name === "aria-busy") this.busy = value; } };
  const action = { focus() {}, addEventListener() {} };
  const dialog = {
    dataset: {
      state,
      statusUrl: "/settings/update/status?id=job&operation=job",
      progressContentSelector: ".settings-page",
      closeUrl: "/settings",
    },
    querySelector(selector) {
      if (selector === ".setup-progress-dialog") return modal;
      if (selector === "[data-progress-close], [data-setup-close]") return action;
      if (selector === "[data-progress-close], [data-setup-close], .button") return action;
      if (selector === "[data-progress-refresh-error]") return error;
      if (selector === "[data-progress-title]") return { textContent: "Updating Redlaunch" };
      return null;
    },
    addEventListener() {},
  };
  return { dialog, error, modal };
}

function runFixture(fetch) {
  const initial = dialogFixture("handoff");
  const complete = dialogFixture("complete");
  const page = { attributes: {}, setAttribute(name, value) { this.attributes[name] = value; } };
  const sidebar = { attributes: {}, setAttribute(name, value) { this.attributes[name] = value; } };
  const timers = [];
  const document = {
    current: initial.dialog,
    querySelector(selector) {
      if (selector === "[data-progress-dialog]") return this.current;
      if (selector === ".settings-page") return page;
      if (selector === ".sidebar") return sidebar;
      return null;
    },
  };
  Object.defineProperty(initial.dialog, "outerHTML", {
    set(value) {
      assert.match(value, /data-state="complete"/);
      document.current = complete.dialog;
    },
  });
  runInNewContext(source, {
    fetch,
    Promise,
    Error,
    document,
    window: {
      location: { assign() {} },
      setTimeout(callback, delay) {
        timers.push({ callback, delay });
        return timers.length;
      },
    },
  });
  return { document, initial, complete, page, sidebar, timers };
}

const flush = () => new Promise((resolve) => setImmediate(resolve));

test("a restart outage keeps polling and finishes when the replacement reports", async () => {
  let calls = 0;
  const fixture = runFixture(async () => {
    calls += 1;
    if (calls === 1) {
      throw new Error("connection refused");
    }
    return { ok: true, text: async () => '<div data-state="complete"></div>' };
  });

  assert.equal(fixture.page.attributes.inert, "");
  assert.equal(fixture.sidebar.attributes.inert, "");
  assert.equal(fixture.timers.length, 1);
  assert.equal(fixture.timers[0].delay, 150);

  fixture.timers.shift().callback();
  await flush();

  assert.equal(calls, 1);
  assert.equal(fixture.initial.dialog.dataset.state, "handoff");
  assert.equal(fixture.initial.error.hidden, false);
  assert.equal(fixture.timers.length, 1);
  assert.ok(fixture.timers[0].delay > 150, "retry must back off");

  fixture.timers.shift().callback();
  await flush();

  assert.equal(calls, 2);
  assert.equal(fixture.document.current.dataset.state, "complete");
  assert.equal(fixture.timers.length, 0, "completed operations must stop polling");
});

test("a definitive HTTP error stops polling and reports the failure", async () => {
  const fixture = runFixture(async () => ({ ok: false, status: 404 }));

  fixture.timers.shift().callback();
  await flush();

  assert.equal(fixture.initial.dialog.dataset.state, "failed");
  assert.equal(fixture.initial.error.hidden, false);
  assert.equal(fixture.initial.modal.busy, "false");
  assert.equal(fixture.timers.length, 0);
});

test("a gateway error during the restart outage retries", async () => {
  let calls = 0;
  const fixture = runFixture(async () => {
    calls += 1;
    if (calls === 1) {
      return { ok: false, status: 502 };
    }
    return { ok: true, text: async () => '<div data-state="complete"></div>' };
  });

  fixture.timers.shift().callback();
  await flush();

  assert.equal(fixture.initial.dialog.dataset.state, "handoff");
  assert.equal(fixture.timers.length, 1, "gateway errors must retry");

  fixture.timers.shift().callback();
  await flush();

  assert.equal(fixture.document.current.dataset.state, "complete");
  assert.equal(fixture.timers.length, 0);
});
