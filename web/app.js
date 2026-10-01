(() => {
  "use strict";
  function normalizeState(data) {
    if (!data || !data.user || !data.stats) throw new Error("대시보드 정보를 불러오지 못했어요.");
    const aliases = {
      serviceId: "ServiceID", billingDay: "BillingDay", paymentMethodId: "PaymentMethodID",
      billingDate: "BillingDate", trialEndsAt: "TrialEndsAt", isTrial: "IsTrial", skipped: "Skipped",
    };
    for (const key of ["subscriptions", "services", "paymentMethods", "currencies"]) {
      if (!Array.isArray(data[key])) data[key] = [];
    }
    for (const subscription of data.subscriptions) {
      for (const [source, target] of Object.entries(aliases)) {
        if (Object.hasOwn(subscription, source)) subscription[target] = subscription[source];
      }
    }
    return data;
  }
  let state = normalizeState(window.__INITIAL_STATE__);
  let stateGeneration = 0;
  let stateRequest = 0;
  let currentView = "dashboard";
  let selectedCurrency = "all";
  let subscriptionQuery = "";
  let subscriptionCategory = "";
  let subscriptionStatus = "active";
  let subscriptionSort = "next";
  const quickSkipPending = new Set();
  let upcomingView = "list";
  let deferredInstallPrompt = null;
  const pwaInstalled = () =>
    matchMedia("(display-mode: standalone)").matches || window.navigator.standalone === true;
  window.addEventListener("beforeinstallprompt", (event) => {
    event.preventDefault();
    deferredInstallPrompt = event;
  });
  const pwaPushSupported = () =>
    window.isSecureContext && "serviceWorker" in navigator && "PushManager" in window && "Notification" in window;
  const urlBase64ToUint8Array = (value) => {
    const padded = value.replace(/-/g, "+").replace(/_/g, "/").padEnd(Math.ceil(value.length / 4) * 4, "=");
    return Uint8Array.from(atob(padded), (character) => character.charCodeAt(0));
  };
  const currentMonth = new Date();
  let calendarYear = currentMonth.getFullYear();
  let calendarMonth = currentMonth.getMonth();
  const upcomingMonths = new Map();
  const currencyDigitsByCode = new Map(
    (state.currencies || []).map((currency) => [currency.code, currency.digits]),
  );
  const currencyFormatters = new Map();
  function replaceState(nextState) {
    state = normalizeState(nextState);
    stateGeneration++;
    upcomingRequest++;
    upcomingMonths.clear();
    currencyDigitsByCode.clear();
    (state.currencies || []).forEach((currency) =>
      currencyDigitsByCode.set(currency.code, currency.digits)
    );
    currencyFormatters.clear();
  }
  let upcomingRequest = 0;
  const main = document.querySelector("#main");
  const backdrop = document.querySelector("#modalBackdrop");
  const modal = backdrop.querySelector(".modal");
  const modalBody = document.querySelector("#modalBody");
  const modalFooter = document.querySelector("#modalFooter");
  const modalTitle = document.querySelector("#modalTitle");
  const modalKicker = document.querySelector("#modalKicker");
  const pageRegions = [
    document.querySelector(".topbar"),
    document.querySelector(".layout"),
  ];
  let modalReturnFocus = null;
  const themeButton = document.querySelector("#themeButton");
  const themeMedia = matchMedia("(prefers-color-scheme: dark)");
  const themeModes = ["system", "dark", "light"];
  const themeLabels = { system: "시스템", dark: "다크", light: "라이트" };
  const themeIcons = {
    system:
      '<svg aria-hidden="true" viewBox="0 0 24 24"><rect x="3" y="4" width="18" height="13" rx="2"/><path d="M8 21h8M12 17v4"/></svg>',
    dark:
      '<svg aria-hidden="true" viewBox="0 0 24 24"><path d="M20.4 15.2A8.5 8.5 0 0 1 8.8 3.6 8.5 8.5 0 1 0 20.4 15.2Z"/></svg>',
    light:
      '<svg aria-hidden="true" viewBox="0 0 24 24"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.93 4.93l1.42 1.42M17.65 17.65l1.42 1.42M2 12h2M20 12h2M4.93 19.07l1.42-1.42M17.65 6.35l1.42-1.42"/></svg>',
  };
  const uiIcons = {
    plus: '<svg aria-hidden="true" viewBox="0 0 24 24"><path d="M12 5v14M5 12h14"/></svg>',
    check: '<svg aria-hidden="true" viewBox="0 0 24 24"><path d="m5 12 4 4L19 6"/></svg>',
    dot:
      '<svg aria-hidden="true" viewBox="0 0 24 24"><circle cx="12" cy="12" r="2" fill="currentColor" stroke="none"/></svg>',
  };

  function applyTheme(preference, persist = false) {
    if (!themeModes.includes(preference)) preference = "system";
    const resolved = preference === "system" ? (themeMedia.matches ? "dark" : "light") : preference;
    document.documentElement.dataset.theme = resolved;
    document.documentElement.dataset.themePreference = preference;
    document.querySelector('meta[name="theme-color"]').content = resolved === "dark"
      ? "#09090B"
      : "#F7F7F8";
    themeButton.innerHTML = themeIcons[preference];
    const next = themeModes[(themeModes.indexOf(preference) + 1) % themeModes.length];
    themeButton.title = `테마: ${themeLabels[preference]} · 클릭하여 ${themeLabels[next]}로 변경`;
    themeButton.setAttribute("aria-label", themeButton.title);
    if (persist) {
      try {
        preference === "system"
          ? localStorage.removeItem("submanager-theme")
          : localStorage.setItem("submanager-theme", preference);
      } catch {}
    }
  }

  const esc = (value) =>
    String(value ?? "").replace(
      /[&<>'"]/g,
      (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", "'": "&#39;", '"': "&quot;" }[c]),
    );
  const serviceColor = (value) => /^#[0-9a-f]{6}$/i.test(value || "") ? value : "#D4D4D8";
  const currencyDigits = (currency) => currencyDigitsByCode.get(String(currency).toUpperCase()) ?? 2;
  const amountValue = (value, currency) =>
    (Number(value || 0) / (10 ** currencyDigits(currency))).toFixed(currencyDigits(currency));
  const amountMinorUnits = (value, currency) => {
    const digits = currencyDigits(currency),
      match = String(value).trim().match(new RegExp(`^\\d+(?:\\.(\\d{0,${digits}}))?$`));
    if (!match) return null;
    const [whole, fraction = ""] = String(value).trim().split(".");
    const minor = Number(whole) * (10 ** digits) + Number(fraction.padEnd(digits, "0") || 0);
    return Number.isSafeInteger(minor) ? minor : null;
  };
  const money = (value, currency = "KRW") => {
    const code = String(currency).toUpperCase();
    const digits = currencyDigits(code);
    const amount = Number(value || 0) / (10 ** digits);
    let formatter = currencyFormatters.get(code);
    if (formatter === undefined) {
      try {
        formatter = new Intl.NumberFormat("en-US", {
          style: "currency",
          currency: code,
          minimumFractionDigits: digits,
          maximumFractionDigits: digits,
        });
      } catch {
        formatter = null;
      }
      currencyFormatters.set(code, formatter);
    }
    if (!formatter) {
      return `${currency} ${
        amount.toLocaleString("en-US", {
          minimumFractionDigits: digits,
          maximumFractionDigits: digits,
        })
      }`;
    }
    return formatter.format(amount);
  };
  const cycle = (value) => value === "yearly" ? "매년" : "매월";
  const activeSubs = () => (state.subscriptions || []).filter((s) => s.Status === "active");
  const visibleMethods = () => (state.paymentMethods || []).filter((p) => !p.Archived);
  const visibleCurrencies = () => (state.currencies || []).filter((c) => !c.archived);
  const today = () => {
    const d = new Date();
    d.setHours(0, 0, 0, 0);
    return d;
  };
  const dateOf = (value) => new Date(`${value}T00:00:00`);
  const localDate = () => {
    const d = new Date(), p = (n) => String(n).padStart(2, "0");
    return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
  };
  const daysUntil = (value) => Math.max(0, Math.round((dateOf(value) - today()) / 86400000));
  const dueText = (value) => {
    const n = daysUntil(value);
    return n === 0 ? "오늘" : n === 1 ? "내일" : `${n}일 뒤`;
  };
  const followingPayment = (subscription) => {
    const value = subscription.NextPayment;
    if (!subscription.Skipped || value.slice(0, 7) !== localDate().slice(0, 7)) return value;
    const [year, month] = value.split("-").map(Number);
    const yearly = subscription.BillingCycle === "yearly";
    const targetMonth = yearly ? month : month === 12 ? 1 : month + 1;
    const targetYear = yearly ? year + 1 : month === 12 ? year + 1 : year;
    const lastDay = new Date(targetYear, targetMonth, 0).getDate();
    const day = Math.min(subscription.BillingDay, lastDay);
    const pad = (number) => String(number).padStart(2, "0");
    return `${targetYear}-${pad(targetMonth)}-${pad(day)}`;
  };

  function syncSummary() {
    document.querySelector("#activeCount").textContent = `${state.stats.ActiveCount}개`;
    document.querySelector("#upcomingCount").textContent = `${state.stats.UpcomingCount}건`;
    const currencies = state.stats.currencies || [];
    document.querySelector("#monthTotal").innerHTML = currencyAmountList(currencies, "monthTotal");
    document.querySelector("#yearTotal").innerHTML = currencyAmountList(currencies, "yearEstimate");
  }

  function currencyAmountList(currencies, field) {
    return currencies.length
      ? currencies.map((c) => `<span>${esc(money(c[field], c.currency))}</span>`).join("")
      : `<span>${esc(money(0, state.user.Currency || "KRW"))}</span>`;
  }

  function currencyTabs() {
    const currencies = state.stats.currencies || [];
    if (selectedCurrency !== "all" && !currencies.some((c) => c.currency === selectedCurrency)) {
      selectedCurrency = "all";
    }
    return `<div class="currency-tabs"><button type="button" data-currency="all" aria-pressed="${selectedCurrency === "all"}" class="${
      selectedCurrency === "all" ? "active" : ""
    }">전체</button>${
      currencies.map((c) =>
        `<button type="button" data-currency="${esc(c.currency)}" aria-pressed="${selectedCurrency === c.currency}" class="${
          selectedCurrency === c.currency ? "active" : ""
        }">${esc(c.currency)}</button>`
      ).join("")
    }</div>`;
  }

  function render(view = currentView) {
    currentView = view;
    syncSummary();
    const renders = {
      dashboard: renderDashboard,
      subscriptions: renderSubscriptions,
      upcoming: renderUpcoming,
      stats: renderStats,
    };
    (renders[view] || renderDashboard)();
    main.focus({ preventScroll: true });
  }

  function pageHead(title, subtitle, action = "") {
    return `
      <div class="section-head">
        <div>
          <h2>${esc(title)}</h2>
          ${subtitle ? `<p>${esc(subtitle)}</p>` : ""}
        </div>
        ${action}
      </div>`;
  }

  function renderDashboard() {
    const allSubs = activeSubs();
    if (!state.subscriptions.length) {
      main.innerHTML = `<div class="page">
        <section class="welcome"><h1>${esc(state.stats.Greeting)}</h1><p>첫 구독을 추가하면 한눈에 정리해 드릴게요.</p></section>
        <section class="dashboard-empty" aria-labelledby="emptyDashboardTitle">
          <span class="empty-icon" aria-hidden="true">${uiIcons.plus}</span>
          <h2 id="emptyDashboardTitle">아직 등록된 구독이 없어요.</h2>
          <p>정기 결제 중인 서비스를 추가하면<br>월별 지출과 결제 일정을 한눈에 확인할 수 있어요.</p>
          <button class="button primary" type="button" data-add-subscription>${uiIcons.plus} 첫 구독 추가</button>
          <span class="help">월 지출 추이 · 다가오는 결제 · 연간 예상 비용</span>
        </section>
      </div>`;
      return;
    }
    const allCurrencies = state.stats.currencies || [];
    const tabs = currencyTabs();
    const series = selectedCurrency === "all"
      ? allCurrencies : allCurrencies.filter((c) => c.currency === selectedCurrency);
    const total = selectedCurrency === "all"
      ? currencyAmountList(allCurrencies, "monthTotal")
      : esc(money(series[0]?.monthTotal || 0, selectedCurrency));
    const delta = selectedCurrency === "all" ? "통화별 합계" : deltaText(series[0]);
    main.innerHTML = `<div class="page">
      <section class="welcome"><h1>${esc(state.stats.Greeting)}</h1><p>이번 달 구독 현황을 확인해 보세요.</p></section>
      <div class="dashboard-overview">
        <section class="dashboard-chart" aria-labelledby="dashboardChartTitle">
          <div class="section-head">
            <div><h2 id="dashboardChartTitle">월별 지출</h2><p>최근 6개월 구독비 흐름</p></div>
            <div class="chart-actions">${tabs}<button class="text-button" type="button" data-view="stats">자세히 보기</button></div>
          </div>
          <div class="chart-card">
            <div class="chart-top"><div><span class="eyebrow">이번 달 구독비</span><strong class="chart-total ${selectedCurrency === "all" ? "multi" : ""}">${total}</strong></div><span class="chart-delta">${esc(delta)}</span></div>
            ${chart(series, state.stats.months)}
          </div>
        </section>
        ${upcomingPreview(allSubs)}
      </div>
      ${pageHead("내 구독", `${allSubs.length}개의 활성 구독`, '<button class="text-button" type="button" data-view="subscriptions">전체 보기</button>')}
      <section class="list-card" aria-label="구독 목록">${allSubs.length ? subscriptionList(allSubs.slice(0, 6)) : `<div class="dashboard-active-empty"><p>현재 이용 중인 구독이 없어요. 이전 지출 기록은 보관하고 있어요.</p><button class="button primary" type="button" data-add-subscription>구독 추가</button></div>`}</section>
    </div>`;
  }

  function upcomingPreview(subscriptions) {
    const upcoming = subscriptions.map((subscription) => ({ subscription, date: followingPayment(subscription) }))
      .filter((item) => item.date)
      .sort((a, b) => a.date.localeCompare(b.date) || a.subscription.ServiceName.localeCompare(b.subscription.ServiceName, "ko"));
    return `<section class="upcoming-preview" aria-labelledby="upcomingPreviewTitle">
      <div class="section-head"><div><h2 id="upcomingPreviewTitle">다가오는 결제</h2><p>가까운 결제부터 확인하세요.</p></div></div>
      ${upcoming.length ? `<div class="upcoming-preview-list">${upcoming.slice(0, 8).map(({ subscription: s, date }) => `
        <button class="upcoming-preview-row" type="button" data-edit-sub="${s.id}">
          <span class="upcoming-preview-service"><strong>${esc(s.ServiceName)}</strong><small class="upcoming-preview-meta">${esc(date.replaceAll("-", "."))}${s.IsTrial ? " · 체험 후 결제" : ""}</small></span>
          <span class="upcoming-preview-price"><strong>${esc(money(s.amount, s.Currency))}</strong><small class="upcoming-preview-due">${daysUntil(date) === 0 ? "오늘" : `D-${daysUntil(date)}`}</small></span>
        </button>`).join("")}</div>` : '<p class="help">결제 예정 없음</p>'}
      <button class="text-button" type="button" data-view="upcoming">전체 결제 일정 보기${upcoming.length > 8 ? ` · ${upcoming.length}건` : ""}</button>
    </section>`;
  }

  function subscriptionList(subscriptions, { management = false } = {}) {
    const labels = `<div class="list-row list-labels" aria-hidden="true"><span>서비스 / 카테고리</span><span>금액 / 주기</span><span>결제수단</span><span>${subscriptionStatus === "cancelled" && management ? "해지일" : "다음 결제"}</span></div>`;
    return `${management ? `<div class="subscription-management-heading">${labels}<span class="subscription-actions-label" aria-hidden="true">이번 달 결제</span></div>` : labels}${subscriptions.map((subscription) => subscriptionRow(subscription, { management })).join("")}`;
  }

  function deltaText(stat) {
    if (!stat) return "데이터가 없어요";
    const d = stat.delta;
    if (d === 0) return "지난달과 비슷해요";
    return d > 0
      ? `지난달보다 +${money(d, stat.currency)}`
      : `지난달보다 -${money(-d, stat.currency)}`;
  }

  // Fixed code slots keep a currency's line/legend identity across filters and themes.
  const chartColors = ["primary", "secondary", "tertiary", "fourth", "fifth", "sixth"];
  const currencyChartSlots = { KRW: 0, USD: 1, JPY: 2, EUR: 3, TRY: 4, ARS: 5 };
  function chartColor(currency) {
    const code = String(currency).toUpperCase();
    const slot = currencyChartSlots[code] ?? (1 + [...code].reduce((sum, char) => sum * 31 + char.charCodeAt(0), 0) % 5);
    return `var(--chart-${chartColors[slot]})`;
  }

  function chart(series, labels) {
    const safeSeries = series.length
      ? series
      : [{ currency: state.user.Currency || "KRW", monthlyTotals: labels.map(() => 0) }];
    const w = 720,
      h = 145,
      p = 10;
    const paths = safeSeries.map((s) => {
      const values = s.monthlyTotals,
        max = Math.max(...values, 1),
        min = Math.min(...values, 0),
        range = Math.max(max - min, 1);
      const pts = values.map((v, i) => ({
        x: p + i * (w - p * 2) / (values.length - 1),
        y: p + (max - v) * (h - p * 2) / range,
        v,
        label: labels[i],
      }));
      const line = pts.map((point, i) =>
        `${i ? "L" : "M"} ${point.x.toFixed(1)} ${point.y.toFixed(1)}`
      ).join(" ");
      const area = `${line} L ${pts[pts.length - 1].x} ${h} L ${pts[0].x} ${h} Z`;
      const color = chartColor(s.currency);
      return `${
        safeSeries.length === 1 ? `<path class="chart-area" style="--chart-color:${color}" d="${area}"/>` : ""
      }<path class="chart-line" style="stroke:${color}" vector-effect="non-scaling-stroke" d="${line}"/>${
        pts.map((point) =>
          `<circle class="chart-dot" style="stroke:${color}" vector-effect="non-scaling-stroke" cx="${point.x}" cy="${point.y}" r="3.2"><title>${s.currency} · ${point.label} ${
            money(point.v, s.currency)
          }</title></circle>`
        ).join("")
      }`;
    }).join("");
    return `<div class="chart-wrap"><div class="chart-legend">${
      safeSeries.map((s) =>
        `<span><i style="background:${chartColor(s.currency)}"></i>${esc(s.currency)}</span>`
      ).join("")
    }</div><svg viewBox="0 0 ${w} ${h}" preserveAspectRatio="none">${paths}</svg><div class="chart-labels">${
      labels.map(esc).map((x) => `<span>${x}</span>`).join("")
    }</div></div>`;
  }

  function subCard(s) {
    const classes = ["sub-card", s.Skipped && "skipped", s.IsTrial && "trial"]
      .filter(Boolean)
      .join(" ");
    const label = s.Skipped ? ` aria-label="${esc(s.ServiceName)}, 이번 달 결제 건너뜀"` : "";
    const meta = s.Skipped
      ? "이번 달 결제 건너뜀"
      : s.IsTrial
      ? `무료 체험 · ${s.BillingDate.replaceAll("-", ".")}부터 결제`
      : `${esc(s.Category || "기타")} · ${cycle(s.BillingCycle)}`;
    const paymentDate = s.IsTrial ? "체험 후 결제" : s.NextPayment.replaceAll("-", ".");

    return `
      <button class="${classes}" type="button" data-edit-sub="${s.id}"${label}>
        <span class="sub-content">
          <span class="sub-title">${esc(s.ServiceName)}</span>
          <span class="sub-meta ${s.Skipped ? "skip-status" : ""}">${meta}</span>
        </span>
        <span class="sub-price">
          ${money(s.amount, s.Currency)}
          <span>${paymentDate}</span>
        </span>
      </button>`;
  }

  function empty(title, body) {
    return `
      <div class="empty">
        <span class="empty-icon">${uiIcons.plus}</span>
        <strong>${esc(title)}</strong>
        <span>${esc(body)}</span>
      </div>`;
  }

  function renderSubscriptions() {
    const subs = state.subscriptions;
    const categories = [...new Set(subs.map((s) => s.Category || "기타"))].sort((a, b) =>
      a.localeCompare(b, "ko")
    );
    if (subscriptionCategory && !categories.includes(subscriptionCategory)) {
      subscriptionCategory = "";
    }
    const categoryButtons = categories.map((category) => `
      <button
        type="button"
        data-sub-category="${esc(category)}"
        aria-pressed="${subscriptionCategory === category}"
      >${esc(category)}</button>`).join("");

    main.innerHTML = `
      <div class="page">
        <section class="welcome">
          <h1>내 구독</h1>
          <p>구독 상태와 결제 일정을 확인하고 관리하세요.</p>
        </section>
        <section class="subscription-tools" aria-label="구독 검색 및 필터">
          <label class="subscription-search">
            <span class="sr-only">구독 검색</span>
            <svg aria-hidden="true" viewBox="0 0 24 24">
              <circle cx="11" cy="11" r="7"/>
              <path d="m16 16 4 4"/>
            </svg>
            <input
              id="subscriptionSearch"
              type="search"
              value="${esc(subscriptionQuery)}"
              placeholder="서비스, 결제수단, 메모 검색"
              autocomplete="off"
            >
          </label>
          <div class="category-filters" role="group" aria-label="카테고리 필터">
            <button
              type="button"
              data-sub-category=""
              aria-pressed="${subscriptionCategory === ""}"
            >전체</button>
            ${categoryButtons}
          </div>
          <div class="subscription-list-controls">
            <div class="subscription-status-filters category-filters" role="group" aria-label="구독 상태 필터">
              ${[["active", "이용 중"], ["trial", "무료 체험"], ["skipped", "건너뜀"], ["cancelled", "해지됨"]].map(([status, label]) => `<button type="button" data-sub-status="${status}" aria-pressed="${subscriptionStatus === status}">${label}</button>`).join("")}
            </div>
            <label class="subscription-sort"><span>정렬</span><select id="subscriptionSort">
              ${[["next", "다음 결제일순"], ["name", "이름순"], ["amount", "통화별 금액 높은 순"]].map(([sort, label]) => `<option value="${sort}" ${subscriptionSort === sort ? "selected" : ""}>${label}</option>`).join("")}
            </select></label>
          </div>
        </section>
        <div id="subscriptionHeading"></div>
        <section class="list-card" id="subscriptionResults" aria-live="polite"></section>
      </div>`;
    renderSubscriptionResults();
  }

  function filteredSubscriptions() {
    const query = subscriptionQuery.trim().normalize("NFKC").toLocaleLowerCase("ko-KR");
    return state.subscriptions.filter((s) => {
      if (subscriptionStatus === "cancelled" ? s.Status !== "cancelled" : s.Status !== "active") return false;
      if (subscriptionStatus === "trial" && !s.IsTrial) return false;
      if (subscriptionStatus === "skipped" && !s.Skipped) return false;
      const category = s.Category || "기타";
      if (subscriptionCategory && category !== subscriptionCategory) return false;
      if (!query) return true;
      return [s.ServiceName, category, s.PaymentMethodName, s.Memo].some((value) =>
        String(value || "").normalize("NFKC").toLocaleLowerCase("ko-KR").includes(query)
      );
    }).sort((a, b) => {
      const byName = () => a.ServiceName.localeCompare(b.ServiceName, "ko") || a.id - b.id;
      if (subscriptionSort === "name") return byName();
      if (subscriptionSort === "amount") return a.Currency.localeCompare(b.Currency) || b.amount - a.amount || byName();
      const date = (subscription) => subscription.Status === "cancelled" ? "" : followingPayment(subscription);
      return date(a).localeCompare(date(b)) || byName();
    });
  }

  function canQuickSkip(subscription) {
    if (subscription.Status !== "active") return false;
    if (subscription.Skipped) return true;
    const now = today();
    const lastDay = new Date(now.getFullYear(), now.getMonth() + 1, 0).getDate();
    const day = Math.min(subscription.BillingDay, lastDay);
    const date = `${localDate().slice(0, 7)}-${String(day).padStart(2, "0")}`;
    const anchor = subscription.BillingDate?.slice(0, 10);
    if (!anchor || !Number.isInteger(day) || day < 1 || date < anchor) return false;
    if (subscription.TrialEndsAt && date < subscription.TrialEndsAt.slice(0, 10)) return false;
    return subscription.BillingCycle === "monthly" || (subscription.BillingCycle === "yearly" && date.slice(5, 7) === anchor.slice(5, 7));
  }

  function subscriptionRow(s, { management = false } = {}) {
    const classes = ["list-row", s.Skipped && "skipped", s.IsTrial && "trial", s.Status === "cancelled" && "cancelled"]
      .filter(Boolean).join(" ");
    const label = s.Skipped ? ` aria-label="${esc(s.ServiceName)}, 이번 달 결제 건너뜀"` : "";
    const category = s.Status === "cancelled" ? "해지됨" : s.IsTrial ? "무료 체험 중" : esc(s.Category || "기타");
    const rowNextPayment = followingPayment(s);
    const nextPayment = s.Status === "cancelled"
      ? `${esc((s.CancelledAt || "").slice(0, 10).replaceAll("-", "."))} 해지`
      : s.IsTrial ? `${s.BillingDate.slice(5).replace("-", ".")}부터 결제`
      : `${dueText(rowNextPayment)} · ${rowNextPayment.slice(5).replace("-", ".")}`;
    const row = `<button class="${classes}" type="button" data-edit-sub="${s.id}"${label}>
      <span class="service-cell"><span><strong>${esc(s.ServiceName)}</strong><span>${category}</span>
        ${s.Skipped && s.Status === "active" ? '<small class="skip-status">이번 달 결제 건너뜀</small>' : ""}
      </span></span>
      <span><strong>${esc(money(s.amount, s.Currency))}</strong><br><small class="muted">${cycle(s.BillingCycle)}</small></span>
      <span class="muted">${esc(s.PaymentMethodName)}</span>
      <span><span class="status-pill">${nextPayment}</span></span>
    </button>`;
    if (!management) return row;
    const action = canQuickSkip(s)
      ? `<button class="button ghost small" type="button" data-quick-skip="${s.id}" aria-label="${esc(s.ServiceName)} 이번 달 결제 ${s.Skipped ? "다시 포함" : "건너뛰기"}" ${quickSkipPending.has(s.id) ? 'disabled aria-busy="true"' : ""}>${s.Skipped ? "다시 포함" : "이번 달 건너뛰기"}</button>`
      : `<span class="help">${s.Status === "cancelled" ? "기록 보기" : "이번 달 결제 없음"}</span>`;
    return `<div class="subscription-management-row">${row}<div class="subscription-row-actions">${action}</div></div>`;
  }

  function renderSubscriptionResults() {
    const subs = filteredSubscriptions();
    const filtered = subscriptionQuery.trim() || subscriptionCategory;
    const statusLabels = { active: "이용 중", trial: "무료 체험", skipped: "이번 달 건너뜀", cancelled: "해지됨" };
    document.querySelector("#subscriptionHeading").innerHTML = pageHead(
      `${filtered ? "검색 결과" : statusLabels[subscriptionStatus]} ${subs.length}개`,
      subscriptionStatus === "cancelled" ? "해지된 구독의 기록을 확인할 수 있어요." : "항목을 누르면 내용을 수정할 수 있어요.",
    );
    document.querySelector("#subscriptionResults").innerHTML = subs.length
      ? subscriptionList(subs, { management: true })
      : subscriptionEmptyState(filtered, statusLabels[subscriptionStatus]);
  }

  function subscriptionEmptyState(filtered, statusLabel) {
    if (!state.subscriptions.length) {
      return `${empty("아직 등록된 구독이 없어요.", "정기 결제 중인 서비스를 추가해 보세요.")}<div class="subscription-empty-actions"><button class="button primary" type="button" data-add-subscription>첫 구독 추가</button></div>`;
    }
    if (filtered) {
      return `${empty("검색 결과가 없어요.", "검색어나 카테고리, 상태를 바꿔 보세요.")}<div class="subscription-empty-actions"><button class="button ghost" type="button" data-reset-subscription-filters>필터 초기화</button></div>`;
    }
    return `${empty(`${statusLabel} 구독이 없어요.`, "다른 상태를 선택하거나 구독을 추가해 보세요.")}<div class="subscription-empty-actions"><button class="button ${subscriptionStatus === "active" ? "primary" : "ghost"}" type="button" ${subscriptionStatus === "active" ? "data-add-subscription" : "data-reset-subscription-filters"}>${subscriptionStatus === "active" ? "구독 추가" : "이용 중 구독 보기"}</button></div>`;
  }

  function subscriptionFocusSelector(element) {
    if (!element || !main.contains(element)) return null;
    if (element.id) return `#${CSS.escape(element.id)}`;
    for (const attribute of ["data-quick-skip", "data-edit-sub", "data-sub-status", "data-sub-category"]) {
      if (element.hasAttribute(attribute)) return `[${attribute}="${CSS.escape(element.getAttribute(attribute))}"]`;
    }
    return null;
  }

  function subscriptionPosition(fallbackFocus = null) {
    return { x: window.scrollX, y: window.scrollY, focus: subscriptionFocusSelector(document.activeElement) || fallbackFocus };
  }

  function restoreSubscriptionPosition(position) {
    const target = position.focus && main.querySelector(position.focus);
    if (backdrop.hidden) (target || main.querySelector(`[data-sub-status="${subscriptionStatus}"]`))?.focus({ preventScroll: true });
    window.scrollTo(position.x, position.y);
  }

  async function quickSkipSubscription(button) {
    const id = Number(button.dataset.quickSkip);
    const subscription = state.subscriptions.find((item) => item.id === id);
    if (!subscription || !canQuickSkip(subscription) || quickSkipPending.has(id)) return;
    const origin = main.querySelector("#subscriptionResults");
    const focus = subscriptionFocusSelector(button);
    quickSkipPending.add(id);
    beginAction(button);
    let saved = false;
    try {
      await api(`/api/subscriptions/${id}/skip`, { method: "POST", body: { skipped: !subscription.Skipped } });
      saved = true;
      await loadFreshState();
      quickSkipPending.delete(id);
      syncSummary();
      if (currentView === "subscriptions" && main.querySelector("#subscriptionResults")) {
        const position = subscriptionPosition(origin?.isConnected ? focus : null);
        renderSubscriptionResults();
        restoreSubscriptionPosition(position);
      }
      toast(subscription.Skipped ? "이번 달 결제를 다시 포함했어요." : "이번 달 결제를 건너뛰었어요.");
    } catch (err) {
      toast(saved ? "변경은 저장했지만 목록을 불러오지 못했어요. 잠시 후 다시 확인해 주세요." : err.message, true);
    } finally {
      quickSkipPending.delete(id);
      endAction(button);
      // A filter change can replace the original button while the request is pending.
      const currentButton = main.querySelector(`[data-quick-skip="${id}"]`);
      if (currentButton && currentButton !== button) {
        currentButton.disabled = false;
        currentButton.removeAttribute("aria-busy");
      }
    }
  }

  function openCancelledSubscription(subscription) {
    openModal(subscription.ServiceName, "해지된 구독 기록");
    modalBody.innerHTML = `<dl class="cancelled-subscription-details">
      <dt>금액 / 주기</dt><dd>${esc(money(subscription.amount, subscription.Currency))} · ${cycle(subscription.BillingCycle)}</dd>
      <dt>해지일</dt><dd>${esc((subscription.CancelledAt || "").slice(0, 10))}</dd>
      <dt>카테고리</dt><dd>${esc(subscription.Category || "기타")}</dd>
      <dt>결제수단</dt><dd>${esc(subscription.PaymentMethodName)}</dd>
      <dt>메모</dt><dd>${esc(subscription.Memo || "없음")}</dd>
    </dl>`;
  }

  function renderUpcoming() {
    if (upcomingView === "calendar") {
      renderUpcomingCalendar();
      return;
    }
    const subs = activeSubs().filter((s) => !s.Skipped).sort((a, b) =>
      a.NextPayment.localeCompare(b.NextPayment)
    );
    const groups = {};
    subs.forEach((s) => {
      const d = daysUntil(s.NextPayment);
      const k = d <= 7 ? "이번 주" : d <= 31 ? "이번 달" : "그 이후";
      (groups[k] ??= []).push(s);
    });
    const content = subs.length
      ? Object.entries(groups).map(([label, items]) => `
        <section class="date-group">
          <h3>${label}</h3>
          ${items.map(upcomingRow).join("")}
        </section>`).join("")
      : empty("예정된 결제가 없어요", "결제를 건너뛴 구독은 이번 달 목록에서 빠져요.");

    main.innerHTML = `
      <div class="page">
        <section class="welcome upcoming-head">
          <div>
            <h1>결제 예정</h1>
            <p>가까운 결제부터 차례로 알려드려요.</p>
          </div>
          ${upcomingViewSwitcher()}
        </section>
        <div class="upcoming-groups">${content}</div>
      </div>`;
  }

  function upcomingViewSwitcher() {
    return `<div class="upcoming-controls">
      <button class="button ghost small" type="button" data-export-ics>결제 일정 내보내기</button>
      <div class="view-switcher" aria-label="결제 예정 보기 방식">
        <button type="button" data-upcoming-view="list" aria-pressed="${upcomingView === "list"}" class="${upcomingView === "list" ? "active" : ""}">목록</button>
        <button type="button" data-upcoming-view="calendar" aria-pressed="${upcomingView === "calendar"}" class="${upcomingView === "calendar" ? "active" : ""}">캘린더</button>
      </div>
    </div>`;
  }

  function openICSExport() {
    openModal("ICS 내보내기", "결제 예정");
    modalBody.innerHTML = `<form id="icsExportForm">
      <p class="modal-description">ICS 형식 · 미래 결제 예정만 포함</p>
      <fieldset class="export-periods">
        <legend>내보낼 기간</legend>
        <label><input type="radio" name="months" value="1"><span>이번 달</span></label>
        <label><input type="radio" name="months" value="3"><span>앞으로 3개월</span></label>
        <label><input type="radio" name="months" value="12" checked><span>앞으로 1년</span></label>
      </fieldset>
      <div class="form-error" aria-live="polite"></div>
      <div class="form-actions"><button class="button ghost" type="button" data-close-modal>취소</button><button class="button primary" type="submit">ICS 내보내기</button></div>
    </form>`;
  }

  async function downloadICS(form) {
    const submit = form.querySelector('[type="submit"]');
    const error = form.querySelector(".form-error");
    submit.disabled = true;
    error.textContent = "";
    try {
      const months = new FormData(form).get("months");
      const response = await fetch(`/api/upcoming/export?format=ics&months=${encodeURIComponent(months)}`);
      if (response.status === 401) {
        location.replace("/");
        return;
      }
      if (!response.ok) {
        const data = await response.json().catch(() => ({}));
        throw new Error(data.error || "ICS 파일을 만들지 못했어요.");
      }
      const link = document.createElement("a");
      link.href = URL.createObjectURL(await response.blob());
      link.download = "submanager-payments.ics";
      link.click();
      URL.revokeObjectURL(link.href);
      closeModal();
      toast("ICS 파일을 내보냈어요.");
    } catch (err) {
      error.textContent = err.message;
    } finally {
      submit.disabled = false;
    }
  }

  const calendarPeriod = () =>
    `${calendarYear}-${String(calendarMonth + 1).padStart(2, "0")}`;

  function renderUpcomingCalendar() {
    const period = calendarPeriod();
    const month = upcomingMonths.get(period);
    main.innerHTML = `
      <div class="page">
        <section class="welcome upcoming-head">
          <div>
            <h1>결제 예정</h1>
            <p>결제가 있는 날을 월별로 확인해 보세요.</p>
          </div>
          ${upcomingViewSwitcher()}
        </section>
        <section class="calendar-card" aria-label="${calendarYear}년 ${calendarMonth + 1}월 결제 예정 캘린더">
          <div class="calendar-toolbar">
            <div>
              <h2>${calendarYear}년 ${calendarMonth + 1}월</h2>
              <p>${month ? calendarSummary(month) : "결제 일정을 불러오는 중이에요."}</p>
            </div>
            <div class="calendar-actions">
              <button class="icon-button" type="button" data-calendar-move="-1" aria-label="이전 달 보기"><svg aria-hidden="true" viewBox="0 0 24 24"><path d="m15 18-6-6 6-6"/></svg></button>
              <button class="button ghost small" type="button" data-calendar-today aria-label="현재 월 보기">오늘</button>
              <button class="icon-button" type="button" data-calendar-move="1" aria-label="다음 달 보기"><svg aria-hidden="true" viewBox="0 0 24 24"><path d="m9 18 6-6-6-6"/></svg></button>
            </div>
          </div>
          ${month ? calendarGrid(month) : `<div class="calendar-loading" aria-live="polite">불러오는 중…</div>`}
        </section>
      </div>`;
    if (!month) loadUpcomingMonth(period);
  }

  async function loadUpcomingMonth(period) {
    const request = ++upcomingRequest;
    const generation = stateGeneration;
    try {
      const month = await api(`/api/upcoming?month=${encodeURIComponent(period)}`);
      if (generation !== stateGeneration) return;
      upcomingMonths.set(period, month);
      if (request === upcomingRequest && currentView === "upcoming" && upcomingView === "calendar" && period === calendarPeriod()) {
        renderUpcomingCalendar();
      }
    } catch (err) {
      if (generation === stateGeneration && request === upcomingRequest && currentView === "upcoming" && upcomingView === "calendar" && period === calendarPeriod()) {
        toast(err.message, true);
        document.querySelector(".calendar-loading")?.replaceChildren("결제 일정을 불러오지 못했어요.");
      }
    }
  }

  function calendarSummary(month) {
    const payable = month.items.filter((item) => !item.skipped);
    const totals = Object.entries(month.totals)
      .filter(([, amount]) => amount > 0)
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([currency, amount]) => money(amount, currency));
    return `${payable.length}건${totals.length ? ` · ${totals.join(" / ")}` : ""}`;
  }

  function calendarGrid(month) {
    const itemsByDate = Object.groupBy
      ? Object.groupBy(month.items, (item) => item.scheduledDate)
      : month.items.reduce((groups, item) => {
        (groups[item.scheduledDate] ??= []).push(item);
        return groups;
      }, {});
    const first = new Date(calendarYear, calendarMonth, 1);
    const gridStart = new Date(calendarYear, calendarMonth, 1 - first.getDay());
    const todayText = localDate();
    const cells = [];
    for (let index = 0; index < 42; index++) {
      const date = new Date(gridStart.getFullYear(), gridStart.getMonth(), gridStart.getDate() + index);
      const dateText = `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`;
      const items = itemsByDate[dateText] || [];
      const inMonth = date.getMonth() === calendarMonth;
      const classes = ["calendar-day"];
      if (!inMonth) classes.push("outside");
      if (dateText === todayText) classes.push("today");
      if (dateText < todayText) classes.push("past");
      if (items.length) classes.push("has-payments");
      const label = `${date.getFullYear()}년 ${date.getMonth() + 1}월 ${date.getDate()}일${items.length ? `, 결제 예정 ${items.length}건` : ""}`;
      cells.push(`<button type="button" class="${classes.join(" ")}" ${items.length ? `data-calendar-date="${dateText}"` : ""} aria-label="${label}">
        <span class="calendar-date">${date.getDate()}</span>
        ${items.length ? `<span class="payment-dots" aria-hidden="true">${items.slice(0, 3).map((item) => `<i style="--dot:${serviceColor(item.color)}"></i>`).join("")}</span>` : ""}
      </button>`);
    }
    return `<div class="calendar-grid">
      ${["일", "월", "화", "수", "목", "금", "토"].map((day) => `<span class="calendar-weekday" aria-hidden="true">${day}</span>`).join("")}
      ${cells.join("")}
    </div>`;
  }

  function openCalendarDate(dateText) {
    const month = upcomingMonths.get(calendarPeriod());
    const items = month?.items.filter((item) => item.scheduledDate === dateText) || [];
    if (!items.length) return;
    const [, monthNumber, day] = dateText.split("-").map(Number);
    openModal(`${monthNumber}월 ${day}일 결제 예정`, `${dateText.slice(0, 4)}년 · ${items.length}건`);
    modalBody.innerHTML = `<div class="calendar-payment-list">${items.map((item) => `
      <article class="calendar-payment ${item.skipped ? "skipped" : ""}">
        <span class="calendar-payment-mark" style="--service-color:${serviceColor(item.color)}"></span>
        <span>
          <strong>${esc(item.serviceName)}</strong>
          <small>${esc(item.paymentMethodName)} · ${item.billingCycle === "yearly" ? "연간" : "월간"}${item.firstPayment ? " · 무료 체험 후 첫 결제" : ""}</small>
          ${item.skipped ? `<em>이번 결제 건너뜀</em>` : ""}
        </span>
        <b>${esc(money(item.amount, item.currency))}</b>
      </article>`).join("")}</div>`;
  }

  function upcomingRow(subscription) {
    const trialPrefix = subscription.IsTrial ? "무료 체험 · " : "";
    const due = subscription.IsTrial
      ? `첫 결제 ${dueText(subscription.NextPayment)}`
      : dueText(subscription.NextPayment);

    return `
      <button
        class="upcoming-row ${subscription.IsTrial ? "trial" : ""}"
        type="button"
        data-edit-sub="${subscription.id}"
      >
        <span class="sub-content">
          <span class="sub-title">${esc(subscription.ServiceName)}</span>
          <span class="sub-meta">
            ${trialPrefix}${esc(subscription.PaymentMethodName)} ·
            ${subscription.NextPayment.replaceAll("-", ".")}
          </span>
        </span>
        <strong>${money(subscription.amount, subscription.Currency)}</strong>
        <span class="due">${due}</span>
      </button>`;
  }

  function renderStats() {
    const allCurrencies = state.stats.currencies || [];
    const tabs = currencyTabs();
    const series = selectedCurrency === "all"
      ? allCurrencies
      : allCurrencies.filter((c) => c.currency === selectedCurrency);
    const selected = series[0];
    const statCards = selectedCurrency === "all"
      ? allCurrencies.map((currency) =>
        statCard(
          `${esc(currency.currency)} · 이번 달`,
          money(currency.monthTotal, currency.currency),
        )
      ).join("")
      : [
        statCard("이번 달", money(selected?.monthTotal || 0, selectedCurrency)),
        statCard(
          "최근 6개월 최고",
          money(Math.max(...(selected?.monthlyTotals || [0])), selectedCurrency),
        ),
        statCard(
          "최근 6개월 최저",
          money(Math.min(...(selected?.monthlyTotals || [0])), selectedCurrency),
        ),
      ].join("");
    const projectedTotals = new Map();
    activeSubs().forEach((subscription) => {
      projectedTotals.set(subscription.Currency, (projectedTotals.get(subscription.Currency) || 0) + monthlyEstimate(subscription));
    });
    const statsRows = activeSubs()
      .sort((a, b) => a.Currency.localeCompare(b.Currency) || b.amount - a.amount)
      .map((subscription) => subscriptionStatRow(subscription, projectedTotals))
      .join("") || empty("표시할 데이터가 없어요", "구독을 추가하면 분석을 시작해요.");
    const chartTitle = selectedCurrency === "all" ? "통화별 지출 흐름" : deltaText(selected);

    main.innerHTML = `
      <div class="page">
        <button
          class="back-button"
          type="button"
          data-view="dashboard"
          aria-label="대시보드로 돌아가기"
        >
          <svg aria-hidden="true" viewBox="0 0 24 24">
            <path d="m15 18-6-6 6-6"/>
          </svg>
          돌아가기
        </button>
        <section class="welcome stats-welcome">
          <h1>월별 지출</h1>
          <p>통화를 섞지 않고 각각의 흐름을 보여드려요.</p>
        </section>
        <div class="section-head">
          <div><h2>통화별 통계</h2></div>
          ${tabs}
        </div>
        <div class="stat-grid">
          ${statCards || statCard("이번 달", money(0, state.user.Currency))}
        </div>
        <section class="chart-card">
          <div class="chart-top">
            <div>
              <span class="eyebrow">최근 6개월</span>
              <strong class="chart-total">${chartTitle}</strong>
            </div>
          </div>
          ${chart(series, state.stats.months)}
        </section>
        ${pageHead("구독별 월 예상", "연간 구독은 같은 통화의 월평균으로 표시해요.")}
        <section class="list-card">${statsRows}</section>
      </div>`;
  }

  function statCard(label, value) {
    return `
      <div class="stat">
        <span>${label}</span>
        <strong>${value}</strong>
      </div>`;
  }

  function monthlyEstimate(subscription) {
    return subscription.BillingCycle === "yearly" ? Math.round(subscription.amount / 12) : subscription.amount;
  }

  function subscriptionStatRow(subscription, projectedTotals) {
    const monthly = monthlyEstimate(subscription);
    const currencyTotal = projectedTotals.get(subscription.Currency) || 0;
    const ratio = currencyTotal ? Math.round(monthly / currencyTotal * 100) : 0;

    return `
      <div class="list-row">
        <span class="service-cell">
          <span>
            <strong>${esc(subscription.ServiceName)}</strong>
            <span>${cycle(subscription.BillingCycle)}</span>
          </span>
        </span>
        <strong>${money(monthly, subscription.Currency)}</strong>
        <span class="muted">${esc(subscription.Category || "기타")}</span>
        <span class="muted">${ratio}% · ${esc(subscription.Currency)}</span>
      </div>`;
  }

  function openModal(title, kicker = "") {
    const opening = backdrop.hidden;
    if (opening) {
      modalReturnFocus = document.activeElement;
      pageRegions.forEach((region) => {
        region.inert = true;
      });
    }
    modalTitle.textContent = title;
    modalKicker.textContent = kicker;
    modal.classList.remove("subscription-modal");
    modalFooter.innerHTML = "";
    backdrop.hidden = false;
    document.body.style.overflow = "hidden";
    setTimeout(() => firstModalControl()?.focus(), 0);
  }

  function closeModal() {
    backdrop.hidden = true;
    document.body.style.overflow = "";
    modal.classList.remove("wide", "subscription-modal");
    modalBody.innerHTML = "";
    modalFooter.innerHTML = "";
    pageRegions.forEach((region) => {
      region.inert = false;
    });
    if (modalReturnFocus?.isConnected) {
      modalReturnFocus.focus({ preventScroll: true });
    }
    modalReturnFocus = null;
  }

  function modalControls() {
    const selector = [
      "button:not([disabled])",
      "input:not([disabled])",
      "select:not([disabled])",
      "textarea:not([disabled])",
      "a[href]",
      '[tabindex]:not([tabindex="-1"])',
    ].join(",");
    return [...modal.querySelectorAll(selector)].filter((element) =>
      !element.hidden && element.getClientRects().length > 0
    );
  }

  function firstModalControl() {
    return modalControls()[0] || modal;
  }

  function trapModalFocus(event) {
    const controls = modalControls();
    if (controls.length === 0) {
      event.preventDefault();
      modal.focus();
      return;
    }
    const first = controls[0];
    const last = controls[controls.length - 1];
    if (!modal.contains(document.activeElement)) {
      event.preventDefault();
      (event.shiftKey ? last : first).focus();
    } else if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  }

  function openServicePicker() {
    openModal("어떤 서비스를 이용하고 있나요?", "구독 추가 · 1/2");
    modalBody.innerHTML = `
      <div class="search">
        <svg viewBox="0 0 24 24">
          <circle cx="11" cy="11" r="7"/>
          <path d="m16 16 4 4"/>
        </svg>
        <input
          id="serviceSearch"
          type="search"
          placeholder="서비스 검색"
          autocomplete="off"
        >
      </div>
      <div class="service-picker" id="servicePicker">${serviceOptions("")}</div>`;
    const search = document.querySelector("#serviceSearch");
    search.addEventListener("input", () => {
      document.querySelector("#servicePicker").innerHTML = serviceOptions(search.value);
    });
  }
  function serviceOptions(query) {
    const q = query.trim().toLowerCase();
    const list = state.services.filter((s) => s.Name.toLowerCase().includes(q));
    const options = list.map((service) => `
      <button class="service-option" type="button" data-service="${service.ID}">
        <span>
          <strong>${esc(service.Name)}</strong>
          <small>${esc(service.Category)}</small>
        </span>
      </button>`).join("");

    return `
      ${options}
      <button class="service-option manual-option" type="button" data-service="manual">
        <span class="inline-icon">${uiIcons.plus}</span>
        직접 추가
      </button>`;
  }

  function subscriptionEditorDateValid(value) {
    if (!/^\d{4}-\d{2}-\d{2}$/.test(value || "")) return false;
    const date = new Date(`${value}T00:00:00Z`);
    return !Number.isNaN(date.getTime()) && date.toISOString().slice(0, 10) === value;
  }

  function subscriptionDraftErrors(values, currencies, methods, retainedMethod = null) {
    const errors = {};
    if (!String(values.serviceName || "").trim()) errors.serviceName = "서비스명을 입력해 주세요.";
    if (!currencies.some((currency) => currency.code === values.currency && !currency.archived)) {
      errors.currency = "사용할 수 있는 통화를 선택해 주세요. 보관된 통화는 새로 저장할 수 없어요.";
    }
    if (amountMinorUnits(values.amount, values.currency) === null) errors.amount = "0 이상의 금액을 통화의 소수 자릿수에 맞게 입력해 주세요.";
    if (!["monthly", "yearly"].includes(values.billingCycle)) errors.billingCycle = "결제 주기를 선택해 주세요.";
    if (!subscriptionEditorDateValid(values.billingDate)) errors.billingDate = "올바른 결제일을 선택해 주세요.";
    if (!methods.some((method) => Number(method.id) === Number(values.paymentMethodId) &&
      (!method.Archived || Number(method.id) === Number(retainedMethod)))) {
      errors.paymentMethodId = "사용할 수 있는 결제수단을 선택해 주세요.";
    }
    if (values.isTrial) {
      if (!subscriptionEditorDateValid(values.trialEndsAt)) errors.trialEndsAt = "무료 체험 종료일을 선택해 주세요.";
      else if (subscriptionEditorDateValid(values.billingDate) && values.billingDate < values.trialEndsAt) {
        errors.billingDate = "첫 결제일은 무료 체험 종료일 이후여야 해요.";
      }
    }
    return errors;
  }

  function subscriptionBillingPreview(values) {
    const amount = amountMinorUnits(values.amount, values.currency);
    const price = amount === null ? "금액을 입력해 주세요." : `${money(amount, values.currency)} · ${cycle(values.billingCycle)}`;
    if (!subscriptionEditorDateValid(values.billingDate)) return { price, rule: "결제일을 선택하면 결제 주기를 확인할 수 있어요.", date: "" };
    const month = Number(values.billingDate.slice(5, 7)), day = Number(values.billingDate.slice(8));
    const rule = `${values.billingCycle === "yearly" ? `매년 ${month}월 ${day}일` : `매월 ${day}일`}${day >= 29 ? " · 없는 날짜는 월말로 조정" : ""}`;
    const date = `${values.isTrial ? "설정된 첫 결제일" : "결제 기준일"}: ${values.billingDate.replaceAll("-", ".")}${values.isTrial && subscriptionEditorDateValid(values.trialEndsAt) ? ` · 체험 종료 ${values.trialEndsAt.replaceAll("-", ".")}` : ""}`;
    return { price, rule, date };
  }

  function openSubForm(service = null, existing = null) {
    const edit = !!existing;
    const s = existing || {
      ServiceName: service?.Name || "", Icon: service?.Icon || "", Color: service?.Color || "#D4D4D8",
      Category: service?.Category || "", Currency: service?.Currency || state.user.Currency || "KRW",
      BillingCycle: service?.BillingCycle || "monthly", amount: "", BillingDate: localDate(),
      TrialEndsAt: "", IsTrial: false, PaymentMethodID: visibleMethods()[0]?.id || "", Memo: "",
      ServiceID: service ? Number(service.ID) : null,
    };
    const currencies = (state.currencies || []).filter((currency) => !currency.archived || (edit && currency.code === s.Currency));
    const methods = (state.paymentMethods || []).filter((method) => !method.Archived || (edit && Number(method.id) === Number(s.PaymentMethodID)));
    openModal(edit ? `${s.ServiceName} 수정` : "구독 정보를 알려주세요", edit ? "구독 관리" : "구독 추가 · 2/2");
    modal.classList.add("wide", "subscription-modal");
    modalBody.innerHTML = `<form id="subscriptionForm" novalidate><div class="field-grid">
      <label class="field wide"><span>서비스명 *</span><input name="serviceName" required maxlength="80" value="${esc(s.ServiceName)}"></label>
      <label class="field"><span>금액 *</span><input name="amount" required type="number" min="0" step="${1 / (10 ** currencyDigits(s.Currency))}" inputmode="decimal" value="${s.amount === "" ? "" : amountValue(s.amount, s.Currency)}" placeholder="14900"></label>
      <label class="field"><span>통화 *</span><select name="currency">${currencies.map((currency) => `<option value="${esc(currency.code)}" ${s.Currency === currency.code ? "selected" : ""}>${esc(currency.code)}${currency.archived ? " · 보관됨 (변경 필요)" : currency.name && currency.name !== currency.code ? " · " + esc(currency.name) : ""}</option>`).join("")}</select></label>
      <label class="field"><span>결제 주기 *</span><select name="billingCycle"><option value="monthly" ${s.BillingCycle === "monthly" ? "selected" : ""}>매월</option><option value="yearly" ${s.BillingCycle === "yearly" ? "selected" : ""}>매년</option></select></label>
      <label class="field"><span id="billingDateLabel">${s.TrialEndsAt ? "첫 결제일" : "결제일"} *</span><input name="billingDate" required type="date" value="${esc((s.BillingDate || s.NextPayment || localDate()).slice(0, 10))}"></label>
      <label class="field"><span>결제수단 *</span><select name="paymentMethodId">${methods.map((method) => `<option value="${method.id}" ${Number(s.PaymentMethodID) === Number(method.id) ? "selected" : ""}>${esc(method.name)}${method.Archived ? " · 보관됨 (현재 구독에서 유지 가능)" : ""}</option>`).join("")}</select></label>
      <div class="subscription-form-preview wide" id="subscriptionPreview" role="status" aria-live="polite" aria-atomic="true"></div>
      <details class="optional-fields wide" ${s.TrialEndsAt || s.Memo ? "open" : ""}><summary id="subscriptionOptionalSummary">추가 옵션</summary><div class="optional-fields-body">
        <label class="check-row trial-toggle"><span>무료 체험 사용${service?.SupportsTrial ? " · 이 서비스에서 지원해요" : ""}</span><span class="switch"><input name="isTrial" type="checkbox" ${s.TrialEndsAt ? "checked" : ""}><span></span></span></label>
        <label class="field wide" id="trialEndField" ${s.TrialEndsAt ? "" : "hidden"}><span>무료 체험 종료일 *</span><input name="trialEndsAt" type="date" value="${esc((s.TrialEndsAt || "").slice(0, 10))}"><p class="help">첫 결제일 전까지 구독비에 포함하지 않아요.</p></label>
        <label class="field"><span>카테고리</span><input name="category" maxlength="40" value="${esc(s.Category)}" placeholder="음악, AI, 영상"></label>
        <label class="field"><span>메모</span><textarea name="memo" maxlength="500" placeholder="함께 사용하는 사람이나 플랜을 적어두세요.">${esc(s.Memo)}</textarea></label>
      </div></details>
    </div><div class="form-error" id="formError" role="alert"></div></form>
    ${edit ? `<div class="edit-actions">${canQuickSkip(s) ? `<button class="button skip-action ${s.Skipped ? "restore" : ""}" type="button" id="skipSub">${s.Skipped ? "이번 달 결제 다시 포함" : "이번 달 결제 건너뛰기"}</button>` : ""}<button class="button danger" type="button" id="cancelSub">구독 해지</button></div>` : ""}`;
    modalFooter.innerHTML = `<div class="form-actions subscription-form-actions">${!edit ? '<button class="button ghost left" type="button" id="backToPicker">이전</button>' : ""}<button class="button primary" form="subscriptionForm" type="submit">${edit ? "변경 저장" : "구독 추가"}</button></div>`;
    const form = modalBody.querySelector("#subscriptionForm"), error = form.querySelector("#formError");
    const fieldNames = ["serviceName", "amount", "currency", "billingCycle", "billingDate", "paymentMethodId", "trialEndsAt"];
    for (const name of fieldNames) {
      const control = form.elements.namedItem(name), message = document.createElement("small");
      message.id = `subscription-${name}-error`;
      message.className = "field-error";
      message.setAttribute("aria-live", "polite");
      control.setAttribute("aria-describedby", message.id);
      control.closest(".field").append(message);
    }
    const readDraft = () => {
      const values = Object.fromEntries(new FormData(form));
      values.isTrial = form.elements.namedItem("isTrial").checked;
      return values;
    };
    let submitted = false, pending = false;
    const showErrors = (errors, focus = false) => {
      for (const name of fieldNames) {
        const control = form.elements.namedItem(name);
        control.setAttribute("aria-invalid", errors[name] ? "true" : "false");
        form.querySelector(`#subscription-${name}-error`).textContent = errors[name] || "";
      }
      if (focus) {
        const name = fieldNames.find((field) => errors[field]);
        const control = name && form.elements.namedItem(name);
        if (control) {
          const details = control.closest("details");
          if (details) details.open = true;
          control.focus();
        }
      }
    };
    const syncDraft = () => {
      const values = readDraft(), enabled = values.isTrial;
      form.querySelector("#trialEndField").hidden = !enabled;
      form.elements.namedItem("trialEndsAt").required = enabled;
      form.querySelector("#billingDateLabel").textContent = enabled ? "첫 결제일 *" : "결제일 *";
      form.elements.namedItem("amount").step = String(1 / (10 ** currencyDigits(values.currency)));
      const options = [enabled && "무료 체험", String(values.category || "").trim() && "카테고리", String(values.memo || "").trim() && "메모"].filter(Boolean);
      form.querySelector("#subscriptionOptionalSummary").textContent = `추가 옵션${options.length ? " · " + options.join(" · ") : ""}`;
      const preview = subscriptionBillingPreview(values);
      form.querySelector("#subscriptionPreview").innerHTML = `<strong>${esc(preview.price)}</strong><span>${esc(preview.rule)}</span>${preview.date ? `<span>${esc(preview.date)}</span>` : ""}`;
      if (submitted) showErrors(subscriptionDraftErrors(values, state.currencies || [], state.paymentMethods || [], edit ? s.PaymentMethodID : null));
    };
    form.addEventListener("input", syncDraft);
    form.addEventListener("change", syncDraft);
    syncDraft();
    // Surface a retained inactive currency immediately instead of silently selecting another.
    if (edit && currencies.find((currency) => currency.code === s.Currency)?.archived) {
      submitted = true;
      showErrors({ currency: "이 통화는 보관되어 있어요. 저장하려면 사용 가능한 통화를 선택해 주세요." });
    }
    modalBody.querySelector("#backToPicker")?.addEventListener("click", openServicePicker);
    modalFooter.querySelector("#backToPicker")?.addEventListener("click", openServicePicker);
    const editorControls = () => [...form.querySelectorAll("input, select, textarea"), ...modalBody.querySelectorAll(".edit-actions button"), ...modalFooter.querySelectorAll("button")];
    const runMutation = async (operation, message) => {
      if (pending || !beginAction(form)) return;
      pending = true;
      const controls = editorControls().map((control) => [control, control.disabled]);
      controls.forEach(([control]) => { control.disabled = true; });
      error.textContent = "";
      try {
        await operation();
        if (form.isConnected) closeModal();
        toast(message);
        try { await refresh(); }
        catch { toast("저장은 완료했지만 목록을 새로고침하지 못했어요. 화면을 새로고침해 주세요.", true); }
      } catch (err) {
        showFormError(error, err.message);
      } finally {
        controls.forEach(([control, disabled]) => { control.disabled = disabled; });
        endAction(form);
        pending = false;
      }
    };
    form.addEventListener("submit", async (event) => {
      event.preventDefault();
      if (pending) return;
      submitted = true;
      const values = readDraft(), errors = subscriptionDraftErrors(values, state.currencies || [], state.paymentMethods || [], edit ? s.PaymentMethodID : null);
      showErrors(errors, true);
      if (Object.keys(errors).length) {
        error.textContent = "입력 항목을 확인해 주세요.";
        return;
      }
      const body = {
        serviceId: edit ? s.ServiceID : service ? Number(service.ID) : null,
        serviceName: values.serviceName, icon: s.Icon || String(values.serviceName).slice(0, 1).toUpperCase(), color: s.Color || "#D4D4D8",
        amount: amountMinorUnits(values.amount, values.currency), currency: values.currency, billingCycle: values.billingCycle,
        billingDate: values.billingDate, trialEndsAt: values.isTrial ? values.trialEndsAt : "", paymentMethodId: Number(values.paymentMethodId),
        category: values.category, memo: values.memo,
      };
      await runMutation(() => api(edit ? `/api/subscriptions/${s.id}` : "/api/subscriptions", { method: edit ? "PUT" : "POST", body }), edit ? "구독 정보를 바꿨어요." : "새 구독을 추가했어요.");
    });
    modalBody.querySelector("#skipSub")?.addEventListener("click", () => runMutation(() => api(`/api/subscriptions/${s.id}/skip`, { method: "POST", body: { skipped: !s.Skipped } }), s.Skipped ? "이번 결제를 다시 포함했어요." : "이번 결제만 건너뛰었어요."));
    modalBody.querySelector("#cancelSub")?.addEventListener("click", () => {
      if (pending || !confirm(`${s.ServiceName} 구독을 해지할까요? 과거 기록은 그대로 남아요.`)) return;
      return runMutation(() => api(`/api/subscriptions/${s.id}/cancel`, { method: "POST", body: {} }), "구독을 해지했어요.");
    });
  }

  function openSettings() {
    openModal("설정", "내 환경");
    modal.classList.add("wide");
    modalBody.innerHTML = `
      ${settingsTabsTemplate()}
      <form id="settingsForm">
        ${profileSettingsTemplate()}
        ${paymentSettingsTemplate()}
        ${currencySettingsTemplate()}
        ${notificationSettingsTemplate()}
        ${channelSettingsTemplate()}
        ${dataSettingsTemplate()}
        <div id="settingsSaveArea">
          <div class="form-error" id="settingsError"></div>
          <div class="form-actions">
            <button class="button ghost" type="button" data-close-modal>닫기</button>
            <button class="button primary" type="submit">설정 저장</button>
          </div>
        </div>
      </form>
      ${accountSettingsTemplate()}
      ${sessionSettingsTemplate()}`;
    bindSettings();
  }

  function settingsTabsTemplate() {
    const tabs = [
      ["profile", "기본 설정"],
      ["account", "계정"],
      ["sessions", "세션 관리"],
      ["payments", "결제수단"],
      ["currencies", "통화"],
      ["notifications", "알림"],
      ["channels", "연동"],
      ["data", "데이터 관리"],
    ];

    return `
      <div class="settings-tabs">
        ${
      tabs.map(([tab, label], index) => `
          <button
            class="${index === 0 ? "active" : ""}"
            type="button"
            data-tab="${tab}"
          >${label}</button>`).join("")
    }
      </div>`;
  }

  function profileSettingsTemplate() {
    const currencyOptions = visibleCurrencies().map((currency) => {
      const selected = state.user.Currency === currency.code ? "selected" : "";
      const name = currency.name && currency.name !== currency.code
        ? ` · ${esc(currency.name)}`
        : "";
      return `<option value="${esc(currency.code)}" ${selected}>${
        esc(currency.code)
      }${name}</option>`;
    }).join("");

    return `
      <section class="settings-section active" data-section="profile">
        <div class="field-grid">
          <label class="field wide">
            <span>사용자 이름</span>
            <input name="name" required value="${esc(state.user.Name)}">
          </label>
          <label class="field">
            <span>기본 통화</span>
            <select name="currency">${currencyOptions}</select>
          </label>
        </div>
        <div class="edit-actions">
          <button class="button ghost" type="button" id="logoutButton">로그아웃</button>
        </div>
      </section>`;
  }

  function paymentSettingsTemplate() {
    const builtinMethods = state.paymentMethods.filter((method) => method.IsBuiltin);
    const customMethods = state.paymentMethods.filter((method) =>
      !method.IsBuiltin && !method.Archived
    );

    return `
      <section class="settings-section" data-section="payments">
        <h3>기본 제공</h3>
        <div>${builtinMethods.map(pmRow).join("")}</div>
        <h3>사용자 지정</h3>
        <div id="customMethods">
          ${
      customMethods.map(pmRow).join("") || '<p class="help">추가한 결제수단이 아직 없어요.</p>'
    }
        </div>
        <div class="add-row">
          <input id="newMethod" placeholder="예: 토스페이" maxlength="40">
          <button class="button" type="button" id="addMethod">추가</button>
        </div>
        <p class="help">사용 중인 결제수단을 삭제하면 기존 구독 기록을 위해 보관 처리돼요.</p>
      </section>`;
  }

  function currencySettingsTemplate() {
    const currencies = state.currencies || [];
    const builtinCurrencies = currencies.filter((currency) => currency.isBuiltin);
    const customCurrencies = currencies.filter((currency) =>
      !currency.isBuiltin && !currency.archived
    );

    return `
      <section class="settings-section" data-section="currencies">
        <h3>기본 제공</h3>
        <div>${builtinCurrencies.map(currencyRow).join("")}</div>
        <h3>사용자 지정</h3>
        <div>
          ${
      customCurrencies.map(currencyRow).join("") || '<p class="help">추가한 통화가 아직 없어요.</p>'
    }
        </div>
        <div class="add-row">
          <input id="newCurrency" placeholder="예: GBP" maxlength="3" autocapitalize="characters">
          <button class="button" type="button" id="addCurrency">추가</button>
        </div>
        <p class="help">ISO 형식의 영문 통화 코드 3자리를 입력해 주세요.</p>
      </section>`;
  }

  function notificationSettingsTemplate() {
    return `
      <section class="settings-section" data-section="notifications">
        ${notificationToggle("notifyUpcoming", "결제 예정 알림", state.settings.NotifyUpcoming)}
        <label class="field">
          <span>결제 며칠 전에 알릴까요?</span>
          <input
            name="notifyDays"
            type="number"
            min="0"
            max="30"
            value="${state.settings.NotifyDays}"
          >
        </label>
      </section>`;
  }

  function notificationToggle(name, label, checked) {
    return `
      <label class="check-row">
        <span>${label}</span>
        <span class="switch">
          <input name="${name}" type="checkbox" ${checked ? "checked" : ""}>
          <span></span>
        </span>
      </label>`;
  }

  function channelSettingsTemplate() {
    const discordEnabled = state.settings.DiscordEnabled;
    const telegramEnabled = state.settings.TelegramEnabled;
    const pwaEnabled = state.settings.PWAEnabled;
    return `
      <section class="settings-section" data-section="channels">
        <section class="integration-option">
          ${integrationToggle("discordEnabled", "Discord", discordEnabled)}
          <div class="integration-fields" data-integration-fields="discordEnabled" ${discordEnabled ? "" : "hidden"}>
            <label class="field">
              <span>Webhook URL</span>
              <input
                name="discordWebhook"
                type="url"
                value="${esc(state.settings.DiscordWebhook)}"
                placeholder="https://discord.com/api/webhooks/..."
              >
            </label>
            <div class="form-actions">
              <button class="button ghost" type="button" data-test="discord">Discord 테스트</button>
            </div></div>
        </section>
        <section class="integration-option">
          ${integrationToggle("telegramEnabled", "Telegram", telegramEnabled)}
          <div class="integration-fields" data-integration-fields="telegramEnabled" ${telegramEnabled ? "" : "hidden"}>
            <div class="field-grid">
              <label class="field wide">
                <span>Bot Token</span>
                <input
                  name="telegramBotToken"
                  type="password"
                  value="${esc(state.settings.TelegramBotToken)}"
                  autocomplete="off"
                >
              </label>
              <label class="field wide">
                <span>Chat ID</span>
                <input name="telegramChatId" value="${esc(state.settings.TelegramChatID)}">
              </label>
            </div>
            <div class="form-actions">
              <button class="button ghost" type="button" data-test="telegram">Telegram 테스트</button>
            </div></div>
        </section>
        <section class="integration-option pwa-option">
          ${integrationToggle("pwaEnabled", "PWA", pwaEnabled)}
          <div class="integration-fields pwa-content" data-integration-fields="pwaEnabled" ${pwaEnabled ? "" : "hidden"}>
          <div class="pwa-description">
            <p class="help">SubManager를 앱처럼 설치해 빠르게 열고, 기기 푸시로 결제 예정 알림을 받을 수 있어요.</p>
            <p class="help">브라우저 메뉴에서 “홈 화면에 추가”를 선택해 설치할 수 있어요.</p>
          </div>
          <div class="pwa-actions">
            ${pwaInstalled()
              ? '<p class="help">이 기기에 이미 설치되어 있어요.</p>'
              : deferredInstallPrompt
                ? '<button class="button ghost" type="button" id="installPWA">앱으로 설치</button>'
                : ""}
            ${pwaPushSupported()
              ? '<button class="button ghost" type="button" id="enablePWAPush">이 기기의 푸시 알림 켜기</button><button class="button ghost" type="button" id="disablePWAPush">이 기기의 푸시 알림 끄기</button><button class="button ghost" type="button" data-test="pwa">PWA 테스트</button>'
              : '<p class="help">푸시 알림은 HTTPS에서 지원하는 브라우저로 열어 주세요.</p>'}
          </div>
          </div>
        </section>
      </section>`;
  }

  function integrationToggle(name, label, checked) {
    return `<label class="check-row integration-toggle">
      <strong>${label}</strong>
      <span class="switch">
        <input name="${name}" type="checkbox" ${checked ? "checked" : ""}>
        <span></span>
      </span>
    </label>`;
  }

  function dataSettingsTemplate() {
    return `
      <section class="settings-section" data-section="data">
        <h3>JSON 백업</h3>
        <p class="help">
          구독, 결제수단, 가격 이력과 설정을 JSON 파일로 관리해요.
          로그인 비밀번호, 세션과 알림 연동 정보는 기본적으로 포함하지 않아요.
        </p>
        <label class="check-row backup-secret-option">
          <span>
            <strong>알림 연동 정보 포함</strong>
            <small>Discord·Telegram 정보와 PWA 푸시 구독 정보를 백업에 저장해요.</small>
          </span>
          <span class="switch">
            <input id="includeNotificationCredentials" type="checkbox">
            <span></span>
          </span>
        </label>
        <div class="data-actions">
          <button class="button" type="button" id="exportData">JSON 내보내기</button>
          <label class="button import-label">
            JSON 가져오기
            <input
              id="importData"
              type="file"
              accept="application/json,.json"
              aria-label="JSON 백업 가져오기"
              hidden
            >
          </label>
        </div>
        <p class="help danger-text">가져오기는 현재 구독 데이터를 백업 파일의 내용으로 교체해요.</p>
      </section>`;
  }

  function accountSettingsTemplate() {
    return `
      <section class="settings-section" data-section="account">
        <div class="account-block">
          <h3>이메일 변경</h3>
          <p class="help">다음 로그인부터 변경한 이메일을 사용해요.</p>
          <form id="emailChangeForm">
            <div class="field-grid">
              <label class="field wide">
                <span>새 이메일</span>
                <input
                  name="email"
                  type="email"
                  maxlength="254"
                  autocomplete="email"
                  value="${esc(state.user.Email)}"
                >
              </label>
              <label class="field wide">
                <span>현재 비밀번호</span>
                <input
                  name="currentPassword"
                  type="password"
                  maxlength="72"
                  autocomplete="current-password"
                >
              </label>
            </div>
            <div class="form-error" aria-live="polite"></div>
            <div class="form-actions">
              <button class="button primary" type="submit">이메일 변경</button>
            </div>
          </form>
        </div>
        <div class="account-block">
          <h3>비밀번호 변경</h3>
          <p class="help">변경하면 다른 기기의 로그인 세션은 모두 종료돼요.</p>
          <form id="passwordChangeForm">
            <div class="field-grid">
              <label class="field wide">
                <span>현재 비밀번호</span>
                <input
                  name="currentPassword"
                  type="password"
                  maxlength="72"
                  autocomplete="current-password"
                >
              </label>
              <label class="field">
                <span>새 비밀번호</span>
                <input
                  name="newPassword"
                  type="password"
                  minlength="8"
                  maxlength="72"
                  autocomplete="new-password"
                >
              </label>
              <label class="field">
                <span>새 비밀번호 확인</span>
                <input
                  name="confirmPassword"
                  type="password"
                  minlength="8"
                  maxlength="72"
                  autocomplete="new-password"
                >
              </label>
            </div>
            <div class="form-error" aria-live="polite"></div>
            <div class="form-actions">
              <button class="button primary" type="submit">비밀번호 변경</button>
            </div>
          </form>
        </div>
      </section>`;
  }

  function sessionSettingsTemplate() {
    return `
      <section class="settings-section" data-section="sessions">
        <div class="session-group">
          <h3>현재 세션</h3>
          <div id="currentSession" class="session-list" aria-live="polite">
            <p class="help">세션 정보를 불러오고 있어요.</p>
          </div>
        </div>
        <div class="session-group">
          <div class="session-heading">
            <h3>등록된 세션</h3>
            <button class="mini-button danger" id="endAllSessions" type="button" hidden>
              일괄 종료
            </button>
          </div>
          <div id="registeredSessions" class="session-list" aria-live="polite"></div>
        </div>
      </section>`;
  }

  function sessionRow(session, current = false) {
    const action = current
      ? '<span class="session-current">현재</span>'
      : `<button class="mini-button danger" type="button" data-end-session="${session.id}">종료</button>`;
    return `
      <div class="session-row">
        <div class="session-details">
          <strong>${esc(session.device)}</strong>
          <small>로그인 ${esc(session.createdAt)} · 만료 ${esc(session.expiresAt)}</small>
        </div>
        ${action}
      </div>`;
  }

  async function loadSessions() {
    const current = document.querySelector("#currentSession"),
      registered = document.querySelector("#registeredSessions"),
      endAll = document.querySelector("#endAllSessions");
    if (!current || !registered || !endAll) return;
    try {
      const sessions = await api("/api/sessions");
      current.innerHTML = sessionRow(sessions.current, true);
      registered.innerHTML = sessions.registered.length
        ? sessions.registered.map((session) => sessionRow(session)).join("")
        : '<p class="help session-empty">다른 등록 세션이 없어요.</p>';
      endAll.hidden = sessions.registered.length === 0;
      registered.querySelectorAll("[data-end-session]").forEach((button) =>
        button.addEventListener("click", async () => {
          if (!confirm("이 세션을 종료할까요? 해당 기기에서 다시 로그인해야 해요.")) return;
          try {
            await api(`/api/sessions/${button.dataset.endSession}`, { method: "DELETE" });
            await loadSessions();
            toast("등록된 세션을 종료했어요.");
          } catch (err) {
            toast(err.message, true);
          }
        })
      );
      endAll.onclick = async () => {
        if (!confirm("등록된 세션을 모두 종료할까요? 다른 기기에서 다시 로그인해야 해요.")) return;
        try {
          await api("/api/sessions", { method: "DELETE" });
          await loadSessions();
          toast("등록된 세션을 모두 종료했어요.");
        } catch (err) {
          toast(err.message, true);
        }
      };
    } catch (err) {
      current.innerHTML = `<p class="form-error">${esc(err.message)}</p>`;
      registered.innerHTML = "";
      endAll.hidden = true;
    }
  }

  function pmRow(p) {
    const actions = p.IsBuiltin ? '<small class="muted">기본</small>' : `
        <span class="inline-actions">
          <button class="mini-button" type="button" data-rename-pm="${p.id}">이름 변경</button>
          <button class="mini-button danger" type="button" data-delete-pm="${p.id}">삭제</button>
        </span>`;

    return `
      <div class="pm-row" data-pm-row="${p.id}">
        <span class="pm-check">${uiIcons.check}</span>
        <span>${esc(p.name)}</span>
        ${actions}
      </div>`;
  }

  function currencyRow(c) {
    const name = c.name && c.name !== c.code ? ` <small class="muted">${esc(c.name)}</small>` : "";
    const action = c.isBuiltin ? '<small class="muted">기본</small>' : `
        <button class="mini-button danger" type="button" data-delete-currency="${c.id}">
          삭제
        </button>`;

    return `
      <div class="pm-row">
        <span class="pm-check">${c.isBuiltin ? uiIcons.check : uiIcons.dot}</span>
        <span><strong>${esc(c.code)}</strong>${name}</span>
        ${action}
      </div>`;
  }
  const pendingActions = new WeakMap();
  function beginAction(element) {
    if (pendingActions.has(element)) return false;
    const controls = element.tagName === "FORM"
      ? [...element.querySelectorAll('button[type="submit"], input[type="submit"]'),
        ...document.querySelectorAll(`button[form="${element.id}"][type="submit"]`)]
      : [element];
    pendingActions.set(element, controls.map((control) => [control, control.disabled]));
    controls.forEach((control) => { control.disabled = true; });
    element.setAttribute("aria-busy", "true");
    return true;
  }
  function endAction(element) {
    (pendingActions.get(element) || []).forEach(([control, disabled]) => { control.disabled = disabled; });
    pendingActions.delete(element);
    element.removeAttribute("aria-busy");
  }
  function showFormError(error, message) {
    if (error?.isConnected) error.textContent = message;
    else toast(message, true);
  }

  function bindSettings() {
    const saveArea = document.querySelector("#settingsSaveArea");
    const tabsUsingSettingsSave = new Set(["profile", "notifications", "channels"]);
    document.querySelectorAll("[data-tab]").forEach((b) =>
      b.addEventListener("click", () => {
        document.querySelectorAll("[data-tab]").forEach((x) =>
          x.classList.toggle("active", x === b)
        );
        document.querySelectorAll("[data-section]").forEach((x) =>
          x.classList.toggle("active", x.dataset.section === b.dataset.tab)
        );
        saveArea.hidden = !tabsUsingSettingsSave.has(b.dataset.tab);
        if (b.dataset.tab === "sessions") loadSessions();
      })
    );
    document.querySelector("#settingsForm").addEventListener("submit", async (e) => {
      e.preventDefault();
      const form = e.currentTarget;
      const error = form.querySelector("#settingsError");
      const f = new FormData(form);
      const body = {
        name: f.get("name"),
        currency: f.get("currency"),
        discordEnabled: f.has("discordEnabled"),
        discordWebhook: form.elements.discordWebhook.value,
        telegramEnabled: f.has("telegramEnabled"),
        telegramBotToken: form.elements.telegramBotToken.value,
        telegramChatId: form.elements.telegramChatId.value,
        pwaEnabled: f.has("pwaEnabled"),
        notifyDays: Number(f.get("notifyDays")),
        notifyUpcoming: f.has("notifyUpcoming"),
        notifyChanges: state.settings.NotifyChanges,
        notifyMonthly: state.settings.NotifyMonthly,
      };
      if (!beginAction(form)) return;
      error.textContent = "";
      try {
        await api("/api/settings", { method: "PUT", body });
        if (form.isConnected) closeModal();
        await refresh();
        toast("설정을 저장했어요.");
      } catch (err) {
        showFormError(error, err.message);
      } finally {
        endAction(form);
      }
    });
    bindAccountSettings();
    bindCatalogSettings();
    bindIntegrationSettings();
    bindDataSettings();
  }

  function bindAccountSettings() {
    document.querySelector("#emailChangeForm").addEventListener("submit", async (e) => {
      e.preventDefault();
      const form = e.currentTarget;
      const f = new FormData(form),
        error = form.querySelector(".form-error"),
        email = String(f.get("email")).trim();
      error.textContent = "";
      if (!email || !f.get("currentPassword")) {
        error.textContent = "이메일과 현재 비밀번호를 입력해 주세요.";
        return;
      }
      if (!beginAction(form)) return;
      try {
        await api("/api/account/email", {
          method: "PUT",
          body: { email, currentPassword: f.get("currentPassword") },
        });
        state.user.Email = email.toLowerCase();
        form.elements.currentPassword.value = "";
        toast("로그인 이메일을 변경했어요.");
      } catch (err) {
        showFormError(error, err.message);
      } finally {
        endAction(form);
      }
    });
    document.querySelector("#passwordChangeForm").addEventListener("submit", async (e) => {
      e.preventDefault();
      const form = e.currentTarget;
      const f = new FormData(form),
        error = form.querySelector(".form-error"),
        currentPassword = String(f.get("currentPassword")),
        newPassword = String(f.get("newPassword")),
        confirmPassword = String(f.get("confirmPassword"));
      error.textContent = "";
      if (!currentPassword || !newPassword) {
        error.textContent = "현재 비밀번호와 새 비밀번호를 입력해 주세요.";
        return;
      }
      if (newPassword !== confirmPassword) {
        error.textContent = "새 비밀번호 확인이 일치하지 않아요.";
        return;
      }
      if (!beginAction(form)) return;
      try {
        await api("/api/account/password", {
          method: "PUT",
          body: { currentPassword, newPassword },
        });
        form.reset();
        toast("비밀번호를 변경했어요. 다른 기기에서는 다시 로그인해 주세요.");
      } catch (err) {
        showFormError(error, err.message);
      } finally {
        endAction(form);
      }
    });
  }

  function bindCatalogSettings() {
    document.querySelector("#addMethod").addEventListener("click", async () => {
      const input = document.querySelector("#newMethod");
      try {
        await api("/api/payment-methods", { method: "POST", body: { name: input.value } });
        await reloadAndSettings("payments");
        toast("결제수단을 추가했어요.");
      } catch (err) {
        toast(err.message, true);
      }
    });
    document.querySelector("#addCurrency").addEventListener("click", async () => {
      const input = document.querySelector("#newCurrency");
      try {
        await api("/api/currencies", { method: "POST", body: { code: input.value } });
        await reloadAndSettings("currencies");
        toast("통화를 추가했어요.");
      } catch (err) {
        toast(err.message, true);
      }
    });
    document.querySelectorAll("[data-delete-currency]").forEach((b) =>
      b.addEventListener("click", async () => {
        const c = (state.currencies || []).find((x) => x.id === Number(b.dataset.deleteCurrency));
        if (!confirm(`${c.code} 통화를 삭제할까요?`)) return;
        try {
          await api(`/api/currencies/${c.id}`, { method: "DELETE" });
          await reloadAndSettings("currencies");
          toast("통화를 정리했어요.");
        } catch (err) {
          toast(err.message, true);
        }
      })
    );
    document.querySelectorAll("[data-rename-pm]").forEach((b) =>
      b.addEventListener("click", async () => {
        const p = state.paymentMethods.find((x) => x.id === Number(b.dataset.renamePm));
        const name = prompt("새 결제수단 이름을 입력해 주세요.", p.name);
        if (!name || name === p.name) return;
        try {
          await api(`/api/payment-methods/${p.id}`, { method: "PUT", body: { name } });
          await reloadAndSettings("payments");
          toast("이름을 바꿨어요.");
        } catch (err) {
          toast(err.message, true);
        }
      })
    );
    document.querySelectorAll("[data-delete-pm]").forEach((b) =>
      b.addEventListener("click", async () => {
        const p = state.paymentMethods.find((x) => x.id === Number(b.dataset.deletePm));
        if (!confirm(`${p.name}을(를) 삭제할까요?`)) return;
        try {
          await api(`/api/payment-methods/${p.id}`, { method: "DELETE" });
          await reloadAndSettings("payments");
          toast("결제수단을 정리했어요.");
        } catch (err) {
          toast(err.message, true);
        }
      })
    );
  }

  function bindIntegrationSettings() {
    document.querySelectorAll("[data-test]").forEach((b) =>
      b.addEventListener("click", async () => {
        const f = new FormData(document.querySelector("#settingsForm"));
        const body = { channel: b.dataset.test };
        if (b.dataset.test === "discord") body.discordWebhook = f.get("discordWebhook") || "";
        if (b.dataset.test === "telegram") {
          body.telegramBotToken = f.get("telegramBotToken") || "";
          body.telegramChatId = f.get("telegramChatId") || "";
        }
        if (!beginAction(b)) return;
        try {
          await api("/api/notifications/test", { method: "POST", body });
          toast("SubManager 알림 테스트를 보냈어요.");
        } catch (err) {
          toast(err.message, true);
        } finally {
          endAction(b);
        }
      })
    );
    document.querySelectorAll("input[name=discordEnabled], input[name=telegramEnabled], input[name=pwaEnabled]").forEach((input) =>
      input.addEventListener("change", () => {
        const fields = modalBody.querySelector(`[data-integration-fields="${input.name}"]`);
        if (fields) {
          fields.hidden = !input.checked;
          fields.querySelectorAll("input").forEach((field) => { field.disabled = !input.checked; });
        }
      })
    );
    document.querySelector("#installPWA")?.addEventListener("click", async () => {
      if (!deferredInstallPrompt) return;
      deferredInstallPrompt.prompt();
      await deferredInstallPrompt.userChoice;
      deferredInstallPrompt = null;
      openSettings();
      document.querySelector('[data-tab="channels"]')?.click();
    });
    document.querySelector("#enablePWAPush")?.addEventListener("click", async () => {
      try {
        if (!pwaPushSupported()) throw new Error("이 브라우저에서는 PWA 푸시 알림을 지원하지 않아요.");
        const permission = await Notification.requestPermission();
        if (permission !== "granted") throw new Error("푸시 알림 권한을 허용해 주세요.");
        const { publicKey } = await api("/api/pwa/vapid-public");
        const registration = await navigator.serviceWorker.ready;
        const subscription = await registration.pushManager.getSubscription() || await registration.pushManager.subscribe({
          userVisibleOnly: true,
          applicationServerKey: urlBase64ToUint8Array(publicKey),
        });
        const subscriptionJSON = subscription.toJSON();
        await api("/api/pwa/subscriptions", {
          method: "POST",
          body: {
            endpoint: subscription.endpoint,
            keys: {
              p256dh: subscriptionJSON.keys?.p256dh,
              auth: subscriptionJSON.keys?.auth,
            },
          },
        });
        toast("이 기기의 PWA 결제 알림을 켰어요.");
      } catch (err) {
        toast(err.message || "PWA 푸시 알림을 켜지 못했어요.", true);
      }
    });
    document.querySelector("#disablePWAPush")?.addEventListener("click", async () => {
      try {
        const registration = await navigator.serviceWorker.ready;
        const subscription = await registration.pushManager.getSubscription();
        if (!subscription) {
          toast("이 기기에는 켜진 푸시 알림이 없어요.");
          return;
        }
        const endpoint = subscription.endpoint;
        await subscription.unsubscribe();
        await api("/api/pwa/subscriptions", { method: "DELETE", body: { endpoint } });
        toast("이 기기의 PWA 결제 알림을 껐어요.");
      } catch (err) {
        toast(err.message || "PWA 푸시 알림을 끄지 못했어요.", true);
      }
    });
  }

  function bindDataSettings() {
    document.querySelector("#logoutButton").addEventListener("click", async () => {
      try {
        await api("/auth/logout", { method: "POST", body: {} });
        location.replace("/");
      } catch (err) {
        toast(err.message, true);
      }
    });
    document.querySelector("#exportData").addEventListener("click", async () => {
      try {
        const includeNotificationCredentials = document.querySelector(
          "#includeNotificationCredentials",
        ).checked;
        const query = new URLSearchParams({
          includeNotificationCredentials: String(includeNotificationCredentials),
        });
        const res = await fetch(`/api/data/export?${query}`);
        if (!res.ok) throw new Error("백업을 만들지 못했어요.");
        const blob = await res.blob();
        const url = URL.createObjectURL(blob);
        const a = document.createElement("a");
        a.href = url;
        a.download = `submanager-backup-${localDate()}.json`;
        a.click();
        URL.revokeObjectURL(url);
        const detail = includeNotificationCredentials
          ? "알림 연동 정보 포함"
          : "알림 연동 정보 제외";
        toast(`JSON 백업을 만들었어요. (${detail})`);
      } catch (err) {
        toast(err.message, true);
      }
    });
    document.querySelector("#importData").addEventListener("change", async (e) => {
      const file = e.target.files[0];
      if (!file) return;
      if (!confirm("현재 구독 데이터를 선택한 백업으로 교체할까요?")) {
        e.target.value = "";
        return;
      }
      try {
        const res = await fetch("/api/data/import", {
          method: "POST",
          headers: { "Content-Type": "application/json", "Accept": "application/json" },
          body: await file.text(),
        });
        const data = await res.json();
        if (!res.ok) throw new Error(data.error || "가져오지 못했어요.");
        closeModal();
        await refresh();
        toast("JSON 백업을 가져왔어요.");
      } catch (err) {
        toast(err.message, true);
      } finally {
        e.target.value = "";
      }
    });
  }
  async function reloadAndSettings(tab) {
    await loadFreshState();
    openSettings();
    document.querySelector(`[data-tab="${tab}"]`)?.click();
  }

  async function api(url, { method = "GET", body } = {}) {
    const opts = { method, headers: { "Accept": "application/json" } };
    if (body !== undefined) {
      opts.headers["Content-Type"] = "application/json";
      opts.body = JSON.stringify(body);
    }
    const res = await fetch(url, opts);
    let data;
    try {
      data = await res.json();
    } catch {
      if (res.status === 401) location.replace("/");
      throw new Error("서버 응답을 읽지 못했어요. 잠시 후 다시 시도해 주세요.");
    }
    if (res.status === 401) {
      location.replace("/");
      throw new Error("로그인이 필요해요");
    }
    if (!res.ok) throw new Error(data.error || "요청을 처리하지 못했어요.");
    return data;
  }
  async function loadFreshState() {
    const request = ++stateRequest;
    const nextState = await api("/api/state");
    if (request === stateRequest) replaceState(nextState);
  }
  async function refresh() {
    await loadFreshState();
    const position = currentView === "subscriptions" ? subscriptionPosition() : null;
    render();
    if (position) restoreSubscriptionPosition(position);
  }
  function toast(message, error = false) {
    const el = document.createElement("div");
    el.className = `toast${error ? " error" : ""}`;
    el.textContent = message;
    document.querySelector("#toasts").append(el);
    setTimeout(() => el.remove(), 3200);
  }

  document.addEventListener("click", (e) => {
    if (e.target.closest("[data-reset-subscription-filters]")) {
      subscriptionQuery = "";
      subscriptionCategory = "";
      subscriptionStatus = "active";
      renderSubscriptions();
      main.querySelector("#subscriptionSearch")?.focus({ preventScroll: true });
      return;
    }
    const quickSkip = e.target.closest("[data-quick-skip]");
    if (quickSkip) {
      quickSkipSubscription(quickSkip);
      return;
    }
    const status = e.target.closest("[data-sub-status]");
    if (status) {
      subscriptionStatus = status.dataset.subStatus;
      document.querySelectorAll("[data-sub-status]").forEach((button) => button.setAttribute("aria-pressed", String(button === status)));
      renderSubscriptionResults();
      return;
    }
    if (e.target.closest("[data-add-subscription]")) {
      openServicePicker();
      return;
    }
    if (e.target.closest("[data-export-ics]")) {
      openICSExport();
      return;
    }
    const upcomingSwitch = e.target.closest("[data-upcoming-view]");
    if (upcomingSwitch) {
      upcomingView = upcomingSwitch.dataset.upcomingView;
      renderUpcoming();
      return;
    }
    const calendarMove = e.target.closest("[data-calendar-move]");
    if (calendarMove) {
      const next = new Date(calendarYear, calendarMonth + Number(calendarMove.dataset.calendarMove), 1);
      calendarYear = next.getFullYear();
      calendarMonth = next.getMonth();
      renderUpcomingCalendar();
      return;
    }
    if (e.target.closest("[data-calendar-today]")) {
      const now = new Date();
      calendarYear = now.getFullYear();
      calendarMonth = now.getMonth();
      renderUpcomingCalendar();
      return;
    }
    const calendarDate = e.target.closest("[data-calendar-date]");
    if (calendarDate) {
      openCalendarDate(calendarDate.dataset.calendarDate);
      return;
    }
    const category = e.target.closest("[data-sub-category]");
    if (category) {
      subscriptionCategory = category.dataset.subCategory;
      document.querySelectorAll("[data-sub-category]").forEach((button) =>
        button.setAttribute("aria-pressed", String(button === category))
      );
      renderSubscriptionResults();
      return;
    }
    const currency = e.target.closest("[data-currency]");
    if (currency) {
      e.preventDefault();
      e.stopPropagation();
      selectedCurrency = currency.dataset.currency;
      render(currentView);
      main.querySelector(`[data-currency="${CSS.escape(selectedCurrency)}"]`)?.focus({ preventScroll: true });
      return;
    }
    const view = e.target.closest("[data-view]");
    if (view) {
      render(view.dataset.view);
      return;
    }
    const edit = e.target.closest("[data-edit-sub]");
    if (edit) {
      const s = state.subscriptions.find((x) => x.id === Number(edit.dataset.editSub));
      if (s) s.Status === "cancelled" ? openCancelledSubscription(s) : openSubForm(null, s);
      return;
    }
    const pick = e.target.closest("[data-service]");
    if (pick) {
      const s = pick.dataset.service === "manual"
        ? null
        : state.services.find((x) => String(x.ID) === pick.dataset.service);
      openSubForm(s);
      return;
    }
    if (e.target.closest("[data-close-modal]")) closeModal();
  });
  document.addEventListener("change", (e) => {
    if (e.target.matches("#subscriptionSort")) {
      subscriptionSort = e.target.value;
      renderSubscriptionResults();
    }
  });
  document.addEventListener("input", (e) => {
    if (e.target.matches("#subscriptionSearch")) {
      subscriptionQuery = e.target.value;
      renderSubscriptionResults();
    }
  });
  document.addEventListener("submit", (e) => {
    if (e.target.matches("#icsExportForm")) {
      e.preventDefault();
      downloadICS(e.target);
    }
  });
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && !backdrop.hidden) closeModal();
    if (e.key === "Tab" && !backdrop.hidden) trapModalFocus(e);
    if ((e.key === "Enter" || e.key === " ") && e.target.matches(".chart-card[data-view]")) {
      e.preventDefault();
      render("stats");
    }
  });
  backdrop.addEventListener("mousedown", (e) => {
    if (e.target === backdrop) closeModal();
  });
  document.querySelector("#addSubscriptionButton").addEventListener("click", openServicePicker);
  themeButton.addEventListener("click", () => {
    const current = document.documentElement.dataset.themePreference || "system",
      next = themeModes[(themeModes.indexOf(current) + 1) % themeModes.length];
    applyTheme(next, true);
    toast(`${themeLabels[next]} 테마로 전환했어요.`);
  });
  themeMedia.addEventListener("change", () => {
    if (document.documentElement.dataset.themePreference === "system") applyTheme("system");
  });
  document.querySelector("#settingsButton").addEventListener("click", openSettings);
  if ("serviceWorker" in navigator) {
    window.addEventListener("load", () => navigator.serviceWorker.register("/sw.js").catch(() => {}));
  }
  applyTheme(document.documentElement.dataset.themePreference || "system");
  render();
})();
