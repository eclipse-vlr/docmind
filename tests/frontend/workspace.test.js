"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");

const script = fs.readFileSync(
  path.join(__dirname, "../../internal/httpapi/web/app.js"),
  "utf8",
);

// Model only the DOM operations used by the workspace. Removing a focused
// element drops focus, so replacing rows cannot accidentally pass focus tests.
function browser(fetchResponse) {
  const document = { hidden: false, activeElement: null };
  class Element {
    constructor(tag = "div", className = "") {
      this.tagName = tag;
      this.className = className;
      this.children = [];
      this.dataset = {};
      this.events = new Map();
      this.value = "";
      this.textContent = "";
      this.checked = false;
      this.disabled = false;
      this.hidden = false;
      this.classList = { add() {}, remove() {}, toggle() {} };
    }
    addEventListener(type, fn) {
      this.events.set(type, fn);
    }
    emit(type, event = {}) {
      return this.events.get(type)?.(event);
    }
    setAttribute() {}
    contains(node) {
      return (
        this === node || this.children.some((child) => child.contains(node))
      );
    }
    focus() {
      document.activeElement = this;
    }
    remove() {
      if (this.contains(document.activeElement)) document.activeElement = null;
      if (this.parent) {
        this.parent.children.splice(this.parent.children.indexOf(this), 1);
        this.parent = null;
      }
    }
    append(...children) {
      for (const child of children) {
        if (child.tagName === "fragment") this.append(...[...child.children]);
        else this.insertBefore(child, null);
      }
    }
    insertBefore(child, before) {
      if (child === before) return;
      child.remove();
      const index = before
        ? this.children.indexOf(before)
        : this.children.length;
      this.children.splice(index, 0, child);
      child.parent = this;
    }
    replaceChildren(...children) {
      for (const child of [...this.children]) child.remove();
      this.append(...children);
    }
    querySelector(selector) {
      for (const child of this.children) {
        const matches = selector.startsWith(".")
          ? child.className.split(" ").includes(selector.slice(1))
          : child.tagName === selector;
        if (matches) return child;
        const nested = child.querySelector(selector);
        if (nested) return nested;
      }
      return null;
    }
  }
  const nodes = new Map();
  const element = (id) => {
    if (!nodes.has(id)) nodes.set(id, new Element());
    return nodes.get(id);
  };
  element("document-template").content = {
    cloneNode() {
      const fragment = new Element("fragment");
      const row = new Element("article", "document");
      row.append(
        new Element("input"),
        new Element("strong"),
        new Element("small"),
        new Element("span", "file-icon"),
        new Element("button", "delete"),
      );
      fragment.append(row);
      return fragment;
    },
  };
  document.getElementById = element;
  document.createElement = (tag) => new Element(tag);
  const intervals = new Map();
  const requests = [];
  const context = vm.createContext({
    document,
    navigator: {},
    AbortSignal,
    TextEncoder,
    FormData: class extends Map {
      append(key, value) {
        this.set(key, value);
      }
    },
    confirm: () => true,
    setInterval: (fn, ms) => intervals.set(ms, fn),
    fetch: async (url, options) => {
      if (url === "/readyz") return response({ status: "ready" });
      requests.push({ url, options });
      return fetchResponse(url, options);
    },
  });
  vm.runInContext(script, context);
  return {
    document,
    element,
    requests,
    poll: () => intervals.get(3000)(),
    upload: (file) => {
      context.file = file;
      return vm.runInContext("upload(file)", context);
    },
    refresh: () => vm.runInContext("loadDocuments()", context),
    rows: () =>
      element("documents").children.filter((e) => e.tagName === "article"),
  };
}

function response(body, status = 200) {
  return { ok: status < 400, status, json: async () => body };
}

function documentRecord(id, status = "ready") {
  return {
    id: String(id),
    original_name: `${id}.txt`,
    mime_type: "text/plain",
    size_bytes: 10,
    status,
  };
}

function page(url, documents) {
  const params = new URL(url, "http://localhost").searchParams;
  const limit = Number(params.get("limit"));
  const offset = Number(params.get("offset"));
  assert.ok(limit >= 1 && limit <= 100, "API page size exceeded");
  return response({
    items: documents.slice(offset, offset + limit),
    limit,
    offset,
  });
}

