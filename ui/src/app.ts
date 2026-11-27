import {
  App,
  applyDocumentTheme,
  applyHostStyleVariables,
  type StandardSchemaV1,
} from "@modelcontextprotocol/ext-apps";
import {
  detectLocale,
  fmtDuration,
  fmtSessionTime,
  fmtSize,
  getStrings,
  pluralCalls,
  pluralSteps,
  type BreakdownId,
  type ContextStatus,
  type Locale,
  type PlanStepStatus,
  type Strings,
  type SubagentStatus,
} from "./i18n";

// --- Contract v2 Types ---

type SessionInfo = {
  id: string;
  title: string;
  updated_at: string;
  size_kb: number;
  is_active: boolean;
  is_subagent?: boolean;
};

type BreakdownItem = {
  id: BreakdownId;
  label?: string;
  tokens: number;
};

type TimelinePoint = {
  n: number;
  step: number;
  total: number;
  cache: number;
  new: number;
  out: number;
  compacted_before?: boolean;
};

type ContextSnapshot = {
  model: string;
  window_tokens: number;
  current_tokens: number;
  free_tokens: number;
  peak_tokens: number;
  compactions: number;
  fill_pct: number;
  status: ContextStatus;
  advice?: string;
  last_turn: {
    prompt: number;
    uncached_input: number;
    cache_read: number;
    output: number;
    cache_hit_pct: number;
  };
  cumulative: {
    prompt_total: number;
    uncached_input: number;
    cache_read: number;
    output: number;
    cache_hit_pct: number;
  };
  breakdown: BreakdownItem[];
  counts: {
    user_turns: number;
    llm_steps: number;
    tool_calls: number;
    subagents: number;
    duration_min: number | null;
    transcript_kb: number;
  };
  timeline: TimelinePoint[];
};

type PlanStep = {
  title: string;
  status: PlanStepStatus;
};

type PlanInfo = {
  source: "update_steps" | "artifact" | "focus_only" | "none";
  focus: string;
  focus_updated_at: string;
  artifact_uri: string;
  done_count: number;
  total_count: number;
  steps: PlanStep[];
};

type SubagentInfo = {
  id: string;
  role: string;
  type_name: string;
  model: string;
  status: SubagentStatus;
  focus: string;
  last_tool?: string;
  current_tokens: number;
  window_tokens: number;
  fill_pct: number;
  llm_steps: number;
  tool_calls: number;
  updated_at: string;
  log_uri: string;
};

type SnapshotPayload = {
  unchanged?: boolean;
  revision?: string;
  auto_selected?: boolean;
  conversation_id: string;
  parent_conversation_id?: string;
  log_uri?: string;
  updated_at: string;
  from_cache: boolean;
  sessions?: SessionInfo[];
  snapshot?: null | ContextSnapshot;
  subagents?: SubagentInfo[];
  plan?: PlanInfo;
};

type BarMode = "window" | "composition";
type ViewMode = "main" | "activity";
type AutoRefreshSec = 0 | 1 | 5 | 15;
type FetchMode = "initial" | "poll" | "manual" | "switch";

type UiError =
  | { kind: "raw"; text: string }
  | { kind: "serverSnapshot" }
  | { kind: "unexpectedFormat" }
  | { kind: "cancelled" }
  | { kind: "update"; detail?: string }
  | { kind: "init"; detail?: string };

interface NavFrame {
  parentId: string;
  parentWasAuto: boolean;
  subagentId: string;
  subagentRole: string;
  subagentType: string;
}

interface AppState {
  locale: Locale;
  localeManual: boolean;
  payload: SnapshotPayload | null;
  lastRevision: string | null;
  lastCheckedAt: string;
  lastFromCache: boolean;
  selectedSessionId: string; // "" means Auto (active)
  navStack: NavFrame[];
  autoRefreshSec: AutoRefreshSec;
  barMode: BarMode;
  viewMode: ViewMode;
  subagentsExpanded: boolean;
  inFlight: boolean;
  manualLoading: boolean;
  hasLoadedOnce: boolean;
  error: UiError | null;
}

// --- Constants & State ---

const BREAKDOWN_ORDER: BreakdownId[] = [
  "baseline",
  "tools",
  "assistant",
  "user_sys",
];

const initialLocale = detectLocale(null);
document.documentElement.lang = initialLocale;

const state: AppState = {
  locale: initialLocale,
  localeManual: false,
  payload: null,
  lastRevision: null,
  lastCheckedAt: "",
  lastFromCache: false,
  selectedSessionId: "",
  navStack: [],
  autoRefreshSec: 1,
  barMode: "window",
  viewMode: "main",
  subagentsExpanded: false,
  inFlight: false,
  manualLoading: false,
  hasLoadedOnce: false,
  error: null,
};

let receivedInitialResult = false;
let initialFallbackTimer: ReturnType<typeof setTimeout> | null = null;
let autoRefreshTimer: ReturnType<typeof setInterval> | null = null;
let toastTimer: ReturnType<typeof setTimeout> | null = null;
let pendingFetchMode: FetchMode | null = null;
let requestCounter = 0;

const rootEl = document.getElementById("app") as HTMLElement;

function tr(): Strings {
  return getStrings(state.locale);
}

function setLocale(next: Locale, manual: boolean): void {
  if (manual) {
    state.localeManual = true;
  }
  if (state.locale === next) return;
  state.locale = next;
  document.documentElement.lang = next;
  render();
}

function formatError(err: UiError, t: Strings): string {
  switch (err.kind) {
    case "raw":
      return err.text;
    case "serverSnapshot":
      return t.errServerSnapshot;
    case "unexpectedFormat":
      return t.errUnexpectedFormat;
    case "cancelled":
      return t.errCancelled;
    case "update":
      return t.errUpdatePrefix(err.detail || t.errConnectMcp);
    case "init":
      return t.errInitPrefix(err.detail || t.errConnectHost);
  }
}

// --- Standard Schema validator for Antigravity toggle-aux-pane extension ---

type ToggleAuxPaneResult = {
  isError?: boolean;
  [key: string]: unknown;
};

const toggleAuxPaneResultSchema: StandardSchemaV1<unknown, ToggleAuxPaneResult> =
  {
    "~standard": {
      version: 1,
      vendor: "agy-info",
      validate(value: unknown) {
        if (value && typeof value === "object") {
          return { value: value as ToggleAuxPaneResult };
        }
        return { value: {} };
      },
    },
  };

// --- Formatting helpers ---

