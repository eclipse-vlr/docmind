"use strict";
const $ = (id) => document.getElementById(id);
const state = {
  documents: [],
  selected: new Set(),
  limit: 20,
  loading: false,
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
  $("documents").replaceChildren();
  $("doc-count").textContent = state.documents.length;
  if (!state.documents.length) {
    const p = document.createElement("p");
    p.className = "muted";
    p.textContent = "Здесь появятся ваши документы.";
    $("documents").append(p);
  }
  for (const doc of state.documents) {
    const node = $("document-template").content.cloneNode(true),
      row = node.querySelector("article"),
      input = node.querySelector("input");
    node.querySelector("strong").textContent = doc.original_name;
    node.querySelector("small").textContent =
      `${statuses[doc.status] || doc.status} · ${Math.max(1, Math.round(doc.size_bytes / 1024))} КБ`;
    node.querySelector(".file-icon").textContent =
      doc.mime_type === "application/pdf" ? "PDF" : "TXT";
    input.checked = state.selected.has(doc.id);
    input.disabled = doc.status !== "ready";
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
    if (doc.status === "failed")
      row.title = doc.error_message || "Документ не удалось обработать";
    const del = node.querySelector(".delete");
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
    $("documents").append(node);
  }
  $("more").hidden = state.documents.length < state.limit || state.limit >= 100;
  updateSelection();
}
async function loadDocuments() {
  if (state.loading) return;
  state.loading = true;
  $("refresh").disabled = true;
  try {
    const data = await request(
      `/api/v1/documents?limit=${state.limit}&offset=0`,
    );
    state.documents = data.items;
    const ready = new Set(
      data.items.filter((d) => d.status === "ready").map((d) => d.id),
    );
    for (const id of state.selected)
      if (!ready.has(id)) state.selected.delete(id);
    renderDocuments();
  } catch (e) {
    feedback("upload-status", e.message, true);
  } finally {
    state.loading = false;
    $("refresh").disabled = false;
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
  state.limit = Math.min(100, state.limit + 20);
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
    !$("documents").contains(document.activeElement) &&
    state.documents.some(
      (d) => d.status === "queued" || d.status === "processing",
    )
  )
    loadDocuments();
}, 3000);
setInterval(() => {
  if (!document.hidden) connection();
}, 15000);
