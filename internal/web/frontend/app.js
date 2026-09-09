// maatplug-web's whole frontend: no build step, no dependencies. Routing is
// hash-based (#/, #/project/:id, #/project/:id/session/:provider/:sessionId)
// so this can be served as plain static files with no server-side routing.
const app = document.getElementById("app");

window.addEventListener("hashchange", render);
window.addEventListener("DOMContentLoaded", render);
window.addEventListener("DOMContentLoaded", startUsageBadge);

function route() {
  const hash = location.hash.replace(/^#\/?/, "");
  const parts = hash.split("/").filter(Boolean);
  if (parts.length === 0) return { view: "projects" };
  if (parts[0] === "project" && parts.length === 2) {
    return { view: "project", areaId: "", projectId: decodeURIComponent(parts[1]) };
  }
  if (parts[0] === "project" && parts.length === 3) {
    return { view: "project", areaId: decodeURIComponent(parts[1]), projectId: decodeURIComponent(parts[2]) };
  }
  if (parts[0] === "project" && parts.length === 5 && parts[2] === "session") {
    return {
      view: "session",
      areaId: "",
      projectId: decodeURIComponent(parts[1]),
      provider: decodeURIComponent(parts[3]),
      sessionId: decodeURIComponent(parts[4]),
    };
  }
  if (parts[0] === "project" && parts.length === 6 && parts[3] === "session") {
    return {
      view: "session",
      areaId: decodeURIComponent(parts[1]),
      projectId: decodeURIComponent(parts[2]),
      provider: decodeURIComponent(parts[4]),
      sessionId: decodeURIComponent(parts[5]),
    };
  }
  if (parts[0] === "trend" && parts.length === 1) {
    return { view: "trend", areaId: "", projectId: "" };
  }
  if (parts[0] === "project" && parts.length === 3 && parts[2] === "trend") {
    return { view: "trend", areaId: "", projectId: decodeURIComponent(parts[1]) };
  }
  if (parts[0] === "project" && parts.length === 4 && parts[3] === "trend") {
    return { view: "trend", areaId: decodeURIComponent(parts[1]), projectId: decodeURIComponent(parts[2]) };
  }
  if (parts[0] === "settings" && parts.length === 1) return { view: "settings" };
  return { view: "notfound" };
}

async function fetchJSON(url, options) {
  const res = await fetch(url, options);
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new Error(body.error || `${res.status} ${res.statusText}`);
  }
  return res.json();
}

async function render() {
  const r = route();
  try {
    if (r.view === "projects") return renderProjects();
    if (r.view === "project") return renderProject(r.areaId, r.projectId);
    if (r.view === "session") return renderSession(r.areaId, r.projectId, r.provider, r.sessionId);
    if (r.view === "trend") return renderTrendDetail(r.areaId, r.projectId);
    if (r.view === "settings") return renderSettings();
    app.innerHTML = `<p class="error">ページが見つかりません</p>`;
  } catch (err) {
    app.innerHTML = `<p class="error">読み込みに失敗しました: ${escapeHTML(err.message)}</p>`;
  }
}

async function renderProjects() {
  app.innerHTML = `<p class="loading">読み込み中…</p>`;

  const state = { query: "", sortKey: "costUsd", sortDir: "desc", provider: "", days: "", trend: [] };
  let allProjects;
  [allProjects, state.trend] = await Promise.all([
    fetchProjects(state.provider, state.days),
    fetchTrend(null, state.provider, state.days, null),
  ]);
  if (allProjects.length === 0 && state.provider === "" && state.days === "") {
    app.innerHTML = `<p class="empty">まだ記録されたプロジェクトがありません。Claude Codeでhookが登録されているか確認してください（<code>maatplug init</code>）。</p>`;
    return;
  }

  app.innerHTML = `
    <div class="toolbar">
      ${searchBoxHTML("project-search", "Search projects…")}
      <div class="toolbar-filters">
        ${providerSegmentHTML("project-provider")}
        ${dateRangeHTML("project-range")}
      </div>
    </div>
    <div id="results"></div>
  `;

  const results = document.getElementById("results");
  document.getElementById("project-search").addEventListener("input", (e) => {
    state.query = e.target.value;
    paint();
  });
  wireSortClicks(results, state, paint);
  wireProviderSegment(document.getElementById("project-provider"), state, reload);
  wireDateRange(document.getElementById("project-range"), state, reload);

  async function reload() {
    [allProjects, state.trend] = await Promise.all([
      fetchProjects(state.provider, state.days),
      fetchTrend(null, state.provider, state.days, null),
    ]);
    paint();
  }

  function paint() {
    const q = state.query.trim().toLowerCase();
    const filtered = q
      ? allProjects.filter((p) => (p.name || p.projectId).toLowerCase().includes(q))
      : allProjects.slice();
    filtered.sort(rowComparator(state.sortKey, state.sortDir, (row) => row.name || row.projectId));
    results.innerHTML = renderProjectsResults(filtered, state);
    wireSparklineTooltips(results);
  }

  paint();
}

// fetchProjects loads /api/projects, optionally restricted server-side to
// one provider and/or a rolling window of `days` (see
// internal/aggregate.Filter), and drops projects with no real instructions
// the same way every list view does.
async function fetchProjects(provider, days) {
  return (await fetchJSON(`/api/projects${filterQuery(provider, days)}`)).filter((p) => p.instructions > 0);
}

// filterQuery builds the ?provider=&days= query string shared by every
// list-view fetch — empty values are simply omitted rather than sent as
// "all"/"0", matching filterFromQuery on the server side.
function filterQuery(provider, days, areaId) {
  const params = new URLSearchParams();
  if (provider) params.set("provider", provider);
  if (days) params.set("days", days);
  if (areaId) params.set("area", areaId);
  const qs = params.toString();
  return qs ? `?${qs}` : "";
}

// fetchTrend loads the cost/token trend chart's daily series — either
// across every project (projectId null, GET /api/projects/trend) or scoped
// to one (GET /api/projects/{id}/trend) — for the stat cards' sparklines.
// Deliberately NOT re-fetched on every search keystroke (see renderProjects/
// renderProject's reload()): it only changes with provider/date-range, so
// re-running it per keystroke would just be wasted file reads for a chart
// that wouldn't visibly change.
async function fetchTrend(projectId, provider, days, areaId) {
  const path = projectId ? `/api/projects/${encodeURIComponent(projectId)}/trend` : "/api/projects/trend";
  return fetchJSON(`${path}${filterQuery(provider, days, areaId)}`);
}

