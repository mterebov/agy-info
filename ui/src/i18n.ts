export type Locale = "en" | "ru";
export type ContextStatus = "ok" | "warn" | "high";
export type BreakdownId = "baseline" | "tools" | "assistant" | "user_sys";
export type SubagentStatus = "running" | "done" | "idle" | "stopped";
export type PlanStepStatus =
  | "pending"
  | "in_progress"
  | "done"
  | "skipped"
  | "failed";

export function detectLocale(hostLocale?: string | null): Locale {
  const host = typeof hostLocale === "string" ? hostLocale.trim() : "";
  const nav =
    typeof navigator !== "undefined" && typeof navigator.language === "string"
      ? navigator.language.trim()
      : "";
  const raw = host || nav;
  return raw.toLowerCase().startsWith("ru") ? "ru" : "en";
}

function pluralRu(n: number, one: string, few: string, many: string): string {
  const abs = Math.abs(Math.round(n)) % 100;
  const last = abs % 10;
  if (abs >= 11 && abs <= 19) return `${n} ${many}`;
  if (last === 1) return `${n} ${one}`;
  if (last >= 2 && last <= 4) return `${n} ${few}`;
  return `${n} ${many}`;
}

export function pluralSteps(n: number, locale: Locale): string {
  if (locale === "ru") return pluralRu(n, "шаг", "шага", "шагов");
  return n === 1 ? "1 step" : `${n} steps`;
}

export function pluralCalls(n: number, locale: Locale): string {
  if (locale === "ru") return pluralRu(n, "вызов", "вызова", "вызовов");
  return n === 1 ? "1 call" : `${n} calls`;
}

export function fmtDuration(
  min: number | null | undefined,
  locale: Locale
): string | null {
  if (min === null || min === undefined || !Number.isFinite(min)) return null;
  const total = Math.max(0, Math.round(min));
  const hUnit = locale === "ru" ? "ч" : "h";
  const mUnit = locale === "ru" ? "м" : "m";
  if (total < 1) return `<1${mUnit}`;
  const h = Math.floor(total / 60);
  const m = total % 60;
  if (h === 0) return `${m}${mUnit}`;
  if (m === 0) return `${h}${hUnit}`;
  return `${h}${hUnit} ${String(m).padStart(2, "0")}${mUnit}`;
}

export function fmtSize(kb: number, locale: Locale): string {
  const v = Number.isFinite(kb) ? Math.max(0, kb) : 0;
  const mbUnit = locale === "ru" ? "МБ" : "MB";
  const kbUnit = locale === "ru" ? "КБ" : "KB";
  if (v >= 1024) return `${(v / 1024).toFixed(1)} ${mbUnit}`;
  if (v >= 100) return `${(v / 1024).toFixed(2)} ${mbUnit}`;
  return `${Math.round(v)} ${kbUnit}`;
}

export function fmtSessionTime(
  iso: string | undefined,
  locale: Locale
): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  const now = new Date();
  const pad = (x: number) => String(x).padStart(2, "0");
  const hm = `${pad(d.getHours())}:${pad(d.getMinutes())}`;
  const sameDay =
    d.getFullYear() === now.getFullYear() &&
    d.getMonth() === now.getMonth() &&
    d.getDate() === now.getDate();
  if (sameDay) return hm;
  if (locale === "ru") {
    return `${pad(d.getDate())}.${pad(d.getMonth() + 1)} ${hm}`;
  }
  return `${pad(d.getMonth() + 1)}/${pad(d.getDate())} ${hm}`;
}

