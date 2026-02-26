// Botster Broker Dashboard — pure JS, no build step, no framework
// Pure functions. Source is what runs.

/** @param {string} id @returns {HTMLElement} */
const $ = (id) => document.getElementById(id);

/** @param {string} url @param {Object} [opts] @returns {Promise<any>} */
async function api(url, opts = {}) {
  const res = await fetch(url, {
    headers: { 'Content-Type': 'application/json', ...opts.headers },
    ...opts,
  });
  return res.json();
}

/** @param {string} text @returns {string} */
function timeAgo(text) {
  if (!text) return '—';
  const diff = Date.now() - new Date(text).getTime();
  const mins = Math.floor(diff / 60000);
  if (mins < 1) return 'just now';
  if (mins < 60) return mins + 'm ago';
  const hrs = Math.floor(mins / 60);
  if (hrs < 24) return hrs + 'h ago';
  return Math.floor(hrs / 24) + 'd ago';
}

/** @param {string} status @returns {string} */
function statusBadge(status) {
  const cls = status === 'online' ? 'badge-online' : 'badge-offline';
  return `<span class="badge ${cls}">${status}</span>`;
}

/** @param {string} type @returns {string} */
function typeBadge(type) {
  const cls = type === 'brain' ? 'badge-brain' : 'badge-vps';
  return `<span class="badge ${cls}">${type}</span>`;
}

// --- Refresh functions (pure: data in, DOM out) ---

async function refreshStats() {
  const health = await api('/health');
  $('stat-schema').textContent = health.schema_version;

  const actuators = await api('/v1/actuators');
  $('stat-actuators').textContent = actuators.length;
}

async function refreshAgents() {
  // We don't have a public agents list endpoint without auth.
  // For the dashboard, we'll show what we get from actuators context.
  // TODO: add session-based dashboard auth
  $('stat-agents').textContent = '—';
}

async function refreshActuators() {
  const actuators = await api('/v1/actuators');
  const tbody = $('actuators-table');
  tbody.innerHTML = actuators.map((a) => `
    <tr>
      <td><strong>${a.name}</strong></td>
      <td>${typeBadge(a.type)}</td>
      <td>${statusBadge(a.status)}</td>
      <td>${timeAgo(a.last_seen_at)}</td>
    </tr>
  `).join('');
}

async function checkSafeMode() {
  // No direct endpoint yet — try toggling logic from banner state
  // For now, we'll just hide the banner. TODO: GET /v1/dashboard/safe
  $('safe-banner').classList.remove('active');
}

async function toggleGlobalSafe() {
  const result = await api('/v1/dashboard/safe', { method: 'POST' });
  if (result.global_safe) {
    $('safe-banner').classList.add('active');
  } else {
    $('safe-banner').classList.remove('active');
  }
}

// --- Init ---

async function refresh() {
  try {
    await Promise.all([refreshStats(), refreshActuators(), checkSafeMode()]);
  } catch (e) {
    console.error('Refresh failed:', e);
  }
}

refresh();
setInterval(refresh, 10000); // Refresh every 10s