function renderProjectsResults(list, state) {
  if (list.length === 0) {
    return `<p class="empty">一致するプロジェクトがありません。</p>`;
  }

  const rows = list.map((p) => `
    <tr>
      <td><a href="#/project/${encodeURIComponent(p.dataAreaId || "default")}/${encodeURIComponent(p.projectId)}">${escapeHTML(p.name || p.projectId)}</a> <span class="chip area-chip">${escapeHTML(p.dataAreaName || "Default")}</span></td>
      <td class="num">${p.sessions}</td>
      <td class="num">${p.instructions}</td>
      <td class="num">${tokenBreakdown(p)}</td>
      <td class="num">${costCell(p)}</td>
      <td class="num"><span class="added">+${p.linesAdded}</span> / <span class="removed">-${p.linesRemoved}</span></td>
      <td class="num">${p.abandonedInstructions > 0 ? `<span class="chip warn">${p.abandonedInstructions} unmeasured</span>` : ""}</td>
    </tr>
  `).join("");

  return `
    ${statsHTML(list, (p) => p.sessions, "project", state.trend, "#/trend")}
    <table>
      <thead>
        <tr>
          ${sortableHeader("Project", "name", state)}
          ${sortableHeader("Sessions", "sessions", state, true)}
          ${sortableHeader("Instructions", "instructions", state, true)}
          ${sortableHeader("Tokens (IN / Cache / OUT)", "totalTokens", state, true)}
          ${sortableHeader("Cost", "costUsd", state, true)}
          <th class="num">Lines</th>
          <th class="num"></th>
        </tr>
      </thead>
      <tbody>${rows}</tbody>
    </table>
  `;
}

async function renderProject(areaId, projectId) {
  app.innerHTML = `<p class="loading">読み込み中…</p>`;

  const state = { query: "", sortKey: "startedAt", sortDir: "desc", provider: "", days: "", trend: [] };
  let allSessions;
  [allSessions, state.trend] = await Promise.all([
    fetchSessions(areaId, projectId, state.provider, state.days),
    fetchTrend(projectId, state.provider, state.days, areaId),
  ]);

  const breadcrumb = `<p class="breadcrumb"><a href="#/">← Projects</a></p>`;

  if (allSessions.length === 0 && state.provider === "" && state.days === "") {
    app.innerHTML = `${breadcrumb}<p class="empty">このプロジェクトにはまだセッションが記録されていません。</p>`;
    return;
  }

  app.innerHTML = `
    ${breadcrumb}
    <div class="toolbar">
      ${searchBoxHTML("session-search", "Search instructions…")}
      <div class="toolbar-filters">
        ${providerSegmentHTML("session-provider")}
        ${dateRangeHTML("session-range")}
      </div>
    </div>
    <div id="results"></div>
  `;

  const results = document.getElementById("results");
  document.getElementById("session-search").addEventListener("input", (e) => {
    state.query = e.target.value;
    paint();
  });
  wireSortClicks(results, state, paint);
  wireProviderSegment(document.getElementById("session-provider"), state, reload);
  wireDateRange(document.getElementById("session-range"), state, reload);

  async function reload() {
    [allSessions, state.trend] = await Promise.all([
      fetchSessions(areaId, projectId, state.provider, state.days),
      fetchTrend(projectId, state.provider, state.days, areaId),
    ]);
    paint();
  }

  function paint() {
    const q = state.query.trim().toLowerCase();
    const filtered = q
      ? allSessions.filter((s) => (s.firstInstruction || s.sessionId).toLowerCase().includes(q))
      : allSessions.slice();
    filtered.sort(rowComparator(state.sortKey, state.sortDir, (row) => row.firstInstruction || row.sessionId));
    results.innerHTML = renderSessionsResults(areaId, projectId, filtered, state);
    wireSparklineTooltips(results);
  }

  paint();
}

// fetchSessions loads one project's session list, optionally restricted
// server-side to one provider and/or a rolling window of `days` (see
// internal/aggregate.Filter), and drops sessions with no real instructions
// the same way every list view does.
async function fetchSessions(areaId, projectId, provider, days) {
  const qs = filterQuery(provider, days, areaId);
  return (await fetchJSON(`/api/projects/${encodeURIComponent(projectId)}/sessions${qs}`)).filter((s) => s.instructions > 0);
}

function renderSessionsResults(areaId, projectId, list, state) {
  if (list.length === 0) {
    return `<p class="empty">一致するセッションがありません。</p>`;
  }

  const rows = list.map((s) => {
    const link = `#/project/${encodeURIComponent(s.dataAreaId || "default")}/${encodeURIComponent(projectId)}/session/${encodeURIComponent(s.provider)}/${encodeURIComponent(s.sessionId)}`;
    const label = truncate(s.firstInstruction, 20) || s.sessionId;
    return `
      <tr>
        <td><a href="${link}" title="${escapeHTML(s.firstInstruction || s.sessionId)}"><span class="chip">${escapeHTML(s.provider)}</span> ${escapeHTML(label)}</a></td>
        <td>${formatTimestamp(s.startedAt)}</td>
        <td class="num">${s.instructions}</td>
        <td class="num">${tokenBreakdown(s)}</td>
        <td class="num">${costCell(s)}</td>
        <td class="num"><span class="added">+${s.linesAdded}</span> / <span class="removed">-${s.linesRemoved}</span></td>
        <td class="num">${s.abandonedInstructions > 0 ? `<span class="chip warn">${s.abandonedInstructions} unmeasured</span>` : ""}</td>
      </tr>
    `;
  }).join("");

  const trendLink = `#/project/${encodeURIComponent(areaId || "default")}/${encodeURIComponent(projectId)}/trend`;
  return `
    ${statsHTML(list, () => 1, "session", state.trend, trendLink)}
    <div class="table-scroll">
    <table class="sessions-table">
      <thead>
        <tr>
          ${sortableHeader("Session", "firstInstruction", state)}
          ${sortableHeader("Started", "startedAt", state)}
          ${sortableHeader("Instructions", "instructions", state, true)}
          ${sortableHeader("Tokens (IN / Cache / OUT)", "totalTokens", state, true)}
          ${sortableHeader("Cost", "costUsd", state, true)}
          <th class="num">Lines</th>
          <th class="num"></th>
        </tr>
      </thead>
      <tbody>${rows}</tbody>
    </table>
    </div>
  `;
}

