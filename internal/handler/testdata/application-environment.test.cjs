// Run with: bun test ./internal/handler/testdata/application-environment.test.cjs
// Also supported: node --test internal/handler/testdata/application-environment.test.cjs
const assert = require("node:assert/strict");
const { readFileSync } = require("node:fs");
const { join } = require("node:path");
const { test } = require("node:test");
const { runInNewContext } = require("node:vm");

const source = readFileSync(join(__dirname, "../static/application-environment.js"), "utf8");

function element(value = "") {
  const listeners = new Map();
  const attributes = new Map();
  return {
    value, type: "password", dataset: {}, disabled: false,
    addEventListener(name, callback) { listeners.set(name, callback); },
    fire(name) { return listeners.get(name)?.({ target: this }); },
    setAttribute(name, value) { attributes.set(name, value); },
    removeAttribute(name) { attributes.delete(name); },
    hasAttribute(name) { return attributes.has(name); },
    getAttribute(name) { return attributes.get(name); },
    toggleAttribute(name, enabled) {
      if (enabled) attributes.set(name, "");
      else attributes.delete(name);
    },
    focus() {}, select() {},
  };
}

function dialogFixture(crypto = { getRandomValues(bytes) {
  bytes.forEach((_, i) => { bytes[i] = i % 256; });
  return bytes;
} }, fetch) {
  const value = element();
  const length = element("16");
  const generate = element();
  const toggle = element();
  const operation = element("edit");
  const status = element();
  const form = element();
  const add = element();
  const edit = element();
  edit.dataset = { secretKey: "API_TOKEN", secretHasValue: "true" };
  const fields = new Map([
    ["#secret-edit-value", value],
    ["#secret-edit-name", element()],
    ["[data-secret-generate-length]", length],
    ["[data-secret-generate]", generate],
    ["[data-secret-toggle]", toggle],
    ["[name=operation]", operation],
    ["[name=original_name]", element("API_TOKEN")],
    ["[data-secret-status]", status],
    ["form", form],
  ]);
  const dialog = element();
  dialog.querySelector = (selector) => fields.get(selector) || null;
  dialog.querySelectorAll = () => [];
  runInNewContext(source, {
    fetch, AbortController,
    window: { crypto, setTimeout: () => 1, clearTimeout() {} },
    document: {
      querySelector: (selector) => selector === "[data-secret-edit-dialog]" ? dialog : null,
      querySelectorAll: (selector) => selector === "[data-secret-add]" ? [add]
        : selector === "[data-secret-edit]" ? [edit] : [],
      body: { classList: { add() {}, remove() {} }, contains: () => true },
    },
    Math: Object.assign(Object.create(Math), {
      random() { throw new Error("insecure randomness must never be used"); },
    }),
  });
  return { value, length, generate, toggle, status, form, add, edit };
}

test("generates the exact default and selected lengths and keeps the value masked", () => {
  for (const size of [16, 17, 32, 1024]) {
    const fixture = dialogFixture();
    if (size !== 16) fixture.length.value = String(size);
    fixture.generate.fire("click");
    assert.equal(fixture.value.value.length, size);
    assert.match(fixture.value.value, /^[A-Za-z0-9_-]+$/);
    assert.equal(fixture.value.type, "password");
    fixture.form.fire("submit");
    assert.equal(fixture.value.value.length, size);
  }
});

test("maps all 256 random byte values uniformly across the 64-character alphabet", () => {
  const fixture = dialogFixture();
  fixture.length.value = "256";
  fixture.generate.fire("click");
  const counts = new Map();
  for (const character of fixture.value.value) {
    counts.set(character, (counts.get(character) || 0) + 1);
  }
  assert.equal(counts.size, 64);
  assert.ok([...counts.values()].every((count) => count === 4));
});

test("rejects invalid lengths before requesting randomness or replacing a value", () => {
  for (const size of ["", "0", "15", "1025", "16.5", "abc", "Infinity"]) {
    const fixture = dialogFixture({ getRandomValues() {
      assert.fail("invalid lengths must not request randomness");
    } });
    fixture.length.value = size;
    fixture.value.value = "existing-value";
    fixture.generate.fire("click");
    assert.equal(fixture.value.value, "existing-value");
    assert.match(fixture.status.textContent, /whole number from 16 to 1024/);
  }
});

test("disables generation when cryptographic randomness is unavailable", () => {
  for (const crypto of [null, {}]) {
    const fixture = dialogFixture(crypto);
    assert.equal(fixture.generate.disabled, true);
    fixture.value.value = "existing-value";
    fixture.generate.fire("click");
    assert.equal(fixture.value.value, "existing-value");
    assert.match(fixture.status.textContent, /Could not securely generate/);
  }
});

test("a random source failure preserves the value and reports a safe error", () => {
  const fixture = dialogFixture({ getRandomValues() { throw new Error("private details"); } });
  fixture.value.value = "existing-value";
  fixture.generate.fire("click");
  assert.equal(fixture.value.value, "existing-value");
  assert.equal(fixture.status.textContent, "Could not securely generate a secret. Try again.");
});

test("both Add and Edit default to 16 and generated replacements survive submission", () => {
  const fixture = dialogFixture();
  for (const trigger of [fixture.add, fixture.edit]) {
    fixture.length.value = "64";
    trigger.fire("click");
    assert.equal(fixture.length.value, "16");
    fixture.generate.fire("click");
    const generated = fixture.value.value;
    fixture.form.fire("submit");
    assert.equal(fixture.value.value, generated);
    assert.equal(generated.length, 16);
  }
});

test("generation cancels a pending reveal so it cannot overwrite the replacement", async () => {
  let respond;
  let signal;
  const fixture = dialogFixture(undefined, (_, options) => {
    signal = options.signal;
    return new Promise((resolve) => { respond = resolve; });
  });
  fixture.form.setAttribute("action", "/applications/7/secrets");
  fixture.edit.fire("click");
  const reveal = fixture.toggle.fire("click");
  fixture.generate.fire("click");
  const generated = fixture.value.value;
  assert.equal(signal.aborted, true);
  respond({ ok: true, json: async () => ({ value: "old-value" }) });
  await reveal;
  assert.equal(fixture.value.value, generated);
  assert.equal(fixture.value.type, "password");
});
