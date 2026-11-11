"use strict";
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const test = require("node:test");
const source = fs.readFileSync(
  path.join(__dirname, "../../internal/httpapi/web/usage.js"),
  "utf8",
);

function screen(fetcher) {
  const nodes = new Map(),
    timers = [],
    observers = [];
  const get = (id) => {
    if (!nodes.has(id))
      nodes.set(id, {
        textContent: "",
        events: {},
        classList: { toggle() {} },
        addEventListener(type, fn) {
          this.events[type] = fn;
        },
      });
    return nodes.get(id);
  };
  const document = {
    hidden: false,
    getElementById: get,
    addEventListener() {},
  };
  vm.runInNewContext(source, {
    document,
    Intl,
    Number,
    Math,
    Error,
    AbortSignal,
    fetch: fetcher,
    MutationObserver: class {
      constructor(fn) {
        observers.push(fn);
      }
      observe() {}
    },
    setInterval: (fn) => timers.push(fn),
  });
  return { get, timers, observers, document };
}
const snapshot = {
  day_utc: "2026-11-12",
  limit_rub: "100.000000000",
  estimated_rub: "0.000010100",
  reserved_rub: "5.000000000",
  remaining_rub: "94.999989900",
  calls: 2,
  recorded_calls: 1,
  unknown_calls: 0,
  pending_calls: 1,
  input_tokens: 0,
  output_tokens: 0,
  embedding_tokens: 1,
  reservation_overruns: 0,
};
async function done() {
  await new Promise(setImmediate);
}
test("usage displays small costs, reserves and unknown requests", async () => {
  const ui = screen(async () => ({ ok: true, json: async () => snapshot }));
  await done();
  assert.match(ui.get("usage-estimated").textContent, /0,00001/);
  assert.equal(ui.get("usage-calls").textContent, "2");
  assert.equal(ui.get("usage-tokens").textContent, "1");
  assert.equal(ui.get("usage-progress").max, 100);
  assert.ok(ui.get("usage-progress").value > 5);
  assert.match(
    ui.get("usage-note").textContent,
    /без окончательной стоимости: 1/,
  );
});
test("accounting outage stays visible and can recover", async () => {
  let online = false;
  const ui = screen(async () => ({
    ok: online,
    json: async () => ({ ...snapshot, pending_calls: 0 }),
  }));
  await done();
  assert.match(ui.get("usage-note").textContent, /недоступен/);
  online = true;
  await ui.get("usage-refresh").events.click();
  await done();
  assert.match(ui.get("usage-note").textContent, /не банковское списание/);
});
test("usage refresh is coalesced and hidden pages do not poll", async () => {
  let resolve,
    calls = 0;
  const pending = new Promise((r) => (resolve = r));
  const ui = screen(() => {
    calls++;
    return pending;
  });
  ui.timers[0]();
  ui.observers[0]();
  assert.equal(calls, 1);
  resolve({ ok: true, json: async () => snapshot });
  await done();
  ui.document.hidden = true;
  ui.timers[0]();
  assert.equal(calls, 1);
});