async function renderSession(areaId, projectId, provider, sessionId) {
  app.innerHTML = `<p class="loading">読み込み中…</p>`;
  const detail = await fetchJSON(
    `/api/projects/${encodeURIComponent(projectId)}/sessions/${encodeURIComponent(provider)}/${encodeURIComponent(sessionId)}${areaId ? `?area=${encodeURIComponent(areaId)}` : ""}`
  );

  const breadcrumb = `<p class="breadcrumb"><a href="#/">Projects</a> / <a href="#/project/${encodeURIComponent(areaId || "default")}/${encodeURIComponent(projectId)}">← Sessions</a></p>`;

  const clocRow = (label, cloc) => cloc
    ? `<div class="stat"><span class="label">${label}</span><span class="value">${cloc.code.toLocaleString()} code lines</span></div>`
    : `<div class="stat"><span class="label">${label}</span><span class="value">—</span></div>`;

  const turns = detail.turns.map((t) => {
    const badges = [];
    if (t.model) badges.push(`<span class="chip model">${escapeHTML(t.model)}</span>`);
    if (t.abandoned) badges.push(`<span class="chip warn">unmeasured (abandoned)</span>`);
    else if (t.mergedInto) badges.push(`<a class="chip merged" href="#turn-${escapeHTML(t.mergedInto)}">queued alongside another instruction — see below</a>`);
    else if (!t.resolved) badges.push(`<span class="chip">in progress</span>`);
    else if (t.tokensUnavailable) badges.push(`<span class="chip">tokens unavailable</span>`);

    const tokens = t.tokens ? `${t.tokens.total.toLocaleString()} tokens ${turnTokenBreakdown(t.tokens)}` : null;
    const diff = t.resolved && !t.abandoned && !t.mergedInto
      ? `<span class="added">+${t.diff.added}</span> / <span class="removed">-${t.diff.removed}</span> (${t.diff.filesChanged} files)`
      : null;
    const cost = t.tokens && !t.abandoned && !t.mergedInto
      ? (t.costUnavailable ? `<span class="chip">cost unavailable</span>` : formatUSD(t.costUsd))
      : null;
    const rawInstructionText = t.text || "(no text recorded)";
    const instructionText = rawInstructionText.trimEnd() || rawInstructionText;
    const hasLineBreak = /[\r\n]/.test(instructionText);

    return `
      <div class="turn" id="turn-${escapeHTML(t.instructionId)}">
        <div class="turn-head">
          <span>${badges.join(" ")}</span>
          <span class="ts">${formatTimestamp(t.ts)}</span>
        </div>
        <details class="instruction-details" data-has-line-break="${hasLineBreak}">
          <summary><span class="instruction-preview">${escapeHTML(instructionText)}</span></summary>
          <p class="text">${escapeHTML(instructionText)}</p>
        </details>
        <div class="metrics">${[diff, tokens, cost].filter(Boolean).join(" &nbsp;·&nbsp; ") || "&nbsp;"}</div>
      </div>
    `;
  }).join("");

  app.innerHTML = `
    ${breadcrumb}
    <p class="session-meta"><span class="chip">${escapeHTML(detail.provider)}</span> <code>${escapeHTML(detail.sessionId)}</code><br>${escapeHTML(detail.cwd || "")}</p>
    <div class="cloc-summary">
      ${clocRow("cloc at start", detail.clocStart)}
      ${clocRow("cloc at end", detail.clocEnd)}
    </div>
    ${turns || '<p class="empty">このセッションにはまだ指示が記録されていません。</p>'}
  `;
  initializeExpandableInstructions();
}

// --- graph detail page (stacked breakdown chart) ---
//
// Reached by clicking a cost/tokens sparkline stat card (see statsHTML's
// trendLink). Two entry points: global (areaId="", projectId="") for
// #/trend, and project-scoped for #/project/:areaId/:projectId/trend — the
// same shape renderProject/renderProjects already use elsewhere. This page
// runs its own independent provider/date-range filters rather than
// inheriting whatever the list page had selected (like Session Detail
// already doesn't inherit Session List's filters) — simpler, and the two
// screens are reached far enough apart in the flow that carrying state
// across would mostly be surprising.
async function renderTrendDetail(areaId, projectId) {
  app.innerHTML = `<p class="loading">読み込み中…</p>`;

  const state = { provider: "", days: "", dimension: "provider", breakdown: null };
  state.breakdown = await fetchBreakdown(projectId, areaId, state.provider, state.days, state.dimension);

  const breadcrumb = projectId
    ? `<p class="breadcrumb"><a href="#/">Projects</a> / <a href="#/project/${encodeURIComponent(areaId || "default")}/${encodeURIComponent(projectId)}">← Sessions</a></p>`
    : `<p class="breadcrumb"><a href="#/">← Projects</a></p>`;

  app.innerHTML = `
    ${breadcrumb}
    <div class="toolbar">
      <div class="toolbar-filters">
        ${providerSegmentHTML("trend-provider")}
        ${dateRangeHTML("trend-range")}
      </div>
      ${dimensionSegmentHTML("trend-dimension")}
    </div>
    <section class="chart-section">
      <h3 class="chart-title">Cost</h3>
      <div id="chart-area-cost"></div>
    </section>
    <section class="chart-section">
      <h3 class="chart-title">Tokens</h3>
      <div id="chart-area-tokens"></div>
    </section>
  `;

  // Cost and Tokens are two different-scale measures plotted as two
  // separate charts, stacked top-to-bottom, rather than one chart with a
  // metric toggle or (worse) two Y-axes — see dataviz's "one axis" rule:
  // never a dual-axis chart, and a toggle just hides the other measure
  // instead of letting them be compared at a glance.
  const chartAreaCost = document.getElementById("chart-area-cost");
  const chartAreaTokens = document.getElementById("chart-area-tokens");
  wireProviderSegment(document.getElementById("trend-provider"), state, reload);
  wireDateRange(document.getElementById("trend-range"), state, reload);
  wireDimensionSegment(document.getElementById("trend-dimension"), state, reload);

  async function reload() {
    state.breakdown = await fetchBreakdown(projectId, areaId, state.provider, state.days, state.dimension);
    paint();
  }

  function paint() {
    const costChart = buildStackedChart(state.breakdown, "costUsd", state.dimension);
    chartAreaCost.innerHTML = costChart.html;
    wireStackedChartTooltips(chartAreaCost, costChart.dates, costChart.series, "costUsd");

    const tokensChart = buildStackedChart(state.breakdown, "totalTokens", state.dimension);
    chartAreaTokens.innerHTML = tokensChart.html;
    wireStackedChartTooltips(chartAreaTokens, tokensChart.dates, tokensChart.series, "totalTokens");
  }

  paint();
}

// fetchBreakdown loads the graph detail page's stacked series — either
// across every project (projectId falsy, GET /api/projects/breakdown) or
// scoped to one (GET /api/projects/{id}/breakdown) — split along dimension
// (aggregate.BreakdownDimension: "provider" | "model" | "tokenType").
async function fetchBreakdown(projectId, areaId, provider, days, dimension) {
  const path = projectId ? `/api/projects/${encodeURIComponent(projectId)}/breakdown` : "/api/projects/breakdown";
  const qs = filterQuery(provider, days, areaId);
  const sep = qs ? "&" : "?";
  return fetchJSON(`${path}${qs}${sep}dimension=${encodeURIComponent(dimension)}`);
}

// dimensionSegmentHTML/wireDimensionSegment: which axis the stack breaks
// down by. Unlike metric, this changes what the server aggregates
// (aggregate.Breakdown groups by a completely different key), so it
// refetches rather than just repainting.
function dimensionSegmentHTML(id) {
  return `
    <div class="segmented" id="${id}">
      <button type="button" class="active" data-dimension="provider">Provider</button>
      <button type="button" data-dimension="model">Model</button>
      <button type="button" data-dimension="tokenType">Token type</button>
    </div>
  `;
}

function wireDimensionSegment(container, state, onReload) {
  container.addEventListener("click", (e) => {
    const btn = e.target.closest("button[data-dimension]");
    if (!btn || btn.classList.contains("active")) return;
    container.querySelector("button.active")?.classList.remove("active");
    btn.classList.add("active");
    state.dimension = btn.dataset.dimension;
    onReload();
  });
}

