/* Shared helpers */
const COLORS = {
  'Banco Falabella': '#3B8C3A',
  'Banco Estado': '#E07B1F',
  'Efectivo': '#1E6B66',
  'Ahorros': '#7A5BB5',
};
const colorFor = n => COLORS[n] || '#6B5F54';
const clp = cents => new Intl.NumberFormat('es-CL', {style:'currency', currency:'CLP', maximumFractionDigits:0}).format(Math.round(cents/100));
const esc = s => String(s).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const uid = () => (crypto.randomUUID ? crypto.randomUUID() : Date.now() + '-' + Math.random());

async function api(path, opts = {}) {
  const res = await fetch(path, {
    credentials: 'same-origin',
    headers: {'Content-Type': 'application/json'},
    ...opts,
    body: opts.body ? JSON.stringify(opts.body) : undefined,
  });
  let data = null;
  try { data = await res.json(); } catch (_) {}
  if (res.status === 401 && !location.pathname.startsWith('/login')) { location.href = '/login'; throw new Error('unauthorized'); }
  if (!res.ok) throw new Error((data && data.error) || 'Algo salió mal. Intenta de nuevo.');
  return data;
}

// Money input: shows 1.234.567 while typing; value in pesos (integer).
function moneyInput(el, onChange) {
  el.setAttribute('inputmode', 'numeric');
  el.addEventListener('input', () => {
    const digits = el.value.replace(/\D/g, '').slice(0, 12);
    el.value = digits ? new Intl.NumberFormat('es-CL').format(+digits) : '';
    onChange && onChange(+digits);
  });
}
const pesos = el => +(el.value.replace(/\D/g, '') || 0);

function countUp(el, cents, ms = 700) {
  const start = performance.now();
  if (matchMedia('(prefers-reduced-motion: reduce)').matches) { el.textContent = clp(cents); return; }
  const tick = t => {
    const p = Math.min(1, (t - start) / ms);
    el.textContent = clp(cents * (1 - Math.pow(1 - p, 3)));
    if (p < 1) requestAnimationFrame(tick);
  };
  requestAnimationFrame(tick);
}

let toastT;
function toast(msg) {
  let t = document.querySelector('.toast');
  if (!t) { t = document.createElement('div'); t.className = 'toast'; t.setAttribute('role', 'status'); document.body.appendChild(t); }
  t.textContent = msg; t.classList.add('show');
  clearTimeout(toastT); toastT = setTimeout(() => t.classList.remove('show'), 2600);
}

function openModal(id) { const m = document.getElementById(id); m.classList.add('open'); const f = m.querySelector('input,select,button'); f && f.focus(); }
function closeModal(id) { document.getElementById(id).classList.remove('open'); }
document.addEventListener('keydown', e => { if (e.key === 'Escape') document.querySelectorAll('.overlay.open').forEach(o => o.classList.remove('open')); });

function renderTopbar(user, current) {
  const link = (href, label, key) => `<a href="${href}" ${current === key ? 'aria-current="page"' : ''}>${label}</a>`;
  document.getElementById('topbar').innerHTML = `
    <a class="brand" href="/dashboard"><i></i>Mi Libro</a>
    <nav aria-label="Principal">${link('/dashboard', 'Resumen', 'resumen')}${link('/transferencias', 'Transferir', 'transferir')}</nav>
    <div class="user"><span>${esc(user.full_name)}</span><a href="/logout">Salir</a></div>`;
}

async function loadAccounts() {
  const accs = await api('/api/v1/accounts');
  return accs;
}

// Ledger rows -> friendly movements
function toMovements(txs) {
  return txs.map(t => {
    const d = t.entries.find(e => e.direction === 'DEBIT');
    const c = t.entries.find(e => e.direction === 'CREDIT');
    const external = d && d.account_name === 'Aportes externos';
    return {
      id: t.id, desc: t.description, date: new Date(t.created_at),
      amount: c ? c.amount_cents : 0,
      from: external ? null : d && d.account_name,
      to: c && c.account_name,
      kind: external ? 'in' : 'move',
    };
  });
}
const fmtDate = d => d.toLocaleDateString('es-CL', {day:'numeric', month:'short'}) + ' · ' + d.toLocaleTimeString('es-CL', {hour:'2-digit', minute:'2-digit'});