function esc(value: unknown): string {
  return String(value ?? "")
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

function fmtTokens(n: number): string {
  const v = Number.isFinite(n) ? Math.max(0, n) : 0;
  if (v >= 1_000_000) {
    const m = v / 1_000_000;
    return `${m >= 10 ? m.toFixed(1) : m.toFixed(2).replace(/\.00$/, "")}M`;
  }
  if (v >= 1_000) {
    const k = v / 1_000;
    return `${k >= 100 ? Math.round(k) : k.toFixed(1).replace(/\.0$/, "")}K`;
  }
  return String(Math.round(v));
}

function fmtPct(n: number): string {
  const v = Number.isFinite(n) ? Math.max(0, n) : 0;
  if (v === 0) return "0";
  if (v >= 100) return String(Math.round(v));
  return v.toFixed(1).replace(/\.0$/, "");
}

function fmtPctInt(n: number): string {
  const v = Number.isFinite(n) ? Math.max(0, n) : 0;
  return String(Math.round(v));
}

function fmtClock(iso: string | undefined): string {
  if (!iso) return "--:--:--";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso.slice(11, 19) || "--:--:--";
  const pad = (x: number) => String(x).padStart(2, "0");
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

function truncate(text: string, maxLen: number): string {
  const clean = text.replace(/\s+/g, " ").trim();
  if (clean.length <= maxLen) return clean;
  return `${clean.slice(0, maxLen - 1)}…`;
}

// --- Payload parsing ---

function isSnapshotPayload(val: unknown): val is SnapshotPayload {
  if (!val || typeof val !== "object") return false;
  const obj = val as Record<string, unknown>;
  if (obj.unchanged === true) {
    return (
      typeof obj.conversation_id === "string" ||
      typeof obj.updated_at === "string" ||
      typeof obj.revision === "string"
    );
  }
  return (
    typeof obj.conversation_id === "string" &&
    ("snapshot" in obj || Array.isArray(obj.sessions))
  );
}

function extractError(result: { content?: unknown }): UiError {
  if (Array.isArray(result.content)) {
    for (const block of result.content) {
      if (
        block &&
        typeof block === "object" &&
        "type" in block &&
        (block as { type?: unknown }).type === "text" &&
        typeof (block as { text?: unknown }).text === "string"
      ) {
        const text = (block as { text: string }).text.trim();
        if (text) return { kind: "raw", text };
      }
    }
  }
  return { kind: "serverSnapshot" };
}

function parseToolResult(result: {
  structuredContent?: unknown;
  content?: unknown;
  isError?: boolean;
}): { payload?: SnapshotPayload; error?: UiError } {
  if (result.isError) {
    return { error: extractError(result) };
  }
  if (isSnapshotPayload(result.structuredContent)) {
    return { payload: result.structuredContent };
  }
  if (Array.isArray(result.content)) {
    for (const block of result.content) {
      if (
        block &&
        typeof block === "object" &&
        (block as { type?: unknown }).type === "text" &&
        typeof (block as { text?: unknown }).text === "string"
      ) {
        const raw = (block as { text: string }).text.trim();
        if (raw.startsWith("{")) {
          try {
            const parsed: unknown = JSON.parse(raw);
            if (isSnapshotPayload(parsed)) return { payload: parsed };
          } catch {
            // ignore non-JSON text
          }
        }
      }
    }
  }
  return { error: { kind: "unexpectedFormat" } };
}

// --- Auto-refresh timer (pauses when hidden, skips when inFlight) ---

function clearAutoTimer(): void {
  if (autoRefreshTimer !== null) {
    clearInterval(autoRefreshTimer);
    autoRefreshTimer = null;
  }
}

function syncAutoTimer(): void {
  clearAutoTimer();
  if (state.autoRefreshSec === 0 || document.hidden) {
    return;
  }
  autoRefreshTimer = setInterval(() => {
    if (document.hidden || state.inFlight) return;
    void fetchSnapshot("poll");
  }, state.autoRefreshSec * 1000);
}

document.addEventListener("visibilitychange", () => {
  if (document.hidden) {
    clearAutoTimer();
  } else {
    if (state.hasLoadedOnce && state.autoRefreshSec > 0 && !state.inFlight) {
      void fetchSnapshot("poll");
    }
    syncAutoTimer();
  }
});

// --- MCP App setup ---

const app = new App({ name: "AGY Info", version: "1.0.0" }, {});

function applyHostContext(ctx: ReturnType<App["getHostContext"]>): void {
  if (!ctx) return;
  if (ctx.theme) {
    applyDocumentTheme(ctx.theme);
  }
  if (ctx.styles?.variables) {
    applyHostStyleVariables(ctx.styles.variables);
  }
  if (!state.localeManual && typeof ctx.locale === "string" && ctx.locale.trim()) {
    setLocale(detectLocale(ctx.locale), false);
  }
}

function showToast(message: string): void {
  const el = document.getElementById("cm-toast");
  if (!el) return;
  el.textContent = message;
  el.classList.add("is-visible");
  if (toastTimer !== null) clearTimeout(toastTimer);
  toastTimer = setTimeout(() => {
    toastTimer = null;
    const current = document.getElementById("cm-toast");
    if (current) current.classList.remove("is-visible");
  }, 3600);
}

async function openInAuxPane(uri: string): Promise<void> {
  if (!uri) return;
  const t = tr();
  try {
    const res = await app.request(
      {
        method: "com.google.antigravity/toggle-aux-pane",
        params: { tabType: "fileView", uri },
      },
      toggleAuxPaneResultSchema
    );
    if (res && res.isError) {
      showToast(t.toastOpenFileFailed);
    }
  } catch (err) {
    const code =
      err && typeof err === "object" && "code" in err
        ? (err as { code?: unknown }).code
        : undefined;
    const msg = err instanceof Error ? err.message : String(err ?? "");
    if (
      code === -32601 ||
      msg.includes("-32601") ||
      msg.toLowerCase().includes("method not found")
    ) {
      showToast(t.toastOpenFileUnsupported);
    } else {
      showToast(t.toastOpenFileFailed);
    }
  }
}

function updateFooterInPlace(): void {
  const t = tr();
  const timeEl = document.getElementById("cm-footer-time");
  if (timeEl) {
    timeEl.textContent = t.checkedAt(fmtClock(state.lastCheckedAt));
  }
  const sourceEl = document.getElementById("cm-footer-source");
  if (sourceEl) {
    sourceEl.textContent = state.lastFromCache
      ? t.sourceCache
      : t.sourceUpdated;
    sourceEl.classList.toggle("is-cache", state.lastFromCache);
    sourceEl.classList.toggle("is-fresh", !state.lastFromCache);
  }
  const liveEl = document.getElementById("cm-footer-live");
  if (liveEl) {
    if (state.autoRefreshSec === 1) {
      liveEl.textContent = t.live1s;
      liveEl.classList.add("is-live");
      liveEl.style.display = "";
    } else if (state.autoRefreshSec > 1) {
      liveEl.textContent = t.autoN(state.autoRefreshSec);
      liveEl.classList.remove("is-live");
      liveEl.style.display = "";
    } else {
      liveEl.style.display = "none";
    }
  }
}

function applyPayload(payload: SnapshotPayload, syncSelection: boolean): void {
  if (payload.revision) {
    state.lastRevision = payload.revision;
  }

  // Fast path: server confirmed revision is unchanged -> 0% DOM re-render!
  if (payload.unchanged && state.payload) {
    state.lastCheckedAt = new Date().toISOString();
    state.lastFromCache = true;
    if (state.error) {
      state.error = null;
      render();
    } else {
      updateFooterInPlace();
    }
    return;
  }

  state.lastCheckedAt = payload.updated_at || new Date().toISOString();

  // Merge sessions fallback if omitted in a partial payload
  if (!payload.sessions && state.payload?.sessions) {
    payload.sessions = state.payload.sessions;
  }

  state.payload = payload;
  state.lastFromCache = Boolean(payload.from_cache);
  state.hasLoadedOnce = true;
  state.error = null;

  if (syncSelection) {
    state.selectedSessionId = payload.auto_selected
      ? ""
      : payload.conversation_id;
  }

  render();
  if (autoRefreshTimer === null && state.autoRefreshSec > 0 && !document.hidden) {
    syncAutoTimer();
  }
}

// Register ALL notification handlers BEFORE app.connect()
app.ontoolinput = (params) => {
  const args = params?.arguments;
  if (args && typeof args.conversation_id === "string" && args.conversation_id) {
    state.selectedSessionId = args.conversation_id;
  }
  if (!state.hasLoadedOnce) {
    render();
  }
};

app.ontoolresult = (result) => {
  receivedInitialResult = true;
  if (initialFallbackTimer !== null) {
    clearTimeout(initialFallbackTimer);
    initialFallbackTimer = null;
  }
  const parsed = parseToolResult(result);
  if (parsed.error) {
    state.hasLoadedOnce = true;
    state.error = parsed.error;
    render();
    return;
  }
  if (parsed.payload) {
    applyPayload(parsed.payload, true);
  }
};

app.ontoolcancelled = () => {
  receivedInitialResult = true;
  if (initialFallbackTimer !== null) {
    clearTimeout(initialFallbackTimer);
    initialFallbackTimer = null;
  }
  state.inFlight = false;
  state.manualLoading = false;
  if (!state.hasLoadedOnce) {
    state.hasLoadedOnce = true;
    state.error = { kind: "cancelled" };
  }
  render();
};

app.onhostcontextchanged = (ctx) => {
  applyHostContext(ctx);
};

async function fetchSnapshot(mode: FetchMode): Promise<void> {
  receivedInitialResult = true;
  if (initialFallbackTimer !== null) {
    clearTimeout(initialFallbackTimer);
    initialFallbackTimer = null;
  }

  if (state.inFlight) {
    // Background ticks skip cleanly when a request is already in flight
    if (mode === "poll") return;
    // User-triggered switch/manual refresh supersedes queued work
    pendingFetchMode = mode;
    return;
  }

  state.inFlight = true;
  state.manualLoading = mode !== "poll";
  pendingFetchMode = null;
  const reqId = ++requestCounter;
  if (state.manualLoading) {
    updateToolbarLoadingState();
  }

  try {
    const args: Record<string, unknown> = {};
    if (state.selectedSessionId) {
      args.conversation_id = state.selectedSessionId;
    }
    if (mode === "poll" && state.lastRevision && state.payload) {
      args.last_revision = state.lastRevision;
    }
    if (mode === "manual") {
      args.force_rescan = true;
    }

    const result = await app.callServerTool({
      name: "get_context_snapshot",
      arguments: args,
    });
    if (reqId !== requestCounter) return;

    const parsed = parseToolResult(result);
    if (parsed.error) {
      state.hasLoadedOnce = true;
      state.error = parsed.error;
      render();
    } else if (parsed.payload) {
      applyPayload(parsed.payload, false);
    }
  } catch (err) {
    if (reqId !== requestCounter) return;
    state.hasLoadedOnce = true;
    state.error = {
      kind: "update",
      detail: err instanceof Error ? err.message : undefined,
    };
    render();
  } finally {
    state.inFlight = false;
    const wasManual = state.manualLoading;
    state.manualLoading = false;
    if (wasManual) {
      updateToolbarLoadingState();
    }
    if (pendingFetchMode !== null) {
      const nextMode = pendingFetchMode;
      pendingFetchMode = null;
      void fetchSnapshot(nextMode);
    }
  }
}

// --- Navigation helpers ---

function isViewingSubagent(): boolean {
  return (
    state.navStack.length > 0 ||
    Boolean(state.payload?.parent_conversation_id)
  );
}

function getActiveSubagentLabel(t: Strings): string | null {
  const top = state.navStack[state.navStack.length - 1];
  if (
    top &&
    top.subagentId === (state.selectedSessionId || state.payload?.conversation_id)
  ) {
    return (
      top.subagentRole ||
      top.subagentType ||
      t.subagentFallback(top.subagentId.slice(0, 8))
    );
  }
  if (state.payload?.parent_conversation_id) {
    const id = state.payload.conversation_id;
    const match = (state.payload.sessions ?? []).find((s) => s.id === id);
    if (match?.title) return match.title;
    return t.subagentFallback(id.slice(0, 8));
  }
  return null;
}

function navigateIntoSubagent(sub: SubagentInfo): void {
  const currentConvId =
    state.payload?.conversation_id || state.selectedSessionId;
  state.navStack.push({
    parentId: currentConvId,
    parentWasAuto: state.selectedSessionId === "",
    subagentId: sub.id,
    subagentRole: sub.role || "",
    subagentType: sub.type_name || "",
  });
  state.selectedSessionId = sub.id;
  state.lastRevision = null;
  state.subagentsExpanded = false;
  state.viewMode = "main";
  render();
  void fetchSnapshot("switch");
}

function navigateBackToParent(): void {
  const popped = state.navStack.pop();
  if (popped) {
    state.selectedSessionId = popped.parentWasAuto ? "" : popped.parentId;
  } else if (state.payload?.parent_conversation_id) {
    state.selectedSessionId = state.payload.parent_conversation_id;
  } else {
    state.selectedSessionId = "";
  }
  state.lastRevision = null;
  state.subagentsExpanded = false;
  state.viewMode = "main";
  render();
  void fetchSnapshot("switch");
}

// --- Rendering: Toolbar ---

function renderToolbarHtml(t: Strings): string {
  const sessions = state.payload?.sessions ?? [];
  const selected = state.selectedSessionId;
  const inSubagent = isViewingSubagent();
  const subLabel = inSubagent ? getActiveSubagentLabel(t) : null;

  const sessionOptions: string[] = [
    `<option value=""${selected === "" ? " selected" : ""}>${esc(t.autoActive)}</option>`,
  ];

  let foundSelected = selected === "";
  for (const s of sessions) {
    const isSel = s.id === selected;
    if (isSel) foundSelected = true;
    if (isSel && inSubagent && subLabel) {
      sessionOptions.push(
        `<option value="${esc(s.id)}" selected>${esc(`↳ ${truncate(subLabel, 32)}`)}</option>`
      );
      continue;
    }
    const prefix = s.is_subagent ? "↳ " : s.is_active ? "● " : "";
    const title = truncate(s.title || s.id.slice(0, 8), 34);
    const time = fmtSessionTime(s.updated_at, state.locale);
    const label = time ? `${prefix}${title} · ${time}` : `${prefix}${title}`;
    sessionOptions.push(
      `<option value="${esc(s.id)}"${isSel ? " selected" : ""}>${esc(label)}</option>`
    );
  }

  if (!foundSelected && selected) {
    const fallbackLabel = subLabel
      ? `↳ ${truncate(subLabel, 32)}`
      : `↳ #${selected.slice(0, 8)}`;
    sessionOptions.unshift(
      `<option value="${esc(selected)}" selected>${esc(fallbackLabel)}</option>`
    );
  }

  const autoOptions: Array<{ val: AutoRefreshSec; label: string }> = [
    { val: 1, label: t.autoSec(1) },
    { val: 5, label: t.autoSec(5) },
    { val: 15, label: t.autoSec(15) },
    { val: 0, label: t.autoOff },
  ];

  const autoHtml = autoOptions
    .map(
      (o) =>
        `<option value="${o.val}"${state.autoRefreshSec === o.val ? " selected" : ""}>${esc(o.label)}</option>`
    )
    .join("");

  const backBtnHtml = inSubagent
    ? `<button id="cm-back-parent" type="button" class="btn-back-parent" title="${esc(t.backToChatTitle)}">${esc(t.backToChat)}</button>`
    : "";

  return `
    <div class="toolbar" role="region" aria-label="${esc(t.toolbarAria)}">
      ${backBtnHtml}
      <div class="session-select-wrap">
        <select id="cm-session" class="control-select${inSubagent ? " is-subagent" : ""}" aria-label="${esc(t.sessionSelectAria)}">
          ${sessionOptions.join("")}
        </select>
      </div>
      <div class="toolbar-actions">
        <select id="cm-auto" class="control-select auto-select" aria-label="${esc(t.autoRefreshAria)}" title="${esc(t.autoRefreshAria)}">
          ${autoHtml}
        </select>
        <button id="cm-refresh" type="button" class="btn-icon-refresh${state.manualLoading ? " is-loading" : ""}"${state.manualLoading ? " disabled" : ""} title="${esc(t.refreshTitle)}" aria-label="${esc(t.refreshAria)}">
          <svg class="icon-refresh" viewBox="0 0 16 16" fill="none" aria-hidden="true">
            <path d="M13.5 8a5.5 5.5 0 1 1-1.35-3.6M13.5 2.75v2.75h-2.75" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/>
          </svg>
        </button>
      </div>
    </div>
  `;
}

// --- Rendering: Loading & Empty States ---

function renderLoadingHtml(): string {
  return `
    <div class="card">
      <div class="skeleton-block" style="height: 20px; width: 62%; margin-bottom: 8px;"></div>
      <div class="skeleton-block" style="height: 12px; width: 48%; margin-bottom: 10px;"></div>
      <div class="skeleton-block" style="height: 10px; width: 100%; border-radius: 999px; margin-bottom: 10px;"></div>
      <div class="skeleton-block" style="height: 26px; width: 100%;"></div>
    </div>
    <div class="card">
      <div class="skeleton-block" style="height: 14px; width: 36%; margin-bottom: 8px;"></div>
      <div class="skeleton-block" style="height: 36px; width: 100%;"></div>
    </div>
    <div class="card">
      <div class="skeleton-block" style="height: 14px; width: 28%; margin-bottom: 8px;"></div>
      <div class="skeleton-block" style="height: 44px; width: 100%;"></div>
    </div>
  `;
}

function renderEmptyHtml(t: Strings): string {
  return `
    <div class="card empty-card">
      <svg class="empty-icon" viewBox="0 0 36 36" fill="none" aria-hidden="true">
        <circle cx="18" cy="18" r="13" stroke="currentColor" stroke-width="2" stroke-dasharray="4 3"/>
        <path d="M12 22l4-4 3 2 5-6" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>
      </svg>
      <div class="empty-title">${esc(t.emptyTitle)}</div>
      <div class="empty-desc">${esc(t.emptyDesc)}</div>
      <button id="cm-empty-refresh" type="button" class="btn-subtle">${esc(t.refreshBtn)}</button>
    </div>
  `;
}

// --- Rendering: Screen A, Block 1 (Unified Context Window Card) ---

function renderWindowBlock(snap: ContextSnapshot | null, t: Strings): string {
  if (!snap) {
    return `
      <section class="card window-card" aria-label="${esc(t.windowTitle)}">
        <div class="block-head">
          <span class="block-title">${esc(t.windowTitle)}</span>
        </div>
        <div class="empty-inline">${esc(t.waitingFirstStep)}</div>
      </section>
    `;
  }

  const status: ContextStatus =
    snap.status === "warn" || snap.status === "high" ? snap.status : "ok";
  const statusLabel = t.statusLabel(status);
  const compactions = Math.max(0, snap.compactions || 0);

  const compactionChip =
    compactions > 0
      ? `<span class="compaction-chip num" title="${esc(t.compactionChipTitle(compactions, fmtTokens(snap.peak_tokens)))}">⚡ ${compactions}×</span>`
      : "";

  const byId = new Map<string, BreakdownItem>();
  for (const item of snap.breakdown ?? []) {
    byId.set(item.id, item);
  }

  const ordered = BREAKDOWN_ORDER.map((id) => {
    const found = byId.get(id);
    return {
      id,
      label: t.breakdownLabel(id, compactions),
      tokens: found ? Math.max(0, found.tokens) : 0,
    };
  });

  const breakdownSum = ordered.reduce((acc, item) => acc + item.tokens, 0);
  const windowTokens = Math.max(1, snap.window_tokens || 1);
  const denom =
    state.barMode === "window"
      ? windowTokens
      : Math.max(1, breakdownSum || snap.current_tokens || 1);

  const segmentsHtml = ordered
    .map((item) => {
      const pct = Math.min(100, Math.max(0, (item.tokens / denom) * 100));
      const shareOfActive =
        breakdownSum > 0 ? (item.tokens / breakdownSum) * 100 : 0;
      const tip = t.segmentTip(
        item.label,
        fmtTokens(item.tokens),
        fmtPct(shareOfActive)
      );
      return `<div class="progress-seg seg-${item.id}" style="width: ${pct.toFixed(3)}%;" title="${esc(tip)}"></div>`;
    })
    .join("");

  const showPeakMarker =
    state.barMode === "window" &&
    snap.peak_tokens > snap.current_tokens &&
    snap.window_tokens > 0;
  const peakLeftPct = showPeakMarker
    ? Math.min(99.2, Math.max(1.2, (snap.peak_tokens / windowTokens) * 100))
    : 0;
  const peakTip = t.peakTip(
    fmtTokens(snap.peak_tokens),
    fmtPct((snap.peak_tokens / windowTokens) * 100)
  );
  const peakMarkerHtml = showPeakMarker
    ? `<div class="peak-marker" style="left: ${peakLeftPct.toFixed(2)}%;" title="${esc(peakTip)}"></div>`
    : "";

  const legendHtml = ordered
    .map((item) => {
      const pctBase =
        state.barMode === "window"
          ? windowTokens
          : Math.max(1, breakdownSum || 1);
      const pct = (item.tokens / pctBase) * 100;
      return `
        <div class="legend-item">
          <span class="legend-dot seg-${item.id}"></span>
          <div class="legend-text">
            <span class="legend-label" title="${esc(item.label)}">${esc(item.label)}</span>
            <span class="legend-val num">${fmtTokens(item.tokens)} <span class="legend-pct">(${fmtPct(pct)}%)</span></span>
          </div>
        </div>
      `;
    })
    .join("");

  const modelText = snap.model || t.unknownModel;
  const adviceClass =
    status === "high"
      ? "advice-inline is-high"
      : status === "warn"
        ? "advice-inline is-warn"
        : "advice-inline";
  const adviceText = t.advice(status, compactions);
  const adviceHtml = `<span class="meta-sep" aria-hidden="true">·</span><span class="${adviceClass}">${esc(adviceText)}</span>`;

  const cacheTitle = t.cacheTitle(
    fmtPct(snap.cumulative.cache_hit_pct),
    fmtTokens(snap.cumulative.cache_read),
    fmtTokens(snap.cumulative.prompt_total),
    fmtPct(snap.last_turn.cache_hit_pct)
  );
  const activityLabel = `${pluralCalls(snap.counts.tool_calls, state.locale)} · ${pluralSteps(snap.counts.llm_steps, state.locale)}`;

  return `
    <section class="card window-card" aria-label="${esc(t.windowAria)}">
      <div class="window-top">
        <div class="window-hero">
          <div class="window-metric num">
            <span class="window-current">${fmtTokens(snap.current_tokens)}</span>
            <span class="window-slash">/</span>
            <span class="window-limit">${fmtTokens(snap.window_tokens)}</span>
          </div>
          <span class="status-badge status-${status} num">${fmtPct(snap.fill_pct)}% · ${esc(statusLabel)}</span>
          ${compactionChip}
        </div>
        <div class="seg-toggle" role="group" aria-label="${esc(t.barScaleAria)}">
          <button type="button" class="seg-btn${state.barMode === "window" ? " is-active" : ""}" data-bar-mode="window" aria-pressed="${state.barMode === "window"}">${esc(t.barWindow)}</button>
          <button type="button" class="seg-btn${state.barMode === "composition" ? " is-active" : ""}" data-bar-mode="composition" aria-pressed="${state.barMode === "composition"}">${esc(t.barBreakdown)}</button>
        </div>
      </div>

      <div class="window-subrow">
        <span class="model-label" title="${esc(modelText)}">${esc(modelText)}</span>
        ${adviceHtml}
      </div>

      <div class="bar-stage">
        <div class="progress-track" role="img" aria-label="${esc(t.progressTrackAria(fmtPct(snap.fill_pct)))}">
          ${segmentsHtml}
        </div>
        ${peakMarkerHtml}
      </div>

      <div class="legend-grid">
        ${legendHtml}
      </div>

      <div class="window-bottom">
        <div class="cache-summary num" title="${esc(cacheTitle)}">
          <span class="cache-k">${esc(t.cacheLabel)}</span>
          <span class="cache-v">${fmtPct(snap.cumulative.cache_hit_pct)}%</span>
          <span class="cache-meta">${esc(t.cacheMeta(fmtPctInt(snap.last_turn.cache_hit_pct), fmtTokens(snap.cumulative.cache_read)))}</span>
        </div>
        <button id="cm-open-activity" type="button" class="activity-link num" title="${esc(t.activityBtnTitle)}">
          <span class="activity-link-text">${esc(t.activityPrefix)} ${esc(activityLabel)} · <strong>${esc(t.graphWord)}</strong></span>
          <span class="activity-chevron" aria-hidden="true">›</span>
        </button>
      </div>
    </section>
  `;
}

// --- Rendering: Screen A, Block 2 (Subagents) ---

function renderSubagentStatusIcon(status: SubagentStatus, t: Strings): string {
  const label = t.subagentStatusLabel(status);
  switch (status) {
    case "running":
      return `<span class="sub-status-dot is-running" title="${esc(label)}" aria-label="${esc(label)}"></span>`;
    case "done":
      return `<span class="sub-status-icon is-done" title="${esc(label)}" aria-label="${esc(label)}">✓</span>`;
    case "idle":
      return `<span class="sub-status-dot is-idle" title="${esc(label)}" aria-label="${esc(label)}"></span>`;
    default:
      return `<span class="sub-status-sq is-stopped" title="${esc(label)}" aria-label="${esc(label)}"></span>`;
  }
}

function renderSubagentsBlock(subagents: SubagentInfo[], t: Strings): string {
  const running = subagents.filter((s) => s.status === "running");
  const others = subagents.filter((s) => s.status !== "running");
  const ordered = [...running, ...others];

  const badgeHtml =
    running.length > 0
      ? `<span class="pill-running num"><span class="pill-pulse-dot" aria-hidden="true"></span>${esc(t.subagentsRunning(running.length))}</span>${
          ordered.length > running.length
            ? `<span class="block-count-muted num">${esc(t.subagentsTotal(ordered.length))}</span>`
            : ""
        }`
      : ordered.length > 0
        ? `<span class="block-count num">${ordered.length}</span>`
        : "";

  if (ordered.length === 0) {
    return `
      <section class="card" aria-label="${esc(t.subagentsTitle)}">
        <div class="block-head">
          <div class="block-title-row">
            <span class="block-title">${esc(t.subagentsTitle)}</span>
          </div>
        </div>
        <div class="empty-inline">${esc(t.subagentsEmpty)}</div>
      </section>
    `;
  }

  const canCollapse = ordered.length > 5 && others.length > 4;
  const visible =
    canCollapse && !state.subagentsExpanded
      ? [...running, ...others.slice(0, 4)]
      : ordered;

  const rowsHtml = visible
    .map((sub) => {
      const role =
        sub.role || sub.type_name || t.subagentFallback(sub.id.slice(0, 8));
      const typeBadge = sub.type_name
        ? `<span class="tag-chip">${esc(sub.type_name)}</span>`
        : "";
      const modelChip = sub.model
        ? `<span class="sub-model" title="${esc(sub.model)}">${esc(truncate(sub.model, 24))}</span>`
        : "";
      const runningTag =
        sub.status === "running"
          ? `<span class="sub-running-tag">${esc(t.subagentStatusLabel("running"))}</span>`
          : "";

      const trimmedFocus = (sub.focus ?? "").trim();
      const trimmedTool = (sub.last_tool ?? "").trim();
      let focusHtml = "";
      if (trimmedFocus) {
        focusHtml = `<div class="sub-focus" title="${esc(trimmedFocus)}">${esc(trimmedFocus)}</div>`;
      } else if (trimmedTool) {
        const fullText = t.subagentRunningToolText(trimmedTool);
        focusHtml = `<div class="sub-focus" title="${esc(fullText)}">${esc(t.subagentRunningPrefix)} <span class="mono">${esc(trimmedTool)}</span></div>`;
      }

      const logBtn = sub.log_uri
        ? `<button type="button" class="btn-mini-link" data-open-uri="${esc(sub.log_uri)}" title="${esc(t.logBtnTitle)}">${esc(t.logBtn)}</button>`
        : "";

      return `
        <div class="sub-row${sub.status === "running" ? " is-running" : ""}" role="button" tabindex="0" data-subagent-id="${esc(sub.id)}" title="${esc(t.subagentRowTitle)}">
          <div class="sub-status-cell">${renderSubagentStatusIcon(sub.status, t)}</div>
          <div class="sub-main">
            <div class="sub-line1">
              <span class="sub-role" title="${esc(role)}">${esc(role)}</span>
              ${runningTag}
              ${typeBadge}
              ${modelChip}
            </div>
            ${focusHtml}
          </div>
          <div class="sub-right">
            <span class="sub-ctx num" title="${esc(t.subagentCtxTitle)}">${fmtTokens(sub.current_tokens)} · ${fmtPct(sub.fill_pct)}%</span>
            ${logBtn}
          </div>
        </div>
      `;
    })
    .join("");

  const toggleHtml = canCollapse
    ? `<button id="cm-toggle-subagents" type="button" class="btn-expand-list">${esc(
        state.subagentsExpanded ? t.collapse : t.showAll(ordered.length)
      )}</button>`
    : "";

  return `
    <section class="card" aria-label="${esc(t.subagentsTitle)}">
      <div class="block-head">
        <div class="block-title-row">
          <span class="block-title">${esc(t.subagentsTitle)}</span>
          ${badgeHtml}
        </div>
      </div>
      <div class="sub-list">
        ${rowsHtml}
      </div>
      ${toggleHtml}
    </section>
  `;
}

// --- Rendering: Screen A, Block 3 (Plan) ---

function renderPlanStepIcon(status: PlanStepStatus, t: Strings): string {
  const aria = t.planStepAria(status);
  switch (status) {
    case "done":
      return `<span class="step-icon is-done" aria-label="${esc(aria)}">✓</span>`;
    case "in_progress":
      return `<span class="step-icon is-progress" aria-label="${esc(aria)}"><span class="step-progress-dot"></span></span>`;
    case "failed":
      return `<span class="step-icon is-failed" aria-label="${esc(aria)}">✕</span>`;
    case "skipped":
      return `<span class="step-icon is-skipped" aria-label="${esc(aria)}">–</span>`;
    default:
      return `<span class="step-icon is-pending" aria-label="${esc(aria)}">○</span>`;
  }
}

function renderPlanBlock(plan: PlanInfo | undefined, t: Strings): string {
  const steps = plan?.steps ?? [];
  const focus = (plan?.focus ?? "").trim();
  const artifactUri = plan?.artifact_uri ?? "";
  const totalCount = plan?.total_count ?? steps.length;
  const doneCount =
    plan?.done_count ?? steps.filter((s) => s.status === "done").length;

  const isAllDone = totalCount > 0 && doneCount >= totalCount;
  const countBadge =
    totalCount > 0
      ? `<span class="block-count num${isAllDone ? " is-done" : ""}">${doneCount} / ${totalCount}${isAllDone ? ` · ${esc(t.planCompleted)}` : ""}</span>`
      : "";

  const artifactBtn = artifactUri
    ? `<button type="button" class="btn-mini-link" data-open-uri="${esc(artifactUri)}" title="${esc(t.fileBtnTitle)}">${esc(t.fileBtn)}</button>`
    : "";

  const hasInProgress = steps.some((s) => s.status === "in_progress");

  const focusHtml = focus
    ? `
      <div class="plan-focus">
        ${hasInProgress ? `<span class="focus-dot" aria-hidden="true"></span>` : ""}
        <span class="plan-focus-text"><span class="plan-focus-prefix">${esc(t.planNow)}</span> <em>${esc(focus)}</em></span>
      </div>
    `
    : "";

  let bodyHtml = "";
  if (steps.length === 0 && !focus) {
    bodyHtml = `<div class="empty-inline">${esc(t.planEmpty)}</div>`;
  } else {
    const stepsHtml =
      steps.length > 0
        ? `
          <ul class="plan-steps">
            ${steps
              .map(
                (step) => `
                  <li class="plan-step step-${esc(step.status)}">
                    ${renderPlanStepIcon(step.status, t)}
                    <span class="step-title">${esc(step.title)}</span>
                  </li>
                `
              )
              .join("")}
          </ul>
        `
        : "";
    bodyHtml = `${focusHtml}${stepsHtml}`;
  }

  return `
    <section class="card" aria-label="${esc(t.planAria)}">
      <div class="block-head">
        <div class="block-title-row">
          <span class="block-title">${esc(t.planTitle)}</span>
          ${countBadge}
        </div>
        ${artifactBtn}
      </div>
      ${bodyHtml}
    </section>
  `;
}

// --- Rendering: Screen B (Activity & Chart Detail View) ---

interface ChartGeometry {
  pts: TimelinePoint[];
  xs: number[];
  ys: number[];
  padL: number;
  padR: number;
  padT: number;
  plotW: number;
  plotH: number;
  yMax: number;
}

let activeChartGeom: ChartGeometry | null = null;

function buildChartGeometry(snap: ContextSnapshot): ChartGeometry | null {
  const pts = snap.timeline ?? [];
  if (pts.length === 0) return null;

  const padL = 46;
  const padR = 14;
  const padT = 24;
  const padB = 22;
  const width = 640;
  const height = 156;
  const plotW = width - padL - padR;
  const plotH = height - padT - padB;

  const maxTotal = pts.reduce((m, p) => Math.max(m, p.total || 0), 0);
  const yMax = Math.max(maxTotal * 1.16, 1000);

  const xs: number[] = [];
  const ys: number[] = [];
  for (let i = 0; i < pts.length; i++) {
    const x =
      pts.length === 1
        ? padL + plotW / 2
        : padL + (i / (pts.length - 1)) * plotW;
    const y = padT + plotH - (Math.max(0, pts[i].total) / yMax) * plotH;
    xs.push(x);
    ys.push(y);
  }

  return { pts, xs, ys, padL, padR, padT, plotW, plotH, yMax };
}

function renderChartCard(snap: ContextSnapshot, t: Strings): string {
  const geom = buildChartGeometry(snap);
  activeChartGeom = geom;

  if (!geom) {
    return `
      <div class="card">
        <div class="block-head">
          <span class="block-title">${esc(t.chartTitle)}</span>
        </div>
        <div class="chart-empty">${esc(t.chartEmpty)}</div>
      </div>
    `;
  }

  const { pts, xs, ys, padL, padT, plotW, plotH, yMax } = geom;
  const bottomY = padT + plotH;

  const gridTicks = [0, 0.5, 1];
  const gridSvg = gridTicks
    .map((ratio) => {
      const y = bottomY - ratio * plotH;
      const val = yMax * ratio;
      return `
        <line class="chart-grid" x1="${padL}" y1="${y.toFixed(1)}" x2="${padL + plotW}" y2="${y.toFixed(1)}"/>
        <text class="chart-axis-text" x="${padL - 6}" y="${(y + 3.5).toFixed(1)}" text-anchor="end">${esc(val === 0 ? "0" : fmtTokens(val))}</text>
      `;
    })
    .join("");

  let linePath = "";
  let areaPath = "";
  if (pts.length === 1) {
    linePath = `M ${padL} ${ys[0].toFixed(1)} L ${padL + plotW} ${ys[0].toFixed(1)}`;
    areaPath = `${linePath} L ${padL + plotW} ${bottomY} L ${padL} ${bottomY} Z`;
  } else {
    const coords = pts.map((_, i) => `${xs[i].toFixed(1)} ${ys[i].toFixed(1)}`);
    linePath = `M ${coords.join(" L ")}`;
    areaPath = `${linePath} L ${xs[pts.length - 1].toFixed(1)} ${bottomY} L ${xs[0].toFixed(1)} ${bottomY} Z`;
  }

  let lastLabelRight = -Infinity;
  const compactionSvg = pts
    .map((pt, i) => {
      if (!pt.compacted_before) return "";
      const x = xs[i];
      const before = i > 0 ? pts[i - 1].total : snap.peak_tokens;
      const after = pt.total;
      const text =
        before > after
          ? t.chartCompactionRange(fmtTokens(before), fmtTokens(after))
          : t.chartCompactionTo(fmtTokens(after));
      const approxW = Math.min(224, Math.max(98, text.length * 6.6 + 16));
      let labelX = x - approxW / 2;
      labelX = Math.max(padL, Math.min(640 - geom.padR - approxW, labelX));
      let labelY = 3;
      if (labelX < lastLabelRight + 4) {
        labelY = 19;
      }
      lastLabelRight = labelX + approxW;
      return `
        <g class="chart-compaction">
          <line class="chart-compaction-line" x1="${x.toFixed(1)}" y1="${padT}" x2="${x.toFixed(1)}" y2="${bottomY}"/>
          <rect class="chart-compaction-pill" x="${labelX.toFixed(1)}" y="${labelY}" width="${approxW.toFixed(1)}" height="15" rx="4"/>
          <text class="chart-compaction-text" x="${(labelX + approxW / 2).toFixed(1)}" y="${labelY + 11}" text-anchor="middle">${esc(text)}</text>
        </g>
      `;
    })
    .join("");

  const xLabelIndices =
    pts.length === 1
      ? [0]
      : pts.length === 2
        ? [0, 1]
        : [0, Math.floor((pts.length - 1) / 2), pts.length - 1];

  const xLabelsSvg = xLabelIndices
    .map((idx, pos) => {
      const anchor =
        xLabelIndices.length === 1
          ? "middle"
          : pos === 0
            ? "start"
            : pos === xLabelIndices.length - 1
              ? "end"
              : "middle";
      return `<text class="chart-axis-text" x="${xs[idx].toFixed(1)}" y="${bottomY + 15}" text-anchor="${anchor}">${esc(t.chartAxisStep(pts[idx].step))}</text>`;
    })
    .join("");

  const lastPt = pts[pts.length - 1];
  const defaultReadout = t.chartDefaultReadout(pts.length, fmtTokens(lastPt.total));

  return `
    <div class="card">
      <div class="block-head">
        <span class="block-title">${esc(t.chartTitle)}</span>
        <span id="cm-chart-readout" class="chart-readout num" data-default="${esc(defaultReadout)}">${esc(defaultReadout)}</span>
      </div>
      <div id="cm-chart-wrap" class="chart-wrap">
        <svg id="cm-chart-svg" class="chart-svg" viewBox="0 0 640 156" role="img" aria-label="${esc(t.chartAria)}">
          <defs>
            <linearGradient id="cm-area-grad" x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stop-color="var(--cm-line)" stop-opacity="0.28"/>
              <stop offset="100%" stop-color="var(--cm-line)" stop-opacity="0.02"/>
            </linearGradient>
          </defs>
          ${gridSvg}
          <path d="${areaPath}" fill="url(#cm-area-grad)"/>
          <path class="chart-line" d="${linePath}"/>
          ${compactionSvg}
          ${xLabelsSvg}
          <g id="cm-chart-hover" style="display: none;">
            <line id="cm-hover-line" class="chart-hover-line" x1="0" y1="${padT}" x2="0" y2="${bottomY}"/>
            <circle id="cm-hover-halo" class="chart-hover-halo" cx="0" cy="0" r="8"/>
            <circle id="cm-hover-dot" class="chart-hover-dot" cx="0" cy="0" r="4"/>
          </g>
          <rect id="cm-chart-hit" x="${padL}" y="${padT}" width="${plotW}" height="${plotH}" fill="transparent" style="cursor: crosshair;"/>
        </svg>
        <div id="cm-chart-tooltip" class="chart-tooltip num" aria-hidden="true"></div>
      </div>
    </div>
  `;
}

function renderActivityScreenHtml(payload: SnapshotPayload, t: Strings): string {
  const snap = payload.snapshot;
  const logBtn = payload.log_uri
    ? `<button type="button" class="btn-mini-link" data-open-uri="${esc(payload.log_uri)}" title="${esc(t.openTranscriptTitle)}">${esc(t.logBtn)}</button>`
    : "";

  const headerHtml = `
    <div class="detail-head">
      <button id="cm-back-main" type="button" class="btn-subtle">${esc(t.backBtn)}</button>
      <span class="detail-title">${esc(t.activityScreenTitle)}</span>
      ${logBtn}
    </div>
  `;

  if (!snap) {
    activeChartGeom = null;
    return `${headerHtml}${renderEmptyHtml(t)}`;
  }

  const dur = fmtDuration(snap.counts.duration_min, state.locale);
  const actMetaParts = [
    t.userTurnsShort(snap.counts.user_turns),
    fmtSize(snap.counts.transcript_kb, state.locale),
  ];
  if (dur) actMetaParts.unshift(dur);

  const compactionsTag =
    snap.compactions > 0
      ? `<span class="compaction-chip num">⚡ ${snap.compactions}×</span>`
      : "";

  const tilesHtml = `
    <div class="detail-tiles">
      <div class="detail-tile">
        <span class="detail-tile-label">${esc(t.tilePeakFree)}</span>
        <div class="detail-tile-main-row">
          <span class="detail-tile-main num">${fmtTokens(snap.peak_tokens)}</span>
          ${compactionsTag}
        </div>
        <span class="detail-tile-sub num">${esc(t.tilePeakSub(fmtTokens(snap.free_tokens), snap.compactions))}</span>
      </div>
      <div class="detail-tile">
        <span class="detail-tile-label">${esc(t.tilePromptCache)}</span>
        <span class="detail-tile-main num">${fmtPct(snap.cumulative.cache_hit_pct)}%</span>
        <span class="detail-tile-sub num">${esc(t.tileCacheSub(fmtPct(snap.last_turn.cache_hit_pct), fmtTokens(snap.cumulative.cache_read), fmtTokens(snap.cumulative.prompt_total)))}</span>
      </div>
      <div class="detail-tile">
        <span class="detail-tile-label">${esc(t.tileActivity)}</span>
        <span class="detail-tile-main num">${snap.counts.llm_steps} <span class="detail-tile-unit">${esc(t.tileStepsUnit)}</span> · ${snap.counts.tool_calls} <span class="detail-tile-unit">${esc(t.tileCallsUnit)}</span></span>
        <span class="detail-tile-sub num">${esc(actMetaParts.join(" · "))}</span>
      </div>
    </div>
  `;

  return `
    ${headerHtml}
    ${tilesHtml}
    ${renderChartCard(snap, t)}
  `;
}

// --- Rendering: Screen A (Main 3-Block Dashboard) ---

function renderMainScreenHtml(payload: SnapshotPayload, t: Strings): string {
  activeChartGeom = null;
  const snap = payload.snapshot ?? null;
  const subagents = payload.subagents ?? [];
  const plan = payload.plan;

  const hasPlanData =
    Boolean(plan?.focus?.trim()) || (plan?.steps?.length ?? 0) > 0;

  if (!snap && subagents.length === 0 && !hasPlanData) {
    return renderEmptyHtml(t);
  }

  return `
    ${renderWindowBlock(snap, t)}
    ${renderSubagentsBlock(subagents, t)}
    ${renderPlanBlock(plan, t)}
  `;
}

// --- Rendering: Footer ---

function renderLangToggleHtml(t: Strings): string {
  return `<button id="cm-lang-toggle" type="button" class="lang-toggle" title="${esc(t.langToggleTitle)}" aria-label="${esc(t.langToggleAria)}"><span class="lang-opt${state.locale === "en" ? " is-active" : ""}">EN</span><span class="lang-sep" aria-hidden="true">/</span><span class="lang-opt${state.locale === "ru" ? " is-active" : ""}">RU</span></button>`;
}

function renderFooterHtml(payload: SnapshotPayload | null, t: Strings): string {
  const langBtn = renderLangToggleHtml(t);

  if (!payload) {
    return `
      <footer class="footer">
        <div class="footer-left"></div>
        <div class="footer-right">${langBtn}</div>
      </footer>
    `;
  }

  const checkedTime = fmtClock(state.lastCheckedAt || payload.updated_at);
  const fromCache = state.lastFromCache;
  const sourceText = fromCache ? t.sourceCache : t.sourceUpdated;
  const sourceCls = fromCache ? "is-cache" : "is-fresh";
  const shortId = payload.conversation_id
    ? payload.conversation_id.slice(0, 8)
    : "—";
  const liveStyle = state.autoRefreshSec > 0 ? "" : ' style="display: none;"';
  const liveCls =
    state.autoRefreshSec === 1 ? "footer-live is-live" : "footer-live";
  const liveText =
    state.autoRefreshSec === 1
      ? t.live1s
      : state.autoRefreshSec > 1
        ? t.autoN(state.autoRefreshSec)
        : "";

  return `
    <footer class="footer">
      <div class="footer-left num">
        <span id="cm-footer-time">${esc(t.checkedAt(checkedTime))}</span>
        <span aria-hidden="true">·</span>
        <span id="cm-footer-source" class="footer-pill ${sourceCls}">${esc(sourceText)}</span>
      </div>
      <div class="footer-right num">
        <span id="cm-footer-id" class="session-id-chip mono" title="${esc(payload.conversation_id)}">#${esc(shortId)}</span>
        <span id="cm-footer-live" class="${liveCls}"${liveStyle}>${esc(liveText)}</span>
        ${langBtn}
      </div>
    </footer>
  `;
}

function updateToolbarLoadingState(): void {
  const btn = document.getElementById("cm-refresh") as HTMLButtonElement | null;
  if (!btn) return;
  btn.disabled = state.manualLoading;
  btn.classList.toggle("is-loading", state.manualLoading);
}

// --- Event Binding ---

function bindEvents(): void {
  const backParentBtn = document.getElementById("cm-back-parent");
  if (backParentBtn) {
    backParentBtn.addEventListener("click", () => {
      navigateBackToParent();
    });
  }

  const sessionSelect = document.getElementById(
    "cm-session"
  ) as HTMLSelectElement | null;
  if (sessionSelect) {
    sessionSelect.addEventListener("change", () => {
      state.selectedSessionId = sessionSelect.value;
      state.navStack = [];
      state.lastRevision = null;
      state.subagentsExpanded = false;
      state.viewMode = "main";
      void fetchSnapshot("switch");
    });
  }

  const refreshBtn = document.getElementById(
    "cm-refresh"
  ) as HTMLButtonElement | null;
  if (refreshBtn) {
    refreshBtn.addEventListener("click", () => {
      state.lastRevision = null;
      void fetchSnapshot("manual");
    });
  }

  const autoSelect = document.getElementById(
    "cm-auto"
  ) as HTMLSelectElement | null;
  if (autoSelect) {
    autoSelect.addEventListener("change", () => {
      const sec = Number(autoSelect.value);
      state.autoRefreshSec = sec === 1 || sec === 5 || sec === 15 ? sec : 0;
      syncAutoTimer();
      updateFooterInPlace();
    });
  }

  const retryBtn = document.getElementById("cm-retry");
  if (retryBtn) {
    retryBtn.addEventListener("click", () => {
      state.lastRevision = null;
      void fetchSnapshot("manual");
    });
  }

  const emptyRefreshBtn = document.getElementById("cm-empty-refresh");
  if (emptyRefreshBtn) {
    emptyRefreshBtn.addEventListener("click", () => {
      state.lastRevision = null;
      void fetchSnapshot("manual");
    });
  }

  const langBtn = document.getElementById("cm-lang-toggle");
  if (langBtn) {
    langBtn.addEventListener("click", () => {
      setLocale(state.locale === "en" ? "ru" : "en", true);
    });
  }

  const modeButtons = rootEl.querySelectorAll<HTMLButtonElement>(
    "[data-bar-mode]"
  );
  modeButtons.forEach((btn) => {
    btn.addEventListener("click", () => {
      const mode = btn.dataset.barMode;
      if (
        (mode === "window" || mode === "composition") &&
        mode !== state.barMode
      ) {
        state.barMode = mode;
        render();
      }
    });
  });

  const openActivityBtn = document.getElementById("cm-open-activity");
  if (openActivityBtn) {
    openActivityBtn.addEventListener("click", () => {
      state.viewMode = "activity";
      render();
      window.scrollTo(0, 0);
    });
  }

  const backMainBtn = document.getElementById("cm-back-main");
  if (backMainBtn) {
    backMainBtn.addEventListener("click", () => {
      state.viewMode = "main";
      render();
      window.scrollTo(0, 0);
    });
  }

  const toggleSubagentsBtn = document.getElementById("cm-toggle-subagents");
  if (toggleSubagentsBtn) {
    toggleSubagentsBtn.addEventListener("click", () => {
      state.subagentsExpanded = !state.subagentsExpanded;
      render();
    });
  }

  // Open file/log in Antigravity aux pane (stopPropagation so row click does not fire)
  const openUriButtons = rootEl.querySelectorAll<HTMLButtonElement>(
    "[data-open-uri]"
  );
  openUriButtons.forEach((btn) => {
    btn.addEventListener("click", (ev) => {
      ev.stopPropagation();
      const uri = btn.dataset.openUri;
      if (uri) void openInAuxPane(uri);
    });
  });

  // Subagent row click -> inspect subagent
  const subRows = rootEl.querySelectorAll<HTMLElement>("[data-subagent-id]");
  const subagents = state.payload?.subagents ?? [];
  const bySubId = new Map<string, SubagentInfo>();
  for (const s of subagents) bySubId.set(s.id, s);

  subRows.forEach((row) => {
    const activate = () => {
      const id = row.dataset.subagentId;
      if (!id) return;
      const sub = bySubId.get(id);
      if (sub) navigateIntoSubagent(sub);
    };
    row.addEventListener("click", activate);
    row.addEventListener("keydown", (ev) => {
      if (ev.key === "Enter" || ev.key === " ") {
        ev.preventDefault();
        activate();
      }
    });
  });

  bindChartHover();
}

function bindChartHover(): void {
  const geom = activeChartGeom;
  if (!geom) return;

  const wrap = document.getElementById("cm-chart-wrap");
  const svg = document.getElementById("cm-chart-svg") as SVGSVGElement | null;
  const hit = document.getElementById("cm-chart-hit");
  const hoverGroup = document.getElementById("cm-chart-hover");
  const hoverLine = document.getElementById("cm-hover-line");
  const hoverHalo = document.getElementById("cm-hover-halo");
  const hoverDot = document.getElementById("cm-hover-dot");
  const tooltip = document.getElementById("cm-chart-tooltip");
  const readout = document.getElementById("cm-chart-readout");

  if (
    !wrap ||
    !svg ||
    !hit ||
    !hoverGroup ||
    !hoverLine ||
    !hoverHalo ||
    !hoverDot ||
    !tooltip ||
    !readout
  ) {
    return;
  }

  const showPoint = (idx: number) => {
    const t = tr();
    const pt = geom.pts[idx];
    const x = geom.xs[idx];
    const y = geom.ys[idx];

    hoverGroup.style.display = "";
    hoverLine.setAttribute("x1", x.toFixed(1));
    hoverLine.setAttribute("x2", x.toFixed(1));
    hoverHalo.setAttribute("cx", x.toFixed(1));
    hoverHalo.setAttribute("cy", y.toFixed(1));
    hoverDot.setAttribute("cx", x.toFixed(1));
    hoverDot.setAttribute("cy", y.toFixed(1));

    readout.textContent = t.chartHoverReadout(
      pt.step,
      fmtTokens(pt.total),
      fmtTokens(pt.cache),
      fmtTokens(pt.new)
    );

    const compactedBadge = pt.compacted_before
      ? `<span class="chart-tooltip-tag">${esc(t.chartTooltipCompacted)}</span>`
      : "";
    tooltip.innerHTML = `
      <div class="chart-tooltip-head"><span>${esc(t.chartTooltipStep(pt.step))}</span>${compactedBadge}</div>
      <div class="chart-tooltip-grid">
        <span class="chart-tooltip-k">${esc(t.chartTooltipContext)}</span><span class="chart-tooltip-v">${fmtTokens(pt.total)}</span>
        <span class="chart-tooltip-k">${esc(t.chartTooltipCache)}</span><span class="chart-tooltip-v">${fmtTokens(pt.cache)}</span>
        <span class="chart-tooltip-k">${esc(t.chartTooltipNew)}</span><span class="chart-tooltip-v">${fmtTokens(pt.new)}</span>
        <span class="chart-tooltip-k">${esc(t.chartTooltipOut)}</span><span class="chart-tooltip-v">${fmtTokens(pt.out)}</span>
      </div>
    `;
    tooltip.classList.add("is-visible");

    const wrapRect = wrap.getBoundingClientRect();
    const tipWidth = tooltip.offsetWidth || 138;
    const anchorPx = (x / 640) * wrapRect.width;
    const gap = 10;
    let leftPx = anchorPx + gap;
    if (leftPx + tipWidth > wrapRect.width - 2) {
      leftPx = anchorPx - tipWidth - gap;
    }
    leftPx = Math.max(2, Math.min(wrapRect.width - tipWidth - 2, leftPx));
    tooltip.style.left = `${Math.round(leftPx)}px`;
  };

  const hidePoint = () => {
    hoverGroup.style.display = "none";
    tooltip.classList.remove("is-visible");
    readout.textContent = readout.dataset.default ?? "";
  };

  const handlePointer = (ev: PointerEvent) => {
    const rect = svg.getBoundingClientRect();
    if (rect.width <= 0) return;
    const svgX = ((ev.clientX - rect.left) / rect.width) * 640;
    let bestIdx = 0;
    let bestDist = Infinity;
    for (let i = 0; i < geom.xs.length; i++) {
      const d = Math.abs(geom.xs[i] - svgX);
      if (d < bestDist) {
        bestDist = d;
        bestIdx = i;
      }
    }
    showPoint(bestIdx);
  };

  hit.addEventListener("pointermove", handlePointer);
  hit.addEventListener("pointerdown", handlePointer);
  wrap.addEventListener("pointerleave", hidePoint);
}

// --- Master Render ---

function render(): void {
  const t = tr();
  const parts: string[] = [renderToolbarHtml(t)];

  if (state.error) {
    parts.push(`
      <div class="error-banner" role="alert">
        <span class="error-banner-text">${esc(formatError(state.error, t))}</span>
        <button id="cm-retry" type="button" class="btn-retry">${esc(t.retryBtn)}</button>
      </div>
    `);
  }

  if (!state.hasLoadedOnce) {
    parts.push(renderLoadingHtml());
  } else if (state.payload) {
    if (state.viewMode === "activity") {
      parts.push(renderActivityScreenHtml(state.payload, t));
    } else {
      parts.push(renderMainScreenHtml(state.payload, t));
    }
  } else if (!state.error) {
    parts.push(renderEmptyHtml(t));
  }

  parts.push(renderFooterHtml(state.payload, t));
  parts.push(
    `<div id="cm-toast" class="toast" role="status" aria-live="polite"></div>`
  );

  rootEl.innerHTML = `<div class="app-shell">${parts.join("")}</div>`;
  bindEvents();
}

// --- Bootstrap ---

async function init(): Promise<void> {
  render();
  try {
    await app.connect();
    applyHostContext(app.getHostContext());

    if (!receivedInitialResult) {
      initialFallbackTimer = setTimeout(() => {
        initialFallbackTimer = null;
        if (!receivedInitialResult && !state.inFlight) {
          void fetchSnapshot("initial");
        }
      }, 300);
    }
  } catch (err) {
    state.hasLoadedOnce = true;
    state.error = {
      kind: "init",
      detail: err instanceof Error ? err.message : undefined,
    };
    render();
  }
}

void init();