function isDarkMode() {
  return !!(window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches);
}

// --- chart hover tooltip (shared by the sparklines and the stacked bar
// chart) ---
//
// A native SVG <title> tooltip works but is slow to appear and easy to
// miss — dataviz's interaction.md calls for an immediate, styled tooltip
// instead ("the hover layer is part of the deliverable, not an upgrade").
// One tooltip element is created lazily and reused across every chart on
// the page; callers show/hide it and never touch its DOM directly. Content
// is built with textContent (never innerHTML) since category labels —
// especially model ids in a BreakdownModel chart — are effectively
// external data, per interaction.md's "Labels are untrusted data" rule.
let chartTooltipEl = null;

function ensureChartTooltipEl() {
  if (chartTooltipEl) return chartTooltipEl;
  chartTooltipEl = document.createElement("div");
  chartTooltipEl.className = "chart-tooltip";
  chartTooltipEl.hidden = true;
  chartTooltipEl.setAttribute("role", "tooltip");
  document.body.appendChild(chartTooltipEl);
  return chartTooltipEl;
}

// showChartTooltip renders `title` plus one row per entry in `rows`
// ({label, value, color}) and positions the tooltip near (clientX, clientY)
// — viewport coordinates, e.g. straight off a pointer event or a
// getBoundingClientRect() for keyboard focus.
function showChartTooltip(clientX, clientY, title, rows) {
  const el = ensureChartTooltipEl();
  el.textContent = "";

  const titleEl = document.createElement("div");
  titleEl.className = "chart-tooltip-date";
  titleEl.textContent = title;
  el.appendChild(titleEl);

  for (const row of rows) {
    const rowEl = document.createElement("div");
    rowEl.className = "chart-tooltip-row" + (row.total ? " chart-tooltip-total" : "");
    if (row.color) {
      const swatch = document.createElement("span");
      swatch.className = "chart-tooltip-swatch";
      swatch.style.background = row.color;
      rowEl.appendChild(swatch);
    }
    const labelEl = document.createElement("span");
    labelEl.className = "chart-tooltip-label";
    labelEl.textContent = row.label;
    rowEl.appendChild(labelEl);
    const valueEl = document.createElement("span");
    valueEl.className = "chart-tooltip-value";
    valueEl.textContent = row.value;
    rowEl.appendChild(valueEl);
    el.appendChild(rowEl);
  }

  el.hidden = false;
  positionChartTooltip(el, clientX, clientY);
}

function hideChartTooltip() {
  if (chartTooltipEl) chartTooltipEl.hidden = true;
}

// positionChartTooltip offsets the tooltip from the pointer/focus point and
// clamps it inside the viewport so it's never clipped at the window edge.
function positionChartTooltip(el, clientX, clientY) {
  const offset = 14;
  const vw = window.innerWidth, vh = window.innerHeight;
  const rect = el.getBoundingClientRect();
  let x = clientX + offset;
  let y = clientY + offset;
  if (x + rect.width > vw - 8) x = clientX - rect.width - offset;
  if (y + rect.height > vh - 8) y = clientY - rect.height - offset;
  el.style.left = `${Math.max(8, x)}px`;
  el.style.top = `${Math.max(8, y)}px`;
}

// wireSparklineTooltips attaches hover/focus tooltips to every hit dot in
// container's sparklines (see sparklineSVG's .spark-hit circles, which
// carry their own content as data-* attributes — no external state needed
// here). Call again after each re-render, since results.innerHTML replaces
// these elements.
function wireSparklineTooltips(container) {
  container.querySelectorAll(".spark-hit").forEach((dot) => {
    const { date, value, color } = dot.dataset;
    const title = formatDateLabel(date);
    const rows = [{ label: "", value, color }];
    dot.addEventListener("pointerenter", (e) => showChartTooltip(e.clientX, e.clientY, title, rows));
    dot.addEventListener("pointermove", (e) => showChartTooltip(e.clientX, e.clientY, title, rows));
    dot.addEventListener("pointerleave", hideChartTooltip);
    dot.addEventListener("focus", () => {
      const r = dot.getBoundingClientRect();
      showChartTooltip(r.left + r.width / 2, r.top, title, rows);
    });
    dot.addEventListener("blur", hideChartTooltip);
  });
}

// wireStackedChartTooltips attaches hover/focus tooltips to every day-bar
// in the graph detail page's stacked chart (see buildStackedChart's
// .chart-bar-group/.chart-hit elements). Unlike sparklines, a stacked bar's
// tooltip needs every series' value for that day, which is simpler to
// carry via closure over the same dates/series buildStackedChart actually
// drew from than to re-flatten into data-* attributes.
function wireStackedChartTooltips(container, dates, series, metric) {
  const dark = isDarkMode();
  container.querySelectorAll(".chart-bar-group").forEach((g) => {
    const di = Number(g.dataset.index);
    const hit = g.querySelector(".chart-hit");
    if (!hit) return;

    const title = formatDateLabel(dates[di]);
    const rows = series.map((s, si) => ({
      label: categoryLabel(s.key),
      value: formatMetricValue(s.points[di][metric], metric),
      color: categoryColor(s.key, si, dark),
    }));
    if (series.length > 1) {
      const total = series.reduce((sum, s) => sum + (s.points[di][metric] || 0), 0);
      rows.push({ label: "Total", value: formatMetricValue(total, metric), total: true });
    }

    hit.addEventListener("pointerenter", (e) => showChartTooltip(e.clientX, e.clientY, title, rows));
    hit.addEventListener("pointermove", (e) => showChartTooltip(e.clientX, e.clientY, title, rows));
    hit.addEventListener("pointerleave", hideChartTooltip);
    hit.addEventListener("focus", () => {
      const r = hit.getBoundingClientRect();
      showChartTooltip(r.left + r.width / 2, r.top, title, rows);
    });
    hit.addEventListener("blur", hideChartTooltip);
  });
}

// Categorical palette (dataviz skill's validated default, palette.md) —
// fixed hue order, light/dark steps. "other" (BreakdownModel's folded tail)
// is deliberately NOT one of these hues: it's a residual bucket, not an
// identity, so it wears the chrome's neutral "muted" step instead in both
// modes (palette.md's Chart chrome table).
const CHART_PALETTE_LIGHT = ["#2a78d6", "#eb6834", "#1baf7a", "#eda100", "#e87ba4", "#008300", "#4a3aa7", "#e34948"];
const CHART_PALETTE_DARK = ["#3987e5", "#d95926", "#199e70", "#c98500", "#d55181", "#008300", "#9085e9", "#e66767"];
const CHART_OTHER_COLOR = "#898781";

const CATEGORY_LABELS = {
  in: "IN", cache: "Cache", out: "OUT",
  claude: "Claude", codex: "Codex",
  unknown: "Unknown model", other: "Other",
};

function categoryLabel(key) {
  return CATEGORY_LABELS[key] || key;
}