export interface Strings {
  // Toolbar
  toolbarAria: string;
  backToChat: string;
  backToChatTitle: string;
  autoActive: string;
  sessionSelectAria: string;
  autoRefreshAria: string;
  autoOff: string;
  autoSec: (n: number) => string;
  refreshTitle: string;
  refreshAria: string;
  // Empty / Error
  emptyTitle: string;
  emptyDesc: string;
  refreshBtn: string;
  retryBtn: string;
  errServerSnapshot: string;
  errUnexpectedFormat: string;
  errCancelled: string;
  errConnectMcp: string;
  errUpdatePrefix: (msg: string) => string;
  errConnectHost: string;
  errInitPrefix: (msg: string) => string;
  toastOpenFileFailed: string;
  toastOpenFileUnsupported: string;
  // Block 1: Context Window
  windowTitle: string;
  windowAria: string;
  waitingFirstStep: string;
  statusLabel: (status: ContextStatus) => string;
  advice: (status: ContextStatus, compactions: number) => string;
  compactionChipTitle: (compactions: number, peakFmt: string) => string;
  barScaleAria: string;
  barWindow: string;
  barBreakdown: string;
  unknownModel: string;
  breakdownLabel: (id: BreakdownId, compactions: number) => string;
  segmentTip: (label: string, tokensFmt: string, sharePctFmt: string) => string;
  peakTip: (peakFmt: string, pctFmt: string) => string;
  progressTrackAria: (fillPctFmt: string) => string;
  cacheLabel: string;
  cacheMeta: (lastHitPctInt: string, cacheReadFmt: string) => string;
  cacheTitle: (
    cumPctFmt: string,
    cacheReadFmt: string,
    promptTotalFmt: string,
    lastPctFmt: string
  ) => string;
  activityBtnTitle: string;
  activityPrefix: string;
  graphWord: string;
  // Block 2: Subagents
  subagentsTitle: string;
  subagentFallback: (shortId: string) => string;
  subagentsRunning: (n: number) => string;
  subagentsTotal: (n: number) => string;
  subagentsEmpty: string;
  subagentStatusLabel: (status: SubagentStatus) => string;
  subagentRunningPrefix: string;
  subagentRunningToolText: (toolName: string) => string;
  logBtn: string;
  logBtnTitle: string;
  subagentRowTitle: string;
  subagentCtxTitle: string;
  showAll: (n: number) => string;
  collapse: string;
  // Block 3: Plan
  planTitle: string;
  planAria: string;
  planCompleted: string;
  fileBtn: string;
  fileBtnTitle: string;
  planNow: string;
  planEmpty: string;
  planStepAria: (status: PlanStepStatus) => string;
  // Screen B: Activity & Growth
  backBtn: string;
  activityScreenTitle: string;
  openTranscriptTitle: string;
  tilePeakFree: string;
  tilePeakSub: (freeFmt: string, compactions: number) => string;
  tilePromptCache: string;
  tileCacheSub: (
    lastPctFmt: string,
    cacheReadFmt: string,
    promptTotalFmt: string
  ) => string;
  tileActivity: string;
  tileStepsUnit: string;
  tileCallsUnit: string;
  userTurnsShort: (n: number) => string;
  chartTitle: string;
  chartEmpty: string;
  chartAria: string;
  chartAxisStep: (stepNum: number) => string;
  chartCompactionRange: (beforeFmt: string, afterFmt: string) => string;
  chartCompactionTo: (afterFmt: string) => string;
  chartDefaultReadout: (stepsCount: number, lastTotalFmt: string) => string;
  chartHoverReadout: (
    stepNum: number,
    totalFmt: string,
    cacheFmt: string,
    newFmt: string
  ) => string;
  chartTooltipStep: (stepNum: number) => string;
  chartTooltipCompacted: string;
  chartTooltipContext: string;
  chartTooltipCache: string;
  chartTooltipNew: string;
  chartTooltipOut: string;
  // Footer
  checkedAt: (timeStr: string) => string;
  sourceCache: string;
  sourceUpdated: string;
  live1s: string;
  autoN: (sec: number) => string;
  langToggleTitle: string;
  langToggleAria: string;
}