function deferred() {
  let resolve, reject;
  const promise = new Promise((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { promise, resolve, reject };
}

async function until(condition) {
  for (let attempt = 0; attempt < 30; attempt++) {
    if (condition()) return;
    await new Promise(setImmediate);
  }
  assert.ok(condition(), "expected browser state was not reached");
}

test("load more reaches documents beyond the first 100", async () => {
  const documents = Array.from({ length: 125 }, (_, i) => documentRecord(i));
  const ui = browser((url) => page(url, documents));
  await until(() => ui.rows().length === 20);
  const selected = ui.rows()[0].querySelector("input");
  selected.checked = true;
  selected.emit("change");
  for (let click = 0; click < 10 && !ui.element("more").hidden; click++) {
    ui.element("more").emit("click");
    await until(() => !ui.element("refresh").disabled);
  }
  assert.equal(ui.rows().length, 125);
  assert.equal(
    new Set(ui.rows().map((row) => row.querySelector("strong").textContent))
      .size,
    125,
  );
  assert.ok(ui.rows()[0].querySelector("input").checked);
  assert.ok(ui.element("more").hidden);
  assert.ok(
    ui.requests.some(
      ({ url }) =>
        Number(new URL(url, "http://localhost").searchParams.get("offset")) > 0,
    ),
  );
});

test("upload waits for a fresh list after an already running GET", async () => {
  const oldList = deferred(),
    newList = deferred();
  let gets = 0;
  const ui = browser((url, options) => {
    if (options.method === "POST")
      return response({ id: "uploaded", status: "queued" }, 202);
    return ++gets === 1 ? oldList.promise : newList.promise;
  });
  let completed = false;
  const uploading = ui.upload({ size: 10 }).then(() => {
    completed = true;
  });
  await until(() =>
    ui.requests.some(({ options }) => options.method === "POST"),
  );
  await new Promise(setImmediate);
  assert.equal(completed, false, "upload refresh was discarded");
  oldList.resolve(response({ items: [] }));
  await until(() => gets === 2);
  assert.equal(completed, false, "upload did not wait for its refresh");
  newList.resolve(response({ items: [documentRecord("uploaded", "queued")] }));
  await uploading;
  assert.equal(ui.rows().length, 1);
  assert.equal(
    ui.rows()[0].querySelector("strong").textContent,
    "uploaded.txt",
  );
});

test("delete refresh is retained while a stale GET is running", async () => {
  const documents = [documentRecord("deleted")];
  const stale = deferred();
  let gets = 0;
  const ui = browser((url, options) => {
    if (options.method === "DELETE") {
      documents.length = 0;
      return response(null, 204);
    }
    return ++gets === 2 ? stale.promise : page(url, documents);
  });
  await until(() => ui.rows().length === 1);
  const refreshing = ui.refresh();
  const deleting = ui.rows()[0].querySelector(".delete").emit("click");
  await until(() => documents.length === 0);
  stale.resolve(response({ items: [documentRecord("deleted")] }));
  await Promise.all([refreshing, deleting]);
  assert.equal(ui.rows().length, 0);
  assert.equal(gets, 3);
});

test("polling updates pending documents while preserving checkbox focus and selection", async () => {
  let documents = [
    documentRecord("chosen"),
    documentRecord("pending", "queued"),
  ];
  const ui = browser((url) => page(url, documents));
  await until(() => ui.rows().length === 2);
  const selected = ui.rows()[0].querySelector("input");
  selected.checked = true;
  selected.emit("change");
  selected.focus();
  documents = [documentRecord("chosen"), documentRecord("pending")];
  ui.poll();
  await until(() => !ui.element("refresh").disabled);
  assert.equal(ui.rows()[1].querySelector("input").disabled, false);
  assert.equal(ui.rows()[0].querySelector("input"), selected);
  assert.equal(ui.document.activeElement, selected);
  assert.ok(selected.checked);
});

test("inserting a new document keeps focus on the existing row", async () => {
  let documents = [documentRecord("chosen")];
  const ui = browser((url) => page(url, documents));
  await until(() => ui.rows().length === 1);
  const selected = ui.rows()[0].querySelector("input");
  selected.checked = true;
  selected.emit("change");
  selected.focus();
  documents = [documentRecord("new"), documentRecord("chosen")];
  await ui.refresh();
  assert.equal(ui.rows().length, 2);
  assert.equal(ui.rows()[1].querySelector("input"), selected);
  assert.equal(ui.document.activeElement, selected);
  assert.ok(selected.checked);
});

test("a queued refresh still runs when the previous GET fails", async () => {
  const first = deferred();
  let gets = 0;
  const ui = browser((url) =>
    ++gets === 1 ? first.promise : page(url, [documentRecord("retry")]),
  );
  const refreshing = ui.refresh();
  first.reject(new Error("injected network failure"));
  await refreshing;
  assert.equal(ui.rows().length, 1);
  assert.equal(ui.rows()[0].querySelector("strong").textContent, "retry.txt");
  assert.equal(ui.element("refresh").disabled, false);
});