function categoryColor(key, index, dark) {
  if (key === "other") return CHART_OTHER_COLOR;
  const palette = dark ? CHART_PALETTE_DARK : CHART_PALETTE_LIGHT;
  return palette[index % palette.length];
}

// buildStackedChart renders the graph detail page's stacked bar chart: one
// bar per day (breakdown.dates), each bar's segments stacked from the
// categories in breakdown.series, colored by categoryColor. Mark specs
// (surface gap between segments, thin bars, hairline gridlines, legend
// always present for >=2 series, hover carrying every series' value) follow
// the dataviz skill's references/marks-and-anatomy.md and interaction.md.
// Returns {html, dates, series} rather than a plain string — the caller
// wires up tooltips afterward (wireStackedChartTooltips) and needs the same
// filtered dates/series the chart was actually drawn from, not
// breakdown's raw (unfiltered) versions.
function buildStackedChart(breakdown, metric, dimension) {
  const dates = breakdown && breakdown.dates || [];
  const series = (breakdown && breakdown.series || []).filter((s) => s.points.some((p) => (p[metric] || 0) > 0));
  if (dates.length === 0 || series.length === 0) {
    return { html: `<p class="empty">この条件に一致するデータがありません。</p>`, dates: [], series: [] };
  }

  const dark = isDarkMode();
  const surface = dark ? "#1c1815" : "#faf6f0"; // matches :root --bg (style.css) — the gap/ring color must match what the chart actually sits on

  const W = 1120, H = 320;
  const padRight = 12, padTop = 16, padBottom = 36;

  const totals = dates.map((_, di) => series.reduce((sum, s) => sum + (s.points[di][metric] || 0), 0));
  const maxTotal = niceMax(Math.max(...totals, 0));

  // Y-axis tick values, computed before padLeft: the left gutter must fit
  // the widest of these labels ("$100.0000" is noticeably wider than
  // "$0.0000"), or its leading character clips off the SVG viewBox's left
  // edge — a real bug this sizing fixes, not just a cosmetic margin.
  const tickCount = 4;
  const yTickValues = Array.from({ length: tickCount + 1 }, (_, i) => (maxTotal / tickCount) * i);
  const yTickLabels = yTickValues.map((v) => formatAxisValue(v, metric));
  const longestLabelLen = Math.max(...yTickLabels.map((s) => s.length), 1);
  const padLeft = Math.max(48, 8 + longestLabelLen * 7 + 8);
  const plotW = W - padLeft - padRight;
  const plotH = H - padTop - padBottom;

  const n = dates.length;
  const slot = plotW / n;
  const barW = Math.max(2, Math.min(24, slot * 0.7));
  const gap = 1; // surface-color gap between stacked segments (marks-and-anatomy.md)

  const yFor = (v) => padTop + plotH - (maxTotal === 0 ? 0 : (v / maxTotal) * plotH);

  const bars = dates.map((date, di) => {
    const x = padLeft + di * slot + (slot - barW) / 2;
    let cursor = totals[di];
    const segs = series.map((s, si) => {
      const v = s.points[di][metric] || 0;
      const yTop = yFor(cursor);
      const yBottom = yFor(cursor - v);
      cursor -= v;
      const h = Math.max(0, yBottom - yTop - gap);
      if (v <= 0 || h <= 0) return "";
      const color = categoryColor(s.key, si, dark);
      return `<rect x="${x.toFixed(1)}" y="${yTop.toFixed(1)}" width="${barW.toFixed(1)}" height="${h.toFixed(1)}" rx="2" fill="${color}" stroke="${surface}" stroke-width="${gap}"></rect>`;
    }).join("");

    // The hit target carries no tooltip content itself — a native <title>
    // is slow to appear and easy to miss; wireStackedChartTooltips reads
    // data-index back off this element instead, to show an immediate,
    // styled tooltip built from `series`/`dates` in the caller's closure.
    // The hit rect is painted LAST (after the colored segments), not
    // first: SVG has no z-index, paint order is DOM order, and an earlier
    // hit rect would sit underneath the segments and never receive pointer
    // events — real users hovering the visible bar would get no tooltip at
    // all (caught by Playwright hover here: the colored rect "intercepts
    // pointer events" until this was reordered).
    const hitH = plotH;
    return `
      <g class="chart-bar-group" data-index="${di}">
        ${segs}
        <rect class="chart-hit" x="${x.toFixed(1)}" y="${padTop}" width="${Math.max(barW, slot - 1).toFixed(1)}" height="${hitH}" fill="transparent" tabindex="0"></rect>
      </g>
    `;
  }).join("");

  // Y-axis: 4 evenly-spaced gridlines + value labels (yTickValues/Labels
  // computed above, alongside padLeft).
  const yAxis = yTickValues.map((v, i) => {
    const y = yFor(v);
    return `
      <line x1="${padLeft}" y1="${y.toFixed(1)}" x2="${W - padRight}" y2="${y.toFixed(1)}" stroke="#e1e0d9" stroke-width="1"></line>
      <text x="${padLeft - 8}" y="${y.toFixed(1)}" text-anchor="end" dominant-baseline="middle" class="chart-axis-label">${escapeHTML(yTickLabels[i])}</text>
    `;
  }).join("");

  // X-axis: sparse date labels — every Nth day so labels never collide.
  const maxLabels = Math.floor(plotW / 56);
  const labelEvery = Math.max(1, Math.ceil(n / Math.max(1, maxLabels)));
  const xAxis = dates.map((date, di) => {
    if (di % labelEvery !== 0 && di !== n - 1) return "";
    const x = padLeft + di * slot + slot / 2;
    return `<text x="${x.toFixed(1)}" y="${H - padBottom + 16}" text-anchor="middle" class="chart-axis-label">${escapeHTML(formatDateLabel(date, true))}</text>`;
  }).join("");

  const legend = series.map((s, si) => `
    <span class="chart-legend-item">
      <span class="chart-legend-swatch" style="background:${categoryColor(s.key, si, dark)}"></span>
      ${escapeHTML(categoryLabel(s.key))}
    </span>
  `).join("");

  const html = `
    <div class="chart-svg-wrap">
      <svg viewBox="0 0 ${W} ${H}" width="${W}" height="${H}" role="img" aria-label="${escapeHTML(dimension)} breakdown">
        ${yAxis}
        ${bars}
        ${xAxis}
      </svg>
    </div>
    <div class="chart-legend">${legend}</div>
  `;
  return { html, dates, series };
}

// niceMax rounds v up to a "clean" axis maximum (1/2/5 × 10^n), so gridline
// labels read as round numbers rather than an arbitrary max.
function niceMax(v) {
  if (v <= 0) return 1;
  const magnitude = Math.pow(10, Math.floor(Math.log10(v)));
  const normalized = v / magnitude;
  const step = normalized <= 1 ? 1 : normalized <= 2 ? 2 : normalized <= 5 ? 5 : 10;
  return step * magnitude;
}

function formatMetricValue(v, metric) {
  return metric === "costUsd" ? formatUSD(v) : Math.round(v || 0).toLocaleString();
}

