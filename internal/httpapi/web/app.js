"use strict";
const $ = (id) => document.getElementById(id);
const state = {
  documents: [],
  selected: new Set(),
  rows: new Map(),
  limit: 20,
  hasMore: false,
  loading: null,
  reloadRequested: false,
  asking: false,
  uploading: false,
};
const statuses = {
  queued: "В очереди",
  processing: "Обрабатывается",
  ready: "Готов",
  failed: "Ошибка обработки",
};
const errors = {
  budget_exceeded:
    "Недостаточно дневного бюджета для нового запроса. Следующий день учёта начинается в 00:00 UTC.",
  accounting_unavailable:
    "Учёт расходов временно недоступен. Платный запрос не может продолжиться.",
  invalid_question: "Проверьте вопрос и выбранные документы.",
  documents_not_ready: "Дождитесь обработки всех выбранных документов.",
  embedding_model_mismatch: "Нужна переиндексация документов.",
  question_timeout: "Ответ занял слишком много времени. Попробуйте ещё раз.",
  provider_unavailable: "Модель временно недоступна.",
  provider_error: "Модель не смогла подготовить ответ.",
  not_found: "Документ уже удалён.",
  document_not_found: "Один из документов уже удалён.",
  unavailable: "Сервис временно недоступен.",
  service_unavailable: "Сервис занят. Попробуйте ещё раз.",
  file_too_large: "Файл больше 10 MiB.",
  unsupported_format: "Нужен текстовый TXT, Markdown или PDF.",
};
function feedback(id, message, error = false) {
  $(id).textContent = message;
  $(id).classList.toggle("error", error);
}
async function request(url, options = {}) {
  let response;
  try {
    response = await fetch(url, {
      ...options,
      signal: AbortSignal.timeout(35000),
    });
  } catch {
    throw new Error(
      "Нет ответа от сервиса. Проверьте соединение и повторите запрос.",
    );
  }
  if (!response.ok) {
    let code;
    try {
      code = (await response.json()).error?.code;
    } catch {}
    throw new Error(
      errors[code] ||
        `Запрос не выполнен (${response.status}). Попробуйте ещё раз.`,
    );
  }
  return response.status === 204 ? null : response.json();
}
function updateSelection() {
  $("selection-count").textContent = state.selected.size
    ? `Выбрано: ${state.selected.size} / 20`
    : "Документы не выбраны";
  $("ask").disabled =
    state.asking || !state.selected.size || !$("question").value.trim();
}
function renderDocuments() {
  const container = $("documents");
  const visible = new Set(state.documents.map((doc) => doc.id));
  for (const [id, row] of state.rows) {
    if (!visible.has(id)) {
      row.remove();
      state.rows.delete(id);
    }
  }
  $("doc-count").textContent = state.documents.length;
  if (!state.documents.length) {
    const p = document.createElement("p");
    p.className = "muted empty-documents";
    p.textContent = "Здесь появятся ваши документы.";
    container.replaceChildren(p);
  } else {
    container.querySelector(".empty-documents")?.remove();
  }
  for (const [index, doc] of state.documents.entries()) {
    let row = state.rows.get(doc.id);
    if (!row) {
      row = createDocumentRow(doc);
      state.rows.set(doc.id, row);
    }
    const input = row.querySelector("input");
    row.querySelector("strong").textContent = doc.original_name;
    row.querySelector("small").textContent =
      `${doc.status === "queued" && doc.error_message?.startsWith("Достигнут дневной лимит AI.") ? "Ожидание бюджета" : statuses[doc.status] || doc.status} · ${Math.max(1, Math.round(doc.size_bytes / 1024))} КБ`;
    row.querySelector(".file-icon").textContent =
      doc.mime_type === "application/pdf" ? "PDF" : "TXT";
    input.checked = state.selected.has(doc.id);
    input.disabled = doc.status !== "ready";
    row.title =
      doc.status === "failed"
        ? doc.error_message || "Документ не удалось обработать"
        : "";
    // Keep existing controls attached: polling must not steal checkbox focus.
    if (container.children[index] !== row)
      container.insertBefore(row, container.children[index] || null);
  }
  $("more").hidden = !state.hasMore;
  updateSelection();
}

function createDocumentRow(doc) {
  const node = $("document-template").content.cloneNode(true);
  const row = node.querySelector("article"),
    input = row.querySelector("input");
  input.addEventListener("change", () => {
    if (input.checked && state.selected.size >= 20) {
      input.checked = false;
      feedback("upload-status", "Можно выбрать до 20 документов.", true);
      return;
    }
    if (input.checked) state.selected.add(doc.id);
    else state.selected.delete(doc.id);
    updateSelection();
  });
  const del = row.querySelector(".delete");
  del.setAttribute("aria-label", `Удалить ${doc.original_name}`);
  del.addEventListener("click", async () => {
    if (!confirm(`Удалить «${doc.original_name}» и его индекс?`)) return;
    del.disabled = true;
    try {
      await request(`/api/v1/documents/${doc.id}`, { method: "DELETE" });
      state.selected.delete(doc.id);
      await loadDocuments();
      feedback("upload-status", "Документ удалён.");
    } catch (e) {
      feedback("upload-status", e.message, true);
      del.disabled = false;
    }
  });
  return row;
}

function loadDocuments() {
  state.reloadRequested = true;
  if (!state.loading) state.loading = refreshDocuments();
  return state.loading;
}

