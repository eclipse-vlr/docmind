"use strict";
(() => {
  const root = document.documentElement;
  const system = window.matchMedia("(prefers-color-scheme: dark)");
  let preference = null;
  try {
    preference = localStorage.getItem("docmind-theme");
  } catch {}
  if (preference !== "light" && preference !== "dark") preference = null;
  function apply() {
    const dark = preference ? preference === "dark" : system.matches;
    root.dataset.theme = dark ? "dark" : "light";
    const button = document.getElementById("theme-toggle");
    if (button) {
      button.setAttribute("aria-pressed", String(dark));
      button.setAttribute(
        "aria-label",
        dark ? "Включить светлую тему" : "Включить тёмную тему",
      );
      button.title = dark ? "Светлая тема" : "Тёмная тема";
    }
  }
  apply();
  system.addEventListener("change", () => {
    if (!preference) apply();
  });
  window.addEventListener("storage", (event) => {
    if (event.key !== "docmind-theme" && event.key !== null) return;
    preference =
      event.newValue === "light" || event.newValue === "dark"
        ? event.newValue
        : null;
    apply();
  });
  document.addEventListener("DOMContentLoaded", () => {
    apply();
    document.getElementById("theme-toggle").addEventListener("click", () => {
      preference = root.dataset.theme === "dark" ? "light" : "dark";
      try {
        localStorage.setItem("docmind-theme", preference);
      } catch {}
      apply();
    });
  });
})();