// formatAxisValue is formatMetricValue's compact counterpart for Y-axis
// ticks specifically: a token count like 300,000,000 is as wide as the axis
// gutter itself and gets clipped, where the tooltip has room to stay exact.
// Cost stays as-is (already short: $NN.NNNN never gets long enough to need
// compacting — see marks-and-anatomy.md's stat-tile convention this
// mirrors: 1,284 / 12.9K / $4.2M).
function formatAxisValue(v, metric) {
  if (metric === "costUsd") return formatUSD(v);
  const n = Math.round(v || 0);
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(n % 1_000_000 === 0 ? 0 : 1) + "M";
  if (n >= 1_000) return (n / 1_000).toFixed(n % 1_000 === 0 ? 0 : 1) + "K";
  return n.toLocaleString();
}

// formatDateLabel renders a "YYYY-MM-DD" bucket date as a short local date
// (short=true drops the year for axis ticks, where space is tight).
function formatDateLabel(dateStr, short) {
  const d = new Date(dateStr + "T00:00:00");
  if (isNaN(d.getTime())) return dateStr;
  return short
    ? d.toLocaleDateString(undefined, { month: "short", day: "numeric" })
    : d.toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" });
}

async function renderSettings() {
  app.innerHTML = `<p class="loading">読み込み中…</p>`;
  const settings = await fetchJSON("/api/settings");
  const rows = (settings.dataAreas || []).map((area) => `
    <li class="data-area-row">
      <div>
        <strong>${escapeHTML(area.name)}</strong>
        <code>${escapeHTML(area.path)}</code>
      </div>
      ${area.default ? `<span class="chip">デフォルト</span>` : `<button class="danger remove-area" data-area-id="${escapeHTML(area.id)}">削除</button>`}
    </li>
  `).join("");

  app.innerHTML = `
    <p class="breadcrumb"><button type="button" id="settings-back" class="back-link">← Projects</button></p>
    <h2>設定</h2>
    <p class="settings-help">複数のユーザーやサーバーのmaatplugデータ領域を追加すると、同じダッシュボードで使用状況を確認できます。共有フォルダやマウント済みのネットワークパスも指定できます。</p>
    <section class="settings-section">
      <h3>データ領域</h3>
      <ul class="data-area-list">${rows}</ul>
      <form id="add-area-form" class="add-area-form">
        <label>表示名<input name="name" type="text" placeholder="例: 開発サーバー"></label>
        <label>データパス<input name="path" type="text" placeholder="例: C:\\Users\\user\\AppData\\Roaming\\maatplug" required></label>
        <button type="submit">データ領域を追加</button>
      </form>
      <p id="settings-message" class="settings-message" hidden></p>
    </section>
  `;

  document.getElementById("settings-back").addEventListener("click", () => {
    window.location.hash = "";
  });
  const message = document.getElementById("settings-message");
  document.getElementById("add-area-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    const form = event.currentTarget;
    const body = Object.fromEntries(new FormData(form));
    try {
      await fetchJSON("/api/settings/areas", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      await renderSettings();
    } catch (err) {
      message.textContent = err.message;
      message.hidden = false;
    }
  });

  document.querySelectorAll(".remove-area").forEach((button) => {
    button.addEventListener("click", async () => {
      if (!confirm("このデータ領域を一覧から削除しますか？保存されているデータ自体は削除されません。")) return;
      try {
        await fetchJSON(`/api/settings/areas/${encodeURIComponent(button.dataset.areaId)}`, { method: "DELETE" });
        await renderSettings();
      } catch (err) {
        message.textContent = err.message;
        message.hidden = false;
      }
    });
  });
}

// Instructions occupy one line by default. Only text containing an actual
// line break or overflowing the available width behaves as an expandable
// details element; short single-line instructions remain plain text.
function initializeExpandableInstructions() {
  document.querySelectorAll(".instruction-details").forEach((details) => {
    const summary = details.querySelector("summary");
    const preview = details.querySelector(".instruction-preview");
    if (!summary || !preview) return;

    const expandable = details.dataset.hasLineBreak === "true" || preview.scrollWidth > preview.clientWidth;
    details.classList.toggle("expandable", expandable);
    if (!expandable) {
      summary.tabIndex = -1;
      summary.setAttribute("aria-disabled", "true");
    }
    summary.addEventListener("click", (event) => {
      if (!details.classList.contains("expandable")) event.preventDefault();
    });
  });
}

// --- toolbar / sortable-table / stat-card helpers, shared by the projects
// and per-project session list views ---

// searchBoxHTML renders a self-contained search input with its own stable
// id. Callers own the input event listener; the box is deliberately built
// once and left alone across re-renders (only the sibling #results div is
// repainted on every keystroke/sort click) so the input never loses focus
// or cursor position mid-type.
function searchBoxHTML(id, placeholder) {
  return `
    <div class="search-box">
      <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="11" cy="11" r="7"></circle><line x1="21" y1="21" x2="16.65" y2="16.65"></line></svg>
      <input type="text" id="${id}" placeholder="${escapeHTML(placeholder)}">
    </div>
  `;
}

// providerSegmentHTML renders the All/Claude/Codex segmented filter with a
// stable id, matching searchBoxHTML's "build once, repaint only #results"
// pattern — the buttons themselves don't need to survive a keystroke's
// focus, but wiring the listener once (rather than on every paint) avoids
// re-attaching it on each provider switch too.
function providerSegmentHTML(id) {
  return `
    <div class="segmented" id="${id}">
      <button type="button" class="active" data-provider="">All</button>
      <button type="button" data-provider="claude">Claude</button>
      <button type="button" data-provider="codex">Codex</button>
    </div>
  `;
}

// wireProviderSegment attaches a delegated click listener that updates
// state.provider, flips the pressed button's active class, and calls
// onReload — unlike search/sort, a provider change re-fetches from the
// server (internal/aggregate's providerFilter), since the un-filtered rows
// for the other provider were never sent to the client at all.
function wireProviderSegment(container, state, onReload) {
  container.addEventListener("click", (e) => {
    const btn = e.target.closest("button[data-provider]");
    if (!btn || btn.classList.contains("active")) return;
    container.querySelector("button.active")?.classList.remove("active");
    btn.classList.add("active");
    state.provider = btn.dataset.provider;
    onReload();
  });
}

// dateRangeHTML renders the rolling-window select (All time / 7 / 30 / 90
// days) with a stable id, matching searchBoxHTML/providerSegmentHTML's
// "build once, repaint only #results" pattern.
function dateRangeHTML(id) {
  return `
    <select id="${id}" class="date-range">
      <option value="">All time</option>
      <option value="7">Last 7 days</option>
      <option value="30">Last 30 days</option>
      <option value="90">Last 90 days</option>
    </select>
  `;
}

// wireDateRange wires the select's change event to state.days (the "days"
// query param — see internal/aggregate.Filter.Since) and, like the provider
// segment, triggers a server re-fetch rather than a client-side re-filter:
// sessions outside the window were never sent to the client at all.
function wireDateRange(select, state, onReload) {
  select.addEventListener("change", () => {
    state.days = select.value;
    onReload();
  });
}

