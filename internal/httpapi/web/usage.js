"use strict";
(() => {
  const panel = document.getElementById("usage-panel");
  const money = (value) =>
    new Intl.NumberFormat("ru-RU", {
      style: "currency",
      currency: "RUB",
      minimumFractionDigits: 2,
      maximumFractionDigits: 6,
    }).format(Number(value));
  let loading = false;
  async function refresh() {
    if (loading || document.hidden) return;
    loading = true;
    try {
      const response = await fetch("/api/v1/usage", {
        cache: "no-store",
        signal: AbortSignal.timeout(5000),
      });
      if (!response.ok) throw new Error("unavailable");
      const data = await response.json();
      const set = (id, text) => {
        document.getElementById(id).textContent = text;
      };
      set("usage-estimated", money(data.estimated_rub));
      set("usage-limit", money(data.limit_rub));
      set("usage-reserved", money(data.reserved_rub));
      set("usage-remaining", money(data.remaining_rub));
      set("usage-calls", new Intl.NumberFormat("ru-RU").format(data.calls));
      set(
        "usage-tokens",
        new Intl.NumberFormat("ru-RU").format(
          data.input_tokens + data.output_tokens + data.embedding_tokens,
        ),
      );
      set("usage-period", data.day_utc + " · UTC");
      set(
        "usage-note",
        data.unknown_calls || data.pending_calls
          ? `Запросов без окончательной стоимости: ${data.unknown_calls + data.pending_calls}. Их резерв сохранён.`
          : "Оценка по учтённым токенам Яндекса, не банковское списание. Расходы до включения учёта не восстановлены.",
      );
      const progress = document.getElementById("usage-progress");
      progress.max = Number(data.limit_rub);
      progress.value = Math.min(
        Number(data.limit_rub),
        Number(data.estimated_rub) + Number(data.reserved_rub),
      );
      panel.classList.toggle(
        "budget-full",
        Number(data.remaining_rub) <= 0 || data.reservation_overruns > 0,
      );
      if (data.reservation_overruns > 0)
        set(
          "usage-note",
          "Расход превысил предварительный резерв. Новые платные вызовы заблокированы до следующего UTC-дня; проверьте тарифы.",
        );
    } catch {
      document.getElementById("usage-note").textContent =
        "Учёт временно недоступен. Новые платные вызовы требуют работающего учёта.";
    } finally {
      loading = false;
    }
  }
  document.getElementById("usage-refresh").addEventListener("click", refresh);
  new MutationObserver(refresh).observe(
    document.getElementById("question-status"),
    { childList: true },
  );
  new MutationObserver(refresh).observe(
    document.getElementById("upload-status"),
    { childList: true },
  );
  document.addEventListener("visibilitychange", refresh);
  refresh();
  setInterval(refresh, 15000);
})();