const EN: Strings = {
  toolbarAria: "Monitor controls",
  backToChat: "← Back to chat",
  backToChatTitle: "Return to parent chat",
  autoActive: "Auto (active)",
  sessionSelectAria: "Select session",
  autoRefreshAria: "Auto-refresh interval",
  autoOff: "Off",
  autoSec: (n) => `${n}s`,
  refreshTitle: "Refresh (rescan)",
  refreshAria: "Refresh",

  emptyTitle: "No context data",
  emptyDesc:
    "No completed LLM steps yet in the selected session, or transcript is empty. Pick another session or click ↻.",
  refreshBtn: "Refresh",
  retryBtn: "Retry",
  errServerSnapshot: "Server returned an error while fetching context snapshot.",
  errUnexpectedFormat: "Unexpected response format from the AGY Info server.",
  errCancelled: "Request was cancelled. Click ↻ to load data.",
  errConnectMcp: "Failed to reach the MCP server.",
  errUpdatePrefix: (msg) => `Refresh error: ${msg}`,
  errConnectHost: "Failed to connect to host.",
  errInitPrefix: (msg) => `MCP App initialization error: ${msg}`,
  toastOpenFileFailed: "Could not open file in side pane.",
  toastOpenFileUnsupported: "Opening files is not supported in this host.",

  windowTitle: "Context Window",
  windowAria: "Context window usage",
  waitingFirstStep: "Waiting for the first completed LLM step…",
  statusLabel: (s) =>
    s === "high" ? "Nearly full" : s === "warn" ? "Half+" : "Free",
  advice: (status, compactions) => {
    if (status === "high") {
      return "Nearly full — run /compact or start a new chat.";
    }
    if (status === "warn") {
      return "Over half full — consider /compact before a large task.";
    }
    if (compactions > 0) {
      return `Compacted (${compactions}×) and mostly empty — /compact is not needed.`;
    }
    return "Plenty of headroom — /compact is not needed.";
  },
  compactionChipTitle: (n, peak) => `Context compactions: ${n} · Peak: ${peak}`,
  barScaleAria: "Bar scale",
  barWindow: "Window",
  barBreakdown: "Breakdown",
  unknownModel: "Unknown model",
  breakdownLabel: (id, compactions) => {
    switch (id) {
      case "baseline":
        return compactions > 0 ? "System & summary" : "System & startup";
      case "tools":
        return "Tool outputs";
      case "assistant":
        return "Assistant & thinking";
      case "user_sys":
        return "User & subagents";
    }
  },
  segmentTip: (label, tokens, share) =>
    `${label}: ${tokens} (${share}% of context)`,
  peakTip: (peak, pct) => `Peak before compaction: ${peak} (${pct}% of window)`,
  progressTrackAria: (pct) => `${pct}% of context window used`,
  cacheLabel: "Cache",
  cacheMeta: (lastPct, read) => `last ${lastPct}% · ${read}`,
  cacheTitle: (cumPct, read, total, lastPct) =>
    `Session cache: ${cumPct}% (${read} of ${total}). Last step: ${lastPct}%`,
  activityBtnTitle: "Open growth chart and detailed activity stats",
  activityPrefix: "Activity:",
  graphWord: "Graph",

  subagentsTitle: "Subagents",
  subagentFallback: (id) => `Subagent #${id}`,
  subagentsRunning: (n) => `${n} running`,
  subagentsTotal: (n) => `${n} total`,
  subagentsEmpty: "No subagents were launched in this session",
  subagentStatusLabel: (s) => {
    switch (s) {
      case "running":
        return "Running";
      case "done":
        return "Done";
      case "idle":
        return "Idle";
      default:
        return "Stopped";
    }
  },
  subagentRunningPrefix: "Running:",
  subagentRunningToolText: (tool) => `Running: ${tool}`,
  logBtn: "Log ↗",
  logBtnTitle: "Open transcript log in side pane",
  subagentRowTitle: "Inspect subagent context",
  subagentCtxTitle: "Subagent current context",
  showAll: (n) => `Show all (${n})`,
  collapse: "Collapse",

  planTitle: "Plan",
  planAria: "Execution plan",
  planCompleted: "Completed",
  fileBtn: "File ↗",
  fileBtnTitle: "Open plan file in side pane",
  planNow: "Now:",
  planEmpty: "No plan yet",
  planStepAria: (s) => {
    switch (s) {
      case "done":
        return "Completed";
      case "in_progress":
        return "In progress";
      case "failed":
        return "Failed";
      case "skipped":
        return "Skipped";
      default:
        return "Pending";
    }
  },

  backBtn: "← Back",
  activityScreenTitle: "Activity & Growth",
  openTranscriptTitle: "Open transcript in side pane",
  tilePeakFree: "Peak / free",
  tilePeakSub: (free, comp) => `Free ${free} · compacted ${comp}×`,
  tilePromptCache: "Prompt cache",
  tileCacheSub: (lastPct, read, total) =>
    `Step ${lastPct}% · ${read} / ${total}`,
  tileActivity: "Activity",
  tileStepsUnit: "steps",
  tileCallsUnit: "calls",
  userTurnsShort: (n) => `${n} ${n === 1 ? "msg" : "msgs"}`,
  chartTitle: "Context growth by LLM step",
  chartEmpty: "Not enough steps to plot the chart",
  chartAria: "Context window growth chart by LLM step",
  chartAxisStep: (step) => `Step ${step}`,
  chartCompactionRange: (before, after) => `⚡ compacted ${before} → ${after}`,
  chartCompactionTo: (after) => `⚡ compacted → ${after}`,
  chartDefaultReadout: (count, last) =>
    `${pluralSteps(count, "en")} · last ${last}`,
  chartHoverReadout: (step, total, cache, nw) =>
    `Step #${step} · ${total} (cache ${cache}, new ${nw})`,
  chartTooltipStep: (step) => `Step #${step}`,
  chartTooltipCompacted: "⚡ compacted",
  chartTooltipContext: "Context",
  chartTooltipCache: "From cache",
  chartTooltipNew: "New",
  chartTooltipOut: "Output",

  checkedAt: (time) => `Checked ${time}`,
  sourceCache: "cache (0% CPU)",
  sourceUpdated: "updated",
  live1s: "live 1s",
  autoN: (sec) => `auto ${sec}s`,
  langToggleTitle: "Switch language (EN / RU)",
  langToggleAria: "Switch interface language to Russian",
};