// sortableHeader renders one <th> showing the current sort state (arrow +
// "active" class when this column is the active sort key) — wireSortClicks
// handles the actual state mutation on click, via the same data-sort-key.
function sortableHeader(label, key, state, numeric) {
  const active = state.sortKey === key;
  const dir = active ? state.sortDir : defaultSortDir(key);
  return `<th class="${numeric ? "num " : ""}${active ? "active" : ""}" data-sort-key="${key}"><span class="th-inner">${escapeHTML(label)}${sortArrowSVG(dir)}</span></th>`;
}

// defaultSortDir picks which direction a column starts in the first time
// it's clicked: text columns read naturally A→Z, everything else (numbers,
// dates) is more useful biggest/most-recent first.
function defaultSortDir(key) {
  return key === "name" || key === "firstInstruction" ? "asc" : "desc";
}

// wireSortClicks attaches one delegated click listener to container (the
// #results div) that handles clicks on any current or future
// th[data-sort-key] — delegation means this only needs to be wired once,
// even though the table itself is rebuilt on every paint().
function wireSortClicks(container, state, onChange) {
  container.addEventListener("click", (e) => {
    const th = e.target.closest("th[data-sort-key]");
    if (!th) return;
    const key = th.dataset.sortKey;
    if (state.sortKey === key) {
      state.sortDir = state.sortDir === "asc" ? "desc" : "asc";
    } else {
      state.sortKey = key;
      state.sortDir = defaultSortDir(key);
    }
    onChange();
  });
}

function sortArrowSVG(dir) {
  const points = dir === "asc" ? "6 15 12 9 18 15" : "6 9 12 15 18 9";
  return `<svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round"><polyline points="${points}"></polyline></svg>`;
}

// rowComparator builds a comparator for Array.prototype.sort over
// project/session summary rows. textFor supplies the string to compare for
// text-like keys ("name", "firstInstruction"); every other key is read as a
// plain number off the row.
function rowComparator(key, dir, textFor) {
  const mul = dir === "asc" ? 1 : -1;
  return (a, b) => {
    if (key === "name" || key === "firstInstruction") {
      return textFor(a).localeCompare(textFor(b)) * mul;
    }
    if (key === "startedAt") {
      return (new Date(a.startedAt).getTime() - new Date(b.startedAt).getTime()) * mul;
    }
    return ((a[key] ?? 0) - (b[key] ?? 0)) * mul;
  };
}

// statsHTML renders the 3-card cost/tokens/count summary above a table,
// totaled over whatever rows are currently visible (i.e. after search
// filtering) so it stays truthful to what's on screen rather than the
// unfiltered set.
function statsHTML(list, countRowsAs, noun, trend, trendLink) {
  const totalCost = list.reduce((sum, row) => sum + (row.costUsd || 0), 0);
  const totalTokens = list.reduce((sum, row) => sum + (row.totalTokens || 0), 0);
  const totalCount = list.reduce((sum, row) => sum + countRowsAs(row), 0);
  const countNoun = noun === "project" ? "session" : "instruction";
  // trend's window doesn't necessarily match the totals above (e.g. "All
  // time" totals next to a chart that's still bounded to the last 30 days
  // — see aggregate.Trend's trendDefaultWindowDays), so the caption always
  // states the chart's own window rather than implying it decomposes the
  // total.
  const trendWindow = trend && trend.length > 1 ? `last ${trend.length - 1}d` : "";
  // Sparkline cards link to the graph detail page (renderTrendDetail) — the
  // stacked, breakdown-able view of the same data. A plain <div> when there
  // is nothing to plot yet (trendLink still resolves, but a chart with zero
  // points isn't a useful destination).
  const cardTag = trend && trend.length > 1 ? "a" : "div";
  const cardHref = cardTag === "a" ? ` href="${escapeHTML(trendLink)}"` : "";

  return `
    <div class="stats">
      <${cardTag} class="stat stat-link"${cardHref}>
        <div class="stat-label">Total cost</div>
        <div class="stat-value">${formatUSD(totalCost)}</div>
        <div class="stat-foot">
          <span class="stat-caption">${trendWindow}</span>
          ${sparklineSVG(trend, "costUsd", "cost")}
        </div>
      </${cardTag}>
      <${cardTag} class="stat stat-link"${cardHref}>
        <div class="stat-label">Total tokens</div>
        <div class="stat-value">${totalTokens.toLocaleString()}</div>
        <div class="stat-foot">
          <span class="stat-caption">${trendWindow}</span>
          ${sparklineSVG(trend, "totalTokens", "tokens")}
        </div>
      </${cardTag}>
      <div class="stat">
        <div class="stat-label">Total ${countNoun}s</div>
        <div class="stat-value">${totalCount.toLocaleString()}</div>
        <div class="stat-caption">across ${list.length} ${noun}${list.length === 1 ? "" : "s"}</div>
      </div>
    </div>
  `;
}

// sparklineSVG draws a small line+area trend chart (cost or token totals
// per day, from aggregate.Trend's DailyPoint series) with a hit-target +
// native <title> tooltip per point. Colors are literal hex (not CSS custom
// properties: SVG presentation attributes don't reliably resolve var() the
// way inline style props do), picked once per render against the viewer's
// current color scheme.
function sparklineSVG(points, valueKey, kind) {
  if (!points || points.length < 2) return "";

  const dark = isDarkMode();
  const color = kind === "cost" ? (dark ? "#dd8a5e" : "#b8532f") : (dark ? "#9fb3dd" : "#4f6390");

  const values = points.map((p) => p[valueKey] || 0);
  const max = Math.max(...values, 0);
  const w = 90, h = 28, pad = 3;
  const stepX = (w - pad * 2) / (points.length - 1);
  const yFor = (v) => (max === 0 ? h - pad : h - pad - (v / max) * (h - pad * 2));
  const coords = values.map((v, i) => [pad + i * stepX, yFor(v)]);

  const line = "M" + coords.map(([x, y]) => `${x.toFixed(1)},${y.toFixed(1)}`).join(" L");
  const [firstX] = coords[0];
  const [lastX, lastY] = coords[coords.length - 1];
  const area = `${line} L${lastX.toFixed(1)},${h} L${firstX.toFixed(1)},${h} Z`;

  // Hit dots carry their tooltip content as data-* rather than <title>: a
  // native title tooltip is slow to appear and easy to miss (see
  // wireSparklineTooltips, which reads this same data back on
  // pointerenter/focus to show an immediate, styled tooltip instead).
  const dots = coords.map(([x, y], i) => {
    const p = points[i];
    const value = formatMetricValue(p[valueKey], valueKey);
    return `<circle class="spark-hit" cx="${x.toFixed(1)}" cy="${y.toFixed(1)}" r="7" fill="transparent" tabindex="0" data-date="${escapeHTML(p.date)}" data-value="${escapeHTML(value)}" data-color="${color}"></circle>`;
  }).join("");

  return `
    <svg class="spark" width="${w}" height="${h}" viewBox="0 0 ${w} ${h}" role="img" aria-label="${valueKey === "costUsd" ? "cost" : "token"} trend">
      <path d="${area}" fill="${color}" opacity="0.14"></path>
      <path d="${line}" fill="none" stroke="${color}" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"></path>
      <circle cx="${lastX.toFixed(1)}" cy="${lastY.toFixed(1)}" r="2.6" fill="${color}"></circle>
      ${dots}
    </svg>
  `;
}

