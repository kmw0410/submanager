(() => {
  'use strict';
  let state = window.__INITIAL_STATE__;
  let currentView = 'dashboard';
  let selectedCurrency = 'all';
  const main = document.querySelector('#main');
  const backdrop = document.querySelector('#modalBackdrop');
  const modal = backdrop.querySelector('.modal');
  const modalBody = document.querySelector('#modalBody');
  const modalTitle = document.querySelector('#modalTitle');
  const modalKicker = document.querySelector('#modalKicker');
  const themeButton = document.querySelector('#themeButton');
  const themeMedia = matchMedia('(prefers-color-scheme: dark)');
  const themeModes = ['system','dark','light'];
  const themeLabels = {system:'시스템',dark:'다크',light:'라이트'};
  const themeIcons = {
    system:'<svg aria-hidden="true" viewBox="0 0 24 24"><rect x="3" y="4" width="18" height="13" rx="2"/><path d="M8 21h8M12 17v4"/></svg>',
    dark:'<svg aria-hidden="true" viewBox="0 0 24 24"><path d="M20.4 15.2A8.5 8.5 0 0 1 8.8 3.6 8.5 8.5 0 1 0 20.4 15.2Z"/></svg>',
    light:'<svg aria-hidden="true" viewBox="0 0 24 24"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.93 4.93l1.42 1.42M17.65 17.65l1.42 1.42M2 12h2M20 12h2M4.93 19.07l1.42-1.42M17.65 6.35l1.42-1.42"/></svg>'
  };
  const uiIcons = {
    plus:'<svg aria-hidden="true" viewBox="0 0 24 24"><path d="M12 5v14M5 12h14"/></svg>',
    check:'<svg aria-hidden="true" viewBox="0 0 24 24"><path d="m5 12 4 4L19 6"/></svg>',
    dot:'<svg aria-hidden="true" viewBox="0 0 24 24"><circle cx="12" cy="12" r="2" fill="currentColor" stroke="none"/></svg>'
  };

  function applyTheme(preference,persist=false){
    if(!themeModes.includes(preference))preference='system';
    const resolved=preference==='system'?(themeMedia.matches?'dark':'light'):preference;
    document.documentElement.dataset.theme=resolved;
    document.documentElement.dataset.themePreference=preference;
    document.querySelector('meta[name="theme-color"]').content=resolved==='dark'?'#09090B':'#F6F7F5';
    themeButton.innerHTML=themeIcons[preference];
    const next=themeModes[(themeModes.indexOf(preference)+1)%themeModes.length];
    themeButton.title=`테마: ${themeLabels[preference]} · 클릭하여 ${themeLabels[next]}로 변경`;
    themeButton.setAttribute('aria-label',themeButton.title);
    if(persist){try{preference==='system'?localStorage.removeItem('submanager-theme'):localStorage.setItem('submanager-theme',preference)}catch{}}
  }

  const esc = value => String(value ?? '').replace(/[&<>'"]/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;',"'":'&#39;','"':'&quot;'}[c]));
  const currencyDigits = currency => state.currencies?.find(c=>c.code===currency)?.digits ?? 2;
  const amountValue = (value,currency) => (Number(value||0)/(10**currencyDigits(currency))).toFixed(currencyDigits(currency));
  const amountMinorUnits = (value,currency) => {const digits=currencyDigits(currency),match=String(value).trim().match(new RegExp(`^\\d+(?:\\.(\\d{0,${digits}}))?$`));if(!match)return null;const [whole,fraction='']=String(value).trim().split('.');const minor=Number(whole)*(10**digits)+Number(fraction.padEnd(digits,'0')||0);return Number.isSafeInteger(minor)?minor:null};
  const money = (value, currency='KRW') => {const amount=Number(value||0)/(10**currencyDigits(currency));try{return new Intl.NumberFormat('en-US',{style:'currency',currency,minimumFractionDigits:currencyDigits(currency),maximumFractionDigits:currencyDigits(currency)}).format(amount)}catch{return `${currency} ${amount.toLocaleString('en-US',{minimumFractionDigits:currencyDigits(currency),maximumFractionDigits:currencyDigits(currency)})}`}};
  const cycle = value => value === 'yearly' ? '매년' : '매월';
  const activeSubs = () => (state.subscriptions || []).filter(s => s.Status === 'active');
  const visibleMethods = () => (state.paymentMethods || []).filter(p => !p.Archived);
  const visibleCurrencies = () => (state.currencies || []).filter(c => !c.archived);
  const today = () => { const d = new Date(); d.setHours(0,0,0,0); return d; };
  const dateOf = value => new Date(`${value}T00:00:00`);
  const localDate = () => { const d=new Date(), p=n=>String(n).padStart(2,'0'); return `${d.getFullYear()}-${p(d.getMonth()+1)}-${p(d.getDate())}`; };
  const daysUntil = value => Math.max(0, Math.round((dateOf(value) - today()) / 86400000));
  const dueText = value => { const n=daysUntil(value); return n===0?'오늘':n===1?'내일':`${n}일 뒤`; };

  function syncSummary() {
    document.querySelector('#activeCount').textContent = `${state.stats.ActiveCount}개`;
    document.querySelector('#upcomingCount').textContent = `${state.stats.UpcomingCount}건`;
    const currencies=state.stats.currencies||[];
    document.querySelector('#monthTotal').innerHTML = currencyAmountList(currencies,'monthTotal');
    document.querySelector('#yearTotal').innerHTML = currencyAmountList(currencies,'yearEstimate');
  }

  function currencyAmountList(currencies,field){return currencies.length?currencies.map(c=>`<span>${esc(money(c[field],c.currency))}</span>`).join(''):`<span>${esc(money(0,state.user.Currency||'KRW'))}</span>`}

  function currencyTabs(){const currencies=state.stats.currencies||[];if(selectedCurrency!=='all'&&!currencies.some(c=>c.currency===selectedCurrency))selectedCurrency='all';return `<div class="currency-tabs"><button type="button" data-currency="all" class="${selectedCurrency==='all'?'active':''}">전체</button>${currencies.map(c=>`<button type="button" data-currency="${esc(c.currency)}" class="${selectedCurrency===c.currency?'active':''}">${esc(c.currency)}</button>`).join('')}</div>`}

  function render(view = currentView) {
    currentView = view;
    syncSummary();
    const renders = { dashboard: renderDashboard, subscriptions: renderSubscriptions, upcoming: renderUpcoming, stats: renderStats };
    (renders[view] || renderDashboard)();
    main.focus({preventScroll:true});
  }

  function pageHead(title, subtitle, action='') {
    return `<div class="section-head"><div><h2>${esc(title)}</h2>${subtitle?`<p>${esc(subtitle)}</p>`:''}</div>${action}</div>`;
  }

  function renderDashboard() {
    const subs = activeSubs().slice(0, 6);
    const allCurrencies=state.stats.currencies||[];
    const tabs=currencyTabs();
    const series=selectedCurrency==='all'?allCurrencies:allCurrencies.filter(c=>c.currency===selectedCurrency);
    const selected=series[0];
    main.innerHTML = `<div class="page">
      <section class="welcome"><h1>${esc(state.stats.Greeting)}</h1><p>${esc(state.stats.Summary)}</p></section>
      <div class="section-head"><div><h2>월별 지출</h2><p>최근 6개월 구독비 흐름</p></div><div class="chart-actions">${tabs}<button class="text-button" type="button" data-view="stats">자세히 보기</button></div></div>
      <section class="chart-card" data-view="stats" tabindex="0" role="button" aria-label="월별 지출 상세 보기"><div class="chart-top"><div><span class="eyebrow">이번 달 구독비</span><strong class="chart-total ${selectedCurrency==='all'?'multi':''}">${selectedCurrency==='all'?currencyAmountList(allCurrencies,'monthTotal'):money(selected?.monthTotal||0,selectedCurrency)}</strong></div><span class="chart-delta">${selectedCurrency==='all'?'통화별 합계':deltaText(selected)}</span></div>${chart(series,state.stats.months)}</section>
      ${pageHead('이번 달 구독',`${state.stats.ActiveCount}개의 활성 구독`,'<button class="text-button" type="button" data-view="subscriptions">전체 보기</button>')}
      <section class="subscription-grid">${subs.length ? subs.map(subCard).join('') : empty('현재 구독 중인 항목이 없어요.','구독 추가를 눌러 추가해 보세요.')}</section>
    </div>`;
  }

  function deltaText(stat) {
    if(!stat)return '데이터가 없어요';const d=stat.delta;
    if (d === 0) return '지난달과 비슷해요';
    return d > 0 ? `지난달보다 +${money(d,stat.currency)}` : `지난달보다 -${money(-d,stat.currency)}`;
  }

  function chart(series, labels) {
    const safeSeries=series.length?series:[{currency:state.user.Currency||'KRW',monthlyTotals:labels.map(()=>0)}];const w=720,h=145,p=10,colors=['#9AB8A8','#AAB5D8','#C6A98D','#B2A7D6','#D1B0B8'];
    const paths=safeSeries.map((s,index)=>{const values=s.monthlyTotals,max=Math.max(...values,1),min=Math.min(...values,0),range=Math.max(max-min,1);const pts=values.map((v,i)=>({x:p+i*(w-p*2)/(values.length-1),y:p+(max-v)*(h-p*2)/range,v,label:labels[i]}));const line=pts.map((point,i)=>`${i?'L':'M'} ${point.x.toFixed(1)} ${point.y.toFixed(1)}`).join(' ');const area=`${line} L ${pts[pts.length-1].x} ${h} L ${pts[0].x} ${h} Z`;const color=colors[index%colors.length];return `${safeSeries.length===1?`<path class="chart-area" d="${area}"/>`:''}<path class="chart-line" style="stroke:${color}" vector-effect="non-scaling-stroke" d="${line}"/>${pts.map(point=>`<circle class="chart-dot" style="stroke:${color}" vector-effect="non-scaling-stroke" cx="${point.x}" cy="${point.y}" r="3.2"><title>${s.currency} · ${point.label} ${money(point.v,s.currency)}</title></circle>`).join('')}`}).join('');
    return `<div class="chart-wrap"><div class="chart-legend">${safeSeries.map((s,i)=>`<span><i style="background:${colors[i%colors.length]}"></i>${esc(s.currency)}</span>`).join('')}</div><svg viewBox="0 0 ${w} ${h}" preserveAspectRatio="none">${paths}</svg><div class="chart-labels">${labels.map(esc).map(x=>`<span>${x}</span>`).join('')}</div></div>`;
  }

  function subCard(s) {
    return `<button class="sub-card ${s.Skipped?'skipped':''} ${s.IsTrial?'trial':''}" type="button" data-edit-sub="${s.id}"><span class="sub-content"><span class="sub-title">${esc(s.ServiceName)}</span><span class="sub-meta ${s.Skipped?'skip-status':''}">${s.Skipped?'이번 달 결제 건너뜀':s.IsTrial?`무료 체험 · ${s.BillingDate.replaceAll('-','.')}부터 결제`:`${esc(s.Category || '기타')} · ${cycle(s.BillingCycle)}`}</span></span><span class="sub-price">${money(s.amount,s.Currency)}<span>${s.IsTrial?'체험 후 결제':s.NextPayment.replaceAll('-','.')}</span></span></button>`;
  }
  function empty(title,body){return `<div class="empty"><span class="empty-icon">${uiIcons.plus}</span><strong>${esc(title)}</strong><span>${esc(body)}</span></div>`}

  function renderSubscriptions() {
    const subs=activeSubs();
    main.innerHTML=`<div class="page"><section class="welcome"><h1>내 구독</h1><p>지금 이용 중인 서비스를 모아봤어요.</p></section>${pageHead(`활성 구독 ${subs.length}개`,'항목을 누르면 내용을 수정할 수 있어요.')}<section class="list-card">${subs.length?`<div class="list-row list-labels"><span>서비스</span><span>금액 / 주기</span><span>결제수단</span><span>다음 결제</span></div>${subs.map(s=>`<button class="list-row ${s.Skipped?'skipped':''} ${s.IsTrial?'trial':''}" type="button" data-edit-sub="${s.id}"><span class="service-cell"><span><strong>${esc(s.ServiceName)}</strong><span class="${s.Skipped?'skip-status skip-status-mobile':''}">${s.Skipped?'이번 달 결제 건너뜀':s.IsTrial?'무료 체험 중':esc(s.Category||'기타')}</span></span></span><span><strong>${money(s.amount,s.Currency)}</strong><br><small class="muted">${cycle(s.BillingCycle)}</small></span><span class="muted">${esc(s.PaymentMethodName)}</span><span><span class="status-pill ${s.Skipped?'skip-status':''}">${s.Skipped?'이번 달 결제 건너뜀':s.IsTrial?`${s.BillingDate.slice(5).replace('-','.')}부터 결제`:`${dueText(s.NextPayment)} · ${s.NextPayment.slice(5).replace('-','.')}`}</span></span></button>`).join('')}`:empty('현재 구독 중인 항목이 없어요.','구독 추가를 눌러 추가해 보세요.')}</section></div>`;
  }

  function renderUpcoming() {
    const subs=activeSubs().filter(s=>!s.Skipped).sort((a,b)=>a.NextPayment.localeCompare(b.NextPayment));
    const groups={}; subs.forEach(s=>{const d=daysUntil(s.NextPayment);const k=d<=7?'이번 주':d<=31?'이번 달':'그 이후';(groups[k]??=[]).push(s)});
    main.innerHTML=`<div class="page"><section class="welcome"><h1>결제 예정</h1><p>가까운 결제부터 차례로 알려드려요.</p></section><div class="upcoming-groups">${subs.length?Object.entries(groups).map(([k,items])=>`<section class="date-group"><h3>${k}</h3>${items.map(s=>`<button class="upcoming-row ${s.IsTrial?'trial':''}" type="button" data-edit-sub="${s.id}"><span class="sub-content"><span class="sub-title">${esc(s.ServiceName)}</span><span class="sub-meta">${s.IsTrial?'무료 체험 · ':''}${esc(s.PaymentMethodName)} · ${s.NextPayment.replaceAll('-','.')}</span></span><strong>${money(s.amount,s.Currency)}</strong><span class="due">${s.IsTrial?'첫 결제 '+dueText(s.NextPayment):dueText(s.NextPayment)}</span></button>`).join('')}</section>`).join(''):empty('예정된 결제가 없어요','결제를 건너뛴 구독은 이번 달 목록에서 빠져요.')}</div></div>`;
  }

  function renderStats() {
    const allCurrencies=state.stats.currencies||[];const tabs=currencyTabs();const series=selectedCurrency==='all'?allCurrencies:allCurrencies.filter(c=>c.currency===selectedCurrency);const selected=series[0];const statCards=selectedCurrency==='all'?allCurrencies.map(c=>`<div class="stat"><span>${esc(c.currency)} · 이번 달</span><strong>${money(c.monthTotal,c.currency)}</strong></div>`).join(''):`<div class="stat"><span>이번 달</span><strong>${money(selected?.monthTotal||0,selectedCurrency)}</strong></div><div class="stat"><span>최근 6개월 최고</span><strong>${money(Math.max(...(selected?.monthlyTotals||[0])),selectedCurrency)}</strong></div><div class="stat"><span>최근 6개월 최저</span><strong>${money(Math.min(...(selected?.monthlyTotals||[0])),selectedCurrency)}</strong></div>`;
    main.innerHTML=`<div class="page"><button class="back-button" type="button" data-view="dashboard" aria-label="대시보드로 돌아가기"><svg aria-hidden="true" viewBox="0 0 24 24"><path d="m15 18-6-6 6-6"/></svg>돌아가기</button><section class="welcome stats-welcome"><h1>월별 지출</h1><p>통화를 섞지 않고 각각의 흐름을 보여드려요.</p></section><div class="section-head"><div><h2>통화별 통계</h2></div>${tabs}</div><div class="stat-grid">${statCards||'<div class="stat"><span>이번 달</span><strong>'+money(0,state.user.Currency)+'</strong></div>'}</div><section class="chart-card"><div class="chart-top"><div><span class="eyebrow">최근 6개월</span><strong class="chart-total">${selectedCurrency==='all'?'통화별 지출 흐름':deltaText(selected)}</strong></div></div>${chart(series,state.stats.months)}</section>${pageHead('구독별 월 예상','연간 구독은 같은 통화의 월평균으로 표시해요.')}<section class="list-card">${activeSubs().sort((a,b)=>a.Currency.localeCompare(b.Currency)||b.amount-a.amount).map(s=>{const monthly=s.BillingCycle==='yearly'?Math.round(s.amount/12):s.amount;const currencyTotal=(state.stats.currencies||[]).find(c=>c.currency===s.Currency)?.monthTotal||0;return `<div class="list-row"><span class="service-cell"><span><strong>${esc(s.ServiceName)}</strong><span>${cycle(s.BillingCycle)}</span></span></span><strong>${money(monthly,s.Currency)}</strong><span class="muted">${esc(s.Category||'기타')}</span><span class="muted">${currencyTotal?Math.round(monthly/currencyTotal*100):0}% · ${esc(s.Currency)}</span></div>`}).join('')||empty('표시할 데이터가 없어요','구독을 추가하면 분석을 시작해요.')}</section></div>`;
  }

  function openModal(title,kicker='') { modalTitle.textContent=title;modalKicker.textContent=kicker;backdrop.hidden=false;document.body.style.overflow='hidden';setTimeout(()=>backdrop.querySelector('input,button,select')?.focus(),0); }
  function closeModal(){backdrop.hidden=true;document.body.style.overflow='';modal.classList.remove('wide');modalBody.innerHTML='';}

  function openServicePicker(){
    openModal('어떤 서비스를 이용하고 있나요?','구독 추가 · 1/2');
    modalBody.innerHTML=`<div class="search"><svg viewBox="0 0 24 24"><circle cx="11" cy="11" r="7"/><path d="m16 16 4 4"/></svg><input id="serviceSearch" type="search" placeholder="서비스 검색" autocomplete="off"></div><div class="service-picker" id="servicePicker">${serviceOptions('')}</div>`;
    const search=document.querySelector('#serviceSearch');search.addEventListener('input',()=>{document.querySelector('#servicePicker').innerHTML=serviceOptions(search.value)});
  }
  function serviceOptions(query){const q=query.trim().toLowerCase();const list=state.services.filter(s=>s.Name.toLowerCase().includes(q));return `${list.map(s=>`<button class="service-option" type="button" data-service="${s.ID}"><span><strong>${esc(s.Name)}</strong><small>${esc(s.Category)}</small></span></button>`).join('')}<button class="service-option manual-option" type="button" data-service="manual"><span class="inline-icon">${uiIcons.plus}</span> 직접 추가</button>`}

  function openSubForm(service=null, existing=null){
    const edit=!!existing;const s=existing||{ServiceName:service?.Name||'',Icon:service?.Icon||'',Color:service?.Color||'#9AB8A8',Category:service?.Category||'',Currency:service?.Currency||'KRW',BillingCycle:service?.BillingCycle||'monthly',amount:'',BillingDate:localDate(),TrialEndsAt:'',IsTrial:false,PaymentMethodID:visibleMethods()[0]?.id||'',Memo:'',ServiceID:service?Number(service.ID):null};
    openModal(edit?`${s.ServiceName} 수정`:'구독 정보를 알려주세요',edit?'구독 관리':'구독 추가 · 2/2');modal.classList.add('wide');
    modalBody.innerHTML=`<form id="subscriptionForm"><div class="field-grid">
      <label class="field wide"><span>서비스명 *</span><input name="serviceName" required maxlength="80" value="${esc(s.ServiceName)}"></label>
      <label class="field"><span>금액 *</span><input name="amount" required type="number" min="0" step="${1/(10**currencyDigits(s.Currency))}" inputmode="decimal" value="${amountValue(s.amount,s.Currency)}" placeholder="14900"></label>
      <label class="field"><span>통화 *</span><select name="currency">${visibleCurrencies().map(c=>`<option value="${esc(c.code)}" ${s.Currency===c.code?'selected':''}>${esc(c.code)}${c.name&&c.name!==c.code?' · '+esc(c.name):''}</option>`).join('')}</select></label>
      <label class="field"><span>결제 주기 *</span><select name="billingCycle"><option value="monthly" ${s.BillingCycle==='monthly'?'selected':''}>매월</option><option value="yearly" ${s.BillingCycle==='yearly'?'selected':''}>매년</option></select></label>
      <label class="field"><span id="billingDateLabel">${s.TrialEndsAt?'첫 결제일':'결제일'} *</span><input name="billingDate" required type="date" value="${esc(s.BillingDate || s.NextPayment || localDate())}"></label>
      <label class="field"><span>결제수단 *</span><select name="paymentMethodId">${visibleMethods().map(p=>`<option value="${p.id}" ${Number(s.PaymentMethodID)===Number(p.id)?'selected':''}>${esc(p.name)}</option>`).join('')}</select></label>
      <label class="check-row wide trial-toggle"><span>무료 체험 사용${service?.SupportsTrial?' · 이 서비스에서 지원해요':''}</span><span class="switch"><input name="isTrial" type="checkbox" ${s.TrialEndsAt?'checked':''}><span></span></span></label>
      <label class="field wide" id="trialEndField" ${s.TrialEndsAt?'':'hidden'}><span>무료 체험 종료일 *</span><input name="trialEndsAt" type="date" value="${esc(s.TrialEndsAt||'')}"><p class="help">첫 결제일 전까지 구독비에 포함하지 않아요.</p></label>
      <label class="field"><span>카테고리</span><input name="category" maxlength="40" value="${esc(s.Category)}" placeholder="음악, AI, 영상"></label>
      <label class="field wide"><span>메모</span><textarea name="memo" maxlength="500" placeholder="함께 사용하는 사람이나 플랜을 적어두세요.">${esc(s.Memo)}</textarea></label>
    </div><div class="form-error" id="formError"></div><div class="form-actions">${!edit?'<button class="button ghost left" type="button" id="backToPicker">이전</button>':''}<button class="button ghost" type="button" data-close-modal>취소</button><button class="button primary" type="submit">${edit?'변경 저장':'구독 추가'}</button></div></form>
    ${edit?`<div class="edit-actions"><button class="button ghost" type="button" id="skipSub">${s.Skipped?'이번 결제 다시 포함':'이번 결제 건너뛰기'}</button><button class="button danger" type="button" id="cancelSub">구독 해지</button></div>`:''}`;
    document.querySelector('#backToPicker')?.addEventListener('click',openServicePicker);
    const trialToggle=document.querySelector('[name="isTrial"]');const syncTrial=()=>{const enabled=trialToggle.checked;document.querySelector('#trialEndField').hidden=!enabled;document.querySelector('[name="trialEndsAt"]').required=enabled;document.querySelector('#billingDateLabel').textContent=enabled?'첫 결제일 *':'결제일 *'};trialToggle.addEventListener('change',syncTrial);syncTrial();
    const currencySelect=document.querySelector('[name="currency"]'),amountInput=document.querySelector('[name="amount"]');currencySelect.addEventListener('change',()=>{amountInput.step=String(1/(10**currencyDigits(currencySelect.value)))});
    document.querySelector('#subscriptionForm').addEventListener('submit',async e=>{e.preventDefault();const f=new FormData(e.currentTarget),amount=amountMinorUnits(f.get('amount'),f.get('currency'));if(amount===null){document.querySelector('#formError').textContent='통화의 소수 자릿수에 맞게 금액을 입력해 주세요.';return}const body={serviceId:edit?s.ServiceID:(service?Number(service.ID):null),serviceName:f.get('serviceName'),icon:s.Icon||String(f.get('serviceName')).slice(0,1).toUpperCase(),color:s.Color||'#9AB8A8',amount,currency:f.get('currency'),billingCycle:f.get('billingCycle'),billingDate:f.get('billingDate'),trialEndsAt:f.has('isTrial')?f.get('trialEndsAt'):'',paymentMethodId:Number(f.get('paymentMethodId')),category:f.get('category'),memo:f.get('memo')};try{await api(edit?`/api/subscriptions/${s.id}`:'/api/subscriptions',{method:edit?'PUT':'POST',body});closeModal();await refresh();toast(edit?'구독 정보를 바꿨어요.':'새 구독을 추가했어요.')}catch(err){document.querySelector('#formError').textContent=err.message}});
    document.querySelector('#skipSub')?.addEventListener('click',async()=>{try{await api(`/api/subscriptions/${s.id}/skip`,{method:'POST',body:{skipped:!s.Skipped}});closeModal();await refresh();toast(s.Skipped?'이번 결제를 다시 포함했어요.':'이번 결제만 건너뛰었어요.')}catch(err){toast(err.message,true)}});
    document.querySelector('#cancelSub')?.addEventListener('click',async()=>{if(!confirm(`${s.ServiceName} 구독을 해지할까요? 과거 기록은 그대로 남아요.`))return;try{await api(`/api/subscriptions/${s.id}/cancel`,{method:'POST',body:{}});closeModal();await refresh();toast('구독을 해지했어요.')}catch(err){toast(err.message,true)}});
  }

  function openSettings(){
    openModal('설정','내 환경');modal.classList.add('wide');
    modalBody.innerHTML=`<div class="settings-tabs"><button class="active" type="button" data-tab="profile">기본 설정</button><button type="button" data-tab="payments">결제수단</button><button type="button" data-tab="currencies">통화</button><button type="button" data-tab="notifications">알림</button><button type="button" data-tab="channels">연동</button><button type="button" data-tab="data">데이터 관리</button></div>
    <form id="settingsForm"><section class="settings-section active" data-section="profile"><div class="field-grid"><label class="field wide"><span>사용자 이름</span><input name="name" required value="${esc(state.user.Name)}"></label><label class="field"><span>기본 통화</span><select name="currency">${visibleCurrencies().map(c=>`<option value="${esc(c.code)}" ${state.user.Currency===c.code?'selected':''}>${esc(c.code)}${c.name&&c.name!==c.code?' · '+esc(c.name):''}</option>`).join('')}</select></label><label class="field"><span>Timezone</span><input name="timezone" value="${esc(state.user.Timezone)}"></label></div><div class="edit-actions"><button class="button ghost" type="button" id="logoutButton">로그아웃</button></div></section>
    <section class="settings-section" data-section="payments"><h3>기본 제공</h3><div>${state.paymentMethods.filter(p=>p.IsBuiltin).map(p=>pmRow(p)).join('')}</div><h3>사용자 지정</h3><div id="customMethods">${state.paymentMethods.filter(p=>!p.IsBuiltin&&!p.Archived).map(p=>pmRow(p)).join('')||'<p class="help">추가한 결제수단이 아직 없어요.</p>'}</div><div class="add-row"><input id="newMethod" placeholder="예: 토스페이" maxlength="40"><button class="button" type="button" id="addMethod">추가</button></div><p class="help">사용 중인 결제수단을 삭제하면 기존 구독 기록을 위해 보관 처리돼요.</p></section>
    <section class="settings-section" data-section="currencies"><h3>기본 제공</h3><div>${(state.currencies||[]).filter(c=>c.isBuiltin).map(c=>currencyRow(c)).join('')}</div><h3>사용자 지정</h3><div>${(state.currencies||[]).filter(c=>!c.isBuiltin&&!c.archived).map(c=>currencyRow(c)).join('')||'<p class="help">추가한 통화가 아직 없어요.</p>'}</div><div class="add-row"><input id="newCurrency" placeholder="예: GBP" maxlength="3" autocapitalize="characters"><button class="button" type="button" id="addCurrency">추가</button></div><p class="help">ISO 형식의 영문 통화 코드 3자리를 입력해 주세요.</p></section>
    <section class="settings-section" data-section="notifications"><label class="check-row"><span>결제 예정 알림</span><span class="switch"><input name="notifyUpcoming" type="checkbox" ${state.settings.NotifyUpcoming?'checked':''}><span></span></span></label><label class="check-row"><span>구독 추가·변경·해지 알림</span><span class="switch"><input name="notifyChanges" type="checkbox" ${state.settings.NotifyChanges?'checked':''}><span></span></span></label><label class="check-row"><span>월간 요약</span><span class="switch"><input name="notifyMonthly" type="checkbox" ${state.settings.NotifyMonthly?'checked':''}><span></span></span></label><label class="field"><span>결제 며칠 전에 알릴까요?</span><input name="notifyDays" type="number" min="0" max="30" value="${state.settings.NotifyDays}"></label></section>
    <section class="settings-section" data-section="channels"><label class="field"><span>Discord Webhook</span><input name="discordWebhook" type="url" value="${esc(state.settings.DiscordWebhook)}" placeholder="https://discord.com/api/webhooks/..."></label><div class="form-actions"><button class="button ghost" type="button" data-test="discord">Discord 테스트</button></div><div class="field-grid"><label class="field wide"><span>Telegram Bot Token</span><input name="telegramBotToken" type="password" value="${esc(state.settings.TelegramBotToken)}" autocomplete="off"></label><label class="field wide"><span>Telegram Chat ID</span><input name="telegramChatId" value="${esc(state.settings.TelegramChatID)}"></label></div><div class="form-actions"><button class="button ghost" type="button" data-test="telegram">Telegram 테스트</button></div></section>
    <section class="settings-section" data-section="data"><h3>JSON 백업</h3><p class="help">구독, 결제수단, 가격 이력과 설정을 JSON 파일로 관리해요. 로그인 비밀번호와 세션은 포함하지 않지만 알림 연동 정보는 포함돼요.</p><div class="data-actions"><button class="button" type="button" id="exportData">JSON 내보내기</button><label class="button import-label">JSON 가져오기<input id="importData" type="file" accept="application/json,.json" aria-label="JSON 백업 가져오기" hidden></label></div><p class="help danger-text">가져오기는 현재 구독 데이터를 백업 파일의 내용으로 교체해요.</p></section>
    <div class="form-error" id="settingsError"></div><div class="form-actions"><button class="button ghost" type="button" data-close-modal>닫기</button><button class="button primary" type="submit">설정 저장</button></div></form>`;
    bindSettings();
  }
  function pmRow(p){return `<div class="pm-row" data-pm-row="${p.id}"><span class="pm-check">${uiIcons.check}</span><span>${esc(p.name)}</span>${p.IsBuiltin?'<small class="muted">기본</small>':`<span class="inline-actions"><button class="mini-button" type="button" data-rename-pm="${p.id}">이름 변경</button><button class="mini-button danger" type="button" data-delete-pm="${p.id}">삭제</button></span>`}</div>`}
  function currencyRow(c){return `<div class="pm-row"><span class="pm-check">${c.isBuiltin?uiIcons.check:uiIcons.dot}</span><span><strong>${esc(c.code)}</strong>${c.name&&c.name!==c.code?` <small class="muted">${esc(c.name)}</small>`:''}</span>${c.isBuiltin?'<small class="muted">기본</small>':`<button class="mini-button danger" type="button" data-delete-currency="${c.id}">삭제</button>`}</div>`}
  function bindSettings(){
    document.querySelectorAll('[data-tab]').forEach(b=>b.addEventListener('click',()=>{document.querySelectorAll('[data-tab]').forEach(x=>x.classList.toggle('active',x===b));document.querySelectorAll('[data-section]').forEach(x=>x.classList.toggle('active',x.dataset.section===b.dataset.tab))}));
    document.querySelector('#settingsForm').addEventListener('submit',async e=>{e.preventDefault();const f=new FormData(e.currentTarget);const body={name:f.get('name'),currency:f.get('currency'),timezone:f.get('timezone'),discordWebhook:f.get('discordWebhook'),telegramBotToken:f.get('telegramBotToken'),telegramChatId:f.get('telegramChatId'),notifyDays:Number(f.get('notifyDays')),notifyUpcoming:f.has('notifyUpcoming'),notifyChanges:f.has('notifyChanges'),notifyMonthly:f.has('notifyMonthly')};try{await api('/api/settings',{method:'PUT',body});closeModal();await refresh();toast('설정을 저장했어요.')}catch(err){document.querySelector('#settingsError').textContent=err.message}});
    document.querySelector('#addMethod').addEventListener('click',async()=>{const input=document.querySelector('#newMethod');try{await api('/api/payment-methods',{method:'POST',body:{name:input.value}});await reloadAndSettings('payments');toast('결제수단을 추가했어요.')}catch(err){toast(err.message,true)}});
    document.querySelector('#addCurrency').addEventListener('click',async()=>{const input=document.querySelector('#newCurrency');try{await api('/api/currencies',{method:'POST',body:{code:input.value}});await reloadAndSettings('currencies');toast('통화를 추가했어요.')}catch(err){toast(err.message,true)}});
    document.querySelectorAll('[data-delete-currency]').forEach(b=>b.addEventListener('click',async()=>{const c=(state.currencies||[]).find(x=>x.id===Number(b.dataset.deleteCurrency));if(!confirm(`${c.code} 통화를 삭제할까요?`))return;try{await api(`/api/currencies/${c.id}`,{method:'DELETE'});await reloadAndSettings('currencies');toast('통화를 정리했어요.')}catch(err){toast(err.message,true)}}));
    document.querySelectorAll('[data-rename-pm]').forEach(b=>b.addEventListener('click',async()=>{const p=state.paymentMethods.find(x=>x.id===Number(b.dataset.renamePm));const name=prompt('새 결제수단 이름을 입력해 주세요.',p.name);if(!name||name===p.name)return;try{await api(`/api/payment-methods/${p.id}`,{method:'PUT',body:{name}});await reloadAndSettings('payments');toast('이름을 바꿨어요.')}catch(err){toast(err.message,true)}}));
    document.querySelectorAll('[data-delete-pm]').forEach(b=>b.addEventListener('click',async()=>{const p=state.paymentMethods.find(x=>x.id===Number(b.dataset.deletePm));if(!confirm(`${p.name}을(를) 삭제할까요?`))return;try{await api(`/api/payment-methods/${p.id}`,{method:'DELETE'});await reloadAndSettings('payments');toast('결제수단을 정리했어요.')}catch(err){toast(err.message,true)}}));
    document.querySelectorAll('[data-test]').forEach(b=>b.addEventListener('click',async()=>{const f=new FormData(document.querySelector('#settingsForm'));const body={channel:b.dataset.test,discordWebhook:f.get('discordWebhook'),telegramBotToken:f.get('telegramBotToken'),telegramChatId:f.get('telegramChatId')};try{await api('/api/notifications/test',{method:'POST',body});toast('SubManager 알림 테스트를 보냈어요.')}catch(err){toast(err.message,true)}}));
    document.querySelector('#logoutButton').addEventListener('click',async()=>{try{await api('/auth/logout',{method:'POST',body:{}});location.replace('/')}catch(err){toast(err.message,true)}});
    document.querySelector('#exportData').addEventListener('click',async()=>{try{const res=await fetch('/api/data/export');if(!res.ok)throw new Error('백업을 만들지 못했어요.');const blob=await res.blob(),url=URL.createObjectURL(blob),a=document.createElement('a');a.href=url;a.download=`submanager-backup-${localDate()}.json`;a.click();URL.revokeObjectURL(url);toast('JSON 백업을 만들었어요.')}catch(err){toast(err.message,true)}});
    document.querySelector('#importData').addEventListener('change',async e=>{const file=e.target.files[0];if(!file)return;if(!confirm('현재 구독 데이터를 선택한 백업으로 교체할까요?')){e.target.value='';return}try{const res=await fetch('/api/data/import',{method:'POST',headers:{'Content-Type':'application/json','Accept':'application/json'},body:await file.text()});const data=await res.json();if(!res.ok)throw new Error(data.error||'가져오지 못했어요.');closeModal();await refresh();toast('JSON 백업을 가져왔어요.')}catch(err){toast(err.message,true)}finally{e.target.value=''}});
  }
  async function reloadAndSettings(tab){state=await api('/api/state');openSettings();document.querySelector(`[data-tab="${tab}"]`)?.click()}

  async function api(url,{method='GET',body}={}) { const opts={method,headers:{'Accept':'application/json'}};if(body!==undefined){opts.headers['Content-Type']='application/json';opts.body=JSON.stringify(body)}const res=await fetch(url,opts);let data={};try{data=await res.json()}catch{}if(res.status===401){location.replace('/');throw new Error('로그인이 필요해요')}if(!res.ok)throw new Error(data.error||'요청을 처리하지 못했어요.');return data }
  async function refresh(){state=await api('/api/state');render()}
  function toast(message,error=false){const el=document.createElement('div');el.className=`toast${error?' error':''}`;el.textContent=message;document.querySelector('#toasts').append(el);setTimeout(()=>el.remove(),3200)}

  document.addEventListener('click',e=>{
    const currency=e.target.closest('[data-currency]');if(currency){e.preventDefault();e.stopPropagation();selectedCurrency=currency.dataset.currency;render(currentView);return}
    const view=e.target.closest('[data-view]');if(view){render(view.dataset.view);return}
    const edit=e.target.closest('[data-edit-sub]');if(edit){const s=state.subscriptions.find(x=>x.id===Number(edit.dataset.editSub));if(s)openSubForm(null,s);return}
    const pick=e.target.closest('[data-service]');if(pick){const s=pick.dataset.service==='manual'?null:state.services.find(x=>String(x.ID)===pick.dataset.service);openSubForm(s);return}
    if(e.target.closest('[data-close-modal]'))closeModal();
  });
  document.addEventListener('keydown',e=>{if(e.key==='Escape'&&!backdrop.hidden)closeModal();if((e.key==='Enter'||e.key===' ')&&e.target.matches('.chart-card[data-view]')){e.preventDefault();render('stats')}});
  backdrop.addEventListener('mousedown',e=>{if(e.target===backdrop)closeModal()});
  document.querySelector('#addSubscriptionButton').addEventListener('click',openServicePicker);
  themeButton.addEventListener('click',()=>{const current=document.documentElement.dataset.themePreference||'system',next=themeModes[(themeModes.indexOf(current)+1)%themeModes.length];applyTheme(next,true);toast(`${themeLabels[next]} 테마로 전환했어요.`)});
  themeMedia.addEventListener('change',()=>{if(document.documentElement.dataset.themePreference==='system')applyTheme('system')});
  document.querySelector('#settingsButton').addEventListener('click',openSettings);
  applyTheme(document.documentElement.dataset.themePreference||'system');
  render();
})();