const RU: Strings = {
  toolbarAria: "Управление мониторингом",
  backToChat: "← К чату",
  backToChatTitle: "Вернуться к родительскому чату",
  autoActive: "Авто (активный)",
  sessionSelectAria: "Выбор сессии",
  autoRefreshAria: "Интервал автообновления",
  autoOff: "Выкл",
  autoSec: (n) => `${n}с`,
  refreshTitle: "Обновить (пересканировать)",
  refreshAria: "Обновить",

  emptyTitle: "Нет данных по контексту",
  emptyDesc:
    "В выбранной сессии пока нет завершённых шагов LLM или транскрипт пуст. Выберите другую сессию или нажмите «↻».",
  refreshBtn: "Обновить",
  retryBtn: "Повторить",
  errServerSnapshot: "Сервер вернул ошибку при получении снимка контекста.",
  errUnexpectedFormat: "Неожиданный формат ответа от сервера AGY Info.",
  errCancelled: "Запрос был отменён. Нажмите «↻» для загрузки данных.",
  errConnectMcp: "Не удалось связаться с MCP-сервером.",
  errUpdatePrefix: (msg) => `Ошибка обновления: ${msg}`,
  errConnectHost: "Не удалось подключиться к хосту.",
  errInitPrefix: (msg) => `Ошибка инициализации MCP App: ${msg}`,
  toastOpenFileFailed: "Не удалось открыть файл в боковой панели.",
  toastOpenFileUnsupported: "Открытие файлов недоступно в этом окружении.",

  windowTitle: "Заполнение окна",
  windowAria: "Заполнение контекстного окна",
  waitingFirstStep: "Ожидание первого завершённого шага LLM…",
  statusLabel: (s) =>
    s === "high" ? "Почти полно" : s === "warn" ? "Половина+" : "Свободно",
  advice: (status, compactions) => {
    if (status === "high") {
      return "Окно почти заполнено — выполните /compact или начните новый чат.";
    }
    if (status === "warn") {
      return "Заполнено больше половины — рассмотрите /compact перед крупной задачей.";
    }
    if (compactions > 0) {
      return `Контекст сжат (${compactions}×) и в основном свободен — /compact не нужен.`;
    }
    return "Запаса достаточно — /compact не нужен.";
  },
  compactionChipTitle: (n, peak) => `Сжатий контекста: ${n} · Пик: ${peak}`,
  barScaleAria: "Масштаб шкалы",
  barWindow: "Окно",
  barBreakdown: "Состав",
  unknownModel: "Неизвестная модель",
  breakdownLabel: (id, compactions) => {
    switch (id) {
      case "baseline":
        return compactions > 0
          ? "Система и сжатая история"
          : "Система и старт";
      case "tools":
        return "Инструменты";
      case "assistant":
        return "Ответы и мысли";
      case "user_sys":
        return "Сообщения и сабагенты";
    }
  },
  segmentTip: (label, tokens, share) =>
    `${label}: ${tokens} (${share}% контекста)`,
  peakTip: (peak, pct) => `Пик до сжатия: ${peak} (${pct}% окна)`,
  progressTrackAria: (pct) => `Заполнено ${pct}% контекстного окна`,
  cacheLabel: "Кэш",
  cacheMeta: (lastPct, read) => `посл. ${lastPct}% · ${read}`,
  cacheTitle: (cumPct, read, total, lastPct) =>
    `Кэш за сессию: ${cumPct}% (${read} из ${total}). Последний шаг: ${lastPct}%`,
  activityBtnTitle: "Открыть график и детальную статистику активности",
  activityPrefix: "Активность:",
  graphWord: "График",

  subagentsTitle: "Сабагенты",
  subagentFallback: (id) => `Сабагент #${id}`,
  subagentsRunning: (n) => `${n} в работе`,
  subagentsTotal: (n) => `всего ${n}`,
  subagentsEmpty: "Сабагенты в этой сессии не запускались",
  subagentStatusLabel: (s) => {
    switch (s) {
      case "running":
        return "В работе";
      case "done":
        return "Готово";
      case "idle":
        return "Ожидание";
      default:
        return "Остановлен";
    }
  },
  subagentRunningPrefix: "Выполняет:",
  subagentRunningToolText: (tool) => `Выполняет: ${tool}`,
  logBtn: "Лог ↗",
  logBtnTitle: "Открыть лог транскрипта в боковой панели",
  subagentRowTitle: "Открыть контекст сабагента",
  subagentCtxTitle: "Текущий контекст сабагента",
  showAll: (n) => `Показать все (${n})`,
  collapse: "Свернуть",

  planTitle: "План",
  planAria: "План выполнения",
  planCompleted: "Выполнен",
  fileBtn: "Файл ↗",
  fileBtnTitle: "Открыть файл плана в боковой панели",
  planNow: "Сейчас:",
  planEmpty: "План пока не сформирован",
  planStepAria: (s) => {
    switch (s) {
      case "done":
        return "Выполнено";
      case "in_progress":
        return "В процессе";
      case "failed":
        return "Ошибка";
      case "skipped":
        return "Пропущено";
      default:
        return "Ожидает";
    }
  },

  backBtn: "← Назад",
  activityScreenTitle: "Активность и график",
  openTranscriptTitle: "Открыть транскрипт в боковой панели",
  tilePeakFree: "Пик / свободно",
  tilePeakSub: (free, comp) => `Свободно ${free} · сжатий ${comp}×`,
  tilePromptCache: "Кэш промпта",
  tileCacheSub: (lastPct, read, total) =>
    `Шаг ${lastPct}% · ${read} / ${total}`,
  tileActivity: "Активность",
  tileStepsUnit: "шаг.",
  tileCallsUnit: "выз.",
  userTurnsShort: (n) => `${n} сообщ.`,
  chartTitle: "Рост контекста по шагам LLM",
  chartEmpty: "Недостаточно шагов для построения графика",
  chartAria: "График динамики объёма контекста по шагам LLM",
  chartAxisStep: (step) => `Шаг ${step}`,
  chartCompactionRange: (before, after) => `⚡ сжато ${before} → ${after}`,
  chartCompactionTo: (after) => `⚡ сжато → ${after}`,
  chartDefaultReadout: (count, last) =>
    `${pluralSteps(count, "ru")} · последн. ${last}`,
  chartHoverReadout: (step, total, cache, nw) =>
    `Шаг #${step} · ${total} (кэш ${cache}, нов. ${nw})`,
  chartTooltipStep: (step) => `Шаг #${step}`,
  chartTooltipCompacted: "⚡ сжато",
  chartTooltipContext: "Контекст",
  chartTooltipCache: "Из кэша",
  chartTooltipNew: "Новые",
  chartTooltipOut: "Вывод",

  checkedAt: (time) => `Проверено ${time}`,
  sourceCache: "кэш (0% CPU)",
  sourceUpdated: "обновлено",
  live1s: "live 1с",
  autoN: (sec) => `авто ${sec}с`,
  langToggleTitle: "Переключить язык (EN / RU)",
  langToggleAria: "Переключить язык интерфейса на английский",
};

export function getStrings(locale: Locale): Strings {
  return locale === "ru" ? RU : EN;
}