// tokenBreakdown renders a project/session summary's total tokens with an
// IN / Cache / OUT sub-line. Cache is cacheRead+cacheCreation combined (the
// title attribute keeps the split available on hover) to keep the table
// column compact.
function tokenBreakdown(sum) {
  const cache = (sum.tokensCacheRead || 0) + (sum.tokensCacheCreation || 0);
  const title = `cache read: ${(sum.tokensCacheRead || 0).toLocaleString()} / cache creation: ${(sum.tokensCacheCreation || 0).toLocaleString()}`;
  return `
    <div class="tokens">
      <span class="total">${sum.totalTokens.toLocaleString()}</span>
      <span class="breakdown" title="${escapeHTML(title)}">
        <span class="tok-in">IN ${(sum.tokensInput || 0).toLocaleString()}</span> ·
        <span class="tok-cache">Cache ${cache.toLocaleString()}</span> ·
        <span class="tok-out">OUT ${(sum.tokensOutput || 0).toLocaleString()}</span>
      </span>
    </div>
  `;
}

// turnTokenBreakdown renders the same IN / Cache / OUT split inline for a
// single turn's token usage (model.TokenUsage shape).
function turnTokenBreakdown(tokens) {
  const cache = (tokens.cacheRead || 0) + (tokens.cacheCreation || 0);
  const title = `cache read: ${(tokens.cacheRead || 0).toLocaleString()} / cache creation: ${(tokens.cacheCreation || 0).toLocaleString()}`;
  return `<span class="breakdown" title="${escapeHTML(title)}">(IN ${(tokens.input || 0).toLocaleString()} · Cache ${cache.toLocaleString()} · OUT ${(tokens.output || 0).toLocaleString()})</span>`;
}

// costCell renders a project/session summary's priced token cost
// (costUsd), plus an "unpriced" badge when costUnpricedInstructions is
// nonzero — some of that summary's turns used a model with no matching
// entry in internal/pricing, so costUsd undercounts the real total rather
// than being wrong by zero.
function costCell(sum) {
  const unpriced = sum.costUnpricedInstructions > 0
    ? `<span class="chip warn" title="${sum.costUnpricedInstructions} instruction(s) used a model not in the pricing reference data">+unpriced</span>`
    : "";
  return `<span class="cost">${formatUSD(sum.costUsd)}</span> ${unpriced}`;
}

// formatUSD renders a USD amount the way maatplug-web shows cost everywhere:
// 4 decimal places, since individual turns are often well under a cent.
function formatUSD(amount) {
  return `$${(amount || 0).toLocaleString(undefined, { minimumFractionDigits: 4, maximumFractionDigits: 4 })}`;
}

function formatTimestamp(ts) {
  if (!ts) return "—";
  const d = new Date(ts);
  if (isNaN(d.getTime())) return "—";
  return d.toLocaleString();
}

// Remaining-usage badge, top-right, always visible across every view (see
// index.html — it lives outside #app, so hash-based re-renders never touch
// it). Polls GET /api/usage independently of the backend's own poll
// interval (internal/usage.Poller); it only ever reads that cache, so
// polling here more often than the backend refetches is harmless.
const USAGE_POLL_INTERVAL_MS = 30_000;
const USAGE_STALE_AFTER_MS = 20 * 60 * 1000; // no fresh fetch in 20min: flag as stale rather than showing silently outdated numbers

function startUsageBadge() {
  const badge = document.getElementById("usage-badge");
  if (badge) {
    badge.addEventListener("click", (event) => {
      const provider = event.target.closest(".provider")?.dataset.provider;
      if (!provider) return;
      refreshUsageBadgeNow(provider);
    });
    badge.addEventListener("keydown", (event) => {
      if (event.key === "Enter" || event.key === " ") {
        event.preventDefault();
        const provider = event.target.closest(".provider")?.dataset.provider;
        if (!provider) return;
        refreshUsageBadgeNow(provider);
      }
    });
  }
  refreshUsageBadge();
  setInterval(refreshUsageBadge, USAGE_POLL_INTERVAL_MS);
}

let usageRefreshInProgress = false;

async function refreshUsageBadgeNow(provider) {
  if (!provider) return;
  if (usageRefreshInProgress) return;
  const badge = document.getElementById("usage-badge");
  if (!badge) return;
  usageRefreshInProgress = true;
  badge.classList.add("refreshing");
  try {
    const query = provider ? `?provider=${encodeURIComponent(provider)}` : "";
    const providers = await fetchJSON(`/api/usage/refresh${query}`, { method: "POST" });
    renderUsageBadge(providers);
  } catch {
    // Keep showing the last good reading when a manual refresh fails.
  } finally {
    usageRefreshInProgress = false;
    badge.classList.remove("refreshing");
  }
}

async function refreshUsageBadge() {
  const badge = document.getElementById("usage-badge");
  if (!badge) return;
  let providers;
  try {
    providers = await fetchJSON("/api/usage");
  } catch {
    return; // keep showing the last good reading rather than clearing it on a transient fetch error
  }

  // Providers without a contract (see internal/usage.Poller) are simply
  // absent from the response — nothing to render for them at all.
  renderUsageBadge(providers);
}

function renderUsageBadge(providers) {
  const badge = document.getElementById("usage-badge");
  if (!badge) return;
  if (!providers || providers.length === 0) {
    badge.hidden = true;
    return;
  }
  const now = Date.now();
  badge.innerHTML = providers.map((p) => {
    const stale = now - new Date(p.fetchedAt).getTime() > USAGE_STALE_AFTER_MS;
    const windows = (p.windows || []).map((w) => {
      const low = w.remainingPercent <= 10 ? " low" : "";
      const title = w.resetLabel ? ` title="resets ${escapeHTML(w.resetLabel)}"` : "";
      return `<span class="window${low}"${title}>${escapeHTML(w.name)} ${w.remainingPercent}%</span>`;
    }).join(" · ");
    const staleMark = stale ? `<span class="stale" title="${escapeHTML(p.error || "last fetch is over 20 minutes old")}">⚠</span>` : "";
    return `<span class="provider" data-provider="${escapeHTML(p.provider)}" role="button" tabindex="0"><span class="name">${escapeHTML(p.provider)}</span>${windows}${staleMark}</span>`;
  }).join("");
  badge.hidden = false;
}

// truncate cuts s to at most max characters, ending with an ellipsis if it
// was longer (whitespace/newlines collapsed first, since instruction text
// is free-form and often multi-line).
function truncate(s, max) {
  if (!s) return "";
  const flat = s.replace(/\s+/g, " ").trim();
  return flat.length > max ? flat.slice(0, max) + "…" : flat;
}

function escapeHTML(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[c]));
}