async function refreshDocuments() {
  $("refresh").disabled = true;
  $("more").disabled = true;
  try {
    do {
      state.reloadRequested = false;
      try {
        const documents = new Map(),
          limit = state.limit;
        let hasMore = false;
        // API pages allow at most 100 records and an offset up to 100000.
        for (let offset = 0; offset < limit && offset <= 100000;) {
          const pageSize = Math.min(100, limit - offset);
          const data = await request(
            `/api/v1/documents?limit=${pageSize}&offset=${offset}`,
          );
          if (state.reloadRequested) break;
          for (const doc of data.items)
            if (!documents.has(doc.id)) documents.set(doc.id, doc);
          hasMore =
            data.items.length === pageSize &&
            (offset < 100000 || pageSize < 100);
          offset += pageSize;
          if (data.items.length < pageSize) break;
        }
        // An upload/delete requested a newer snapshot; discard this response.
        if (state.reloadRequested) continue;
        state.documents = [...documents.values()];
        state.hasMore = hasMore;
        const ready = new Set(
          state.documents.filter((d) => d.status === "ready").map((d) => d.id),
        );
        for (const id of state.selected)
          if (!ready.has(id)) state.selected.delete(id);
        renderDocuments();
      } catch (e) {
        if (!state.reloadRequested) feedback("upload-status", e.message, true);
      }
    } while (state.reloadRequested);
  } finally {
    state.loading = null;
    $("refresh").disabled = false;
    $("more").disabled = false;
  }
}
async function upload(file) {
  if (!file || state.uploading) return;
  if (file.size > 10 * 1024 * 1024) {
    feedback("upload-status", "Файл больше 10 MiB.", true);
    return;
  }
  state.uploading = true;
  $("file").disabled = true;
  feedback("upload-status", "Загружаем документ…");
  const body = new FormData();
  body.append("file", file);
  try {
    await request("/api/v1/documents", { method: "POST", body });
    feedback(
      "upload-status",
      "Загружен. Выберите документ, когда появится статус «Готов».",
    );
    await loadDocuments();
  } catch (e) {
    feedback("upload-status", e.message, true);
  } finally {
    state.uploading = false;
    $("file").disabled = false;
    $("file").value = "";
  }
}
$("file").addEventListener("change", (e) => upload(e.target.files[0]));
for (const type of ["dragenter", "dragover"])
  $("dropzone").addEventListener(type, (e) => {
    e.preventDefault();
    $("dropzone").classList.add("dragging");
  });
for (const type of ["dragleave", "drop"])
  $("dropzone").addEventListener(type, (e) => {
    e.preventDefault();
    $("dropzone").classList.remove("dragging");
  });
$("dropzone").addEventListener("drop", (e) => upload(e.dataTransfer.files[0]));
$("refresh").addEventListener("click", loadDocuments);
$("more").addEventListener("click", () => {
  if (state.loading || !state.hasMore) return;
  state.limit += 20;
  loadDocuments();
});
$("question").addEventListener("input", updateSelection);
$("question").addEventListener("keydown", (e) => {
  if ((e.ctrlKey || e.metaKey) && e.key === "Enter") {
    e.preventDefault();
    if (!$("ask").disabled) $("question-form").requestSubmit();
  }
});
$("question-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  if (state.asking || !state.selected.size) return;
  const question = $("question").value.trim();
  if (!question || new TextEncoder().encode(question).length > 4000) {
    feedback(
      "question-status",
      "Вопрос должен содержать от 1 до 4000 байт UTF-8.",
      true,
    );
    return;
  }
  const ids = [...state.selected],
    names = new Map(state.documents.map((d) => [d.id, d.original_name]));
  state.asking = true;
  updateSelection();
  feedback("question-status", "Ищем фрагменты и готовим ответ…");
  $("result").hidden = true;
  try {
    const answer = await request("/api/v1/questions", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ question, document_ids: ids }),
    });
    $("answer-title").textContent = question;
    $("answer").textContent = answer.answer;
    $("sources").replaceChildren();
    $("source-count").textContent = `${answer.sources.length} фрагм.`;
    for (const [index, source] of answer.sources.entries()) {
      const details = document.createElement("details"),
        summary = document.createElement("summary"),
        quote = document.createElement("blockquote");
      details.className = "source";
      details.open = true;
      summary.textContent = `${index + 1}. ${names.get(source.document_id) || source.document_id}${source.page ? ` · стр. ${source.page}` : ""}`;
      quote.textContent = source.snippet;
      details.append(summary, quote);
      $("sources").append(details);
    }
    if (!answer.sources.length) {
      const p = document.createElement("p");
      p.className = "muted";
      p.textContent = "Подходящих источников для ответа нет.";
      $("sources").append(p);
    }
    $("result").hidden = false;
    $("empty-answer").hidden = true;
    feedback("question-status", "Ответ готов.");
  } catch (e) {
    feedback("question-status", e.message, true);
  } finally {
    state.asking = false;
    updateSelection();
  }
});
$("copy").addEventListener("click", async () => {
  try {
    await navigator.clipboard.writeText($("answer").textContent);
    feedback("question-status", "Ответ скопирован.");
  } catch {
    feedback(
      "question-status",
      "Не удалось скопировать. Выделите текст вручную.",
      true,
    );
  }
});
async function connection() {
  try {
    await request("/readyz");
    $("connection").textContent = "Сервис готов";
    $("connection").classList.add("online");
  } catch {
    $("connection").textContent = "Нет связи с БД";
    $("connection").classList.remove("online");
  }
}
loadDocuments();
connection();
setInterval(() => {
  if (
    !document.hidden &&
    !state.loading &&
    state.documents.some(
      (d) => d.status === "queued" || d.status === "processing",
    )
  )
    loadDocuments();
}, 3000);
setInterval(() => {
  if (!document.hidden) connection();
}, 15000);
