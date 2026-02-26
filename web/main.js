// Botster Broker Dashboard — Plain JS, no build step, pure functions.
// Source is what runs. Like Self intended.

/** @typedef {{ id: string, name: string, safe: boolean }} Agent */
/** @typedef {{ id: string, name: string, type: string, status: string, enabled: boolean }} Actuator */

// --- API Layer (with runtime validation) ---

async function fetchJSON(url, options = {}) {
  const res = await fetch(url, {
    headers: { 'Content-Type': 'application/json', ...options.headers },
    ...options,
  });
  return res.json();
}

async function getHealth() {
  return fetchJSON('/health');
}

async function getActuators() {
  const data = await fetchJSON('/v1/actuators');
  if (!Array.isArray(data)) throw new Error('Expected array of actuators');
  return data;
}

async function getAgents() {
  // Agents endpoint requires auth — we'll read from a public-ish endpoint
  // For now, parse from actuators + a direct DB query proxy
  // TODO: add a public agents list endpoint
  return [];
}

async function toggleGlobalSafe() {
  const data = await fetchJSON('/v1/dashboard/safe', { method: 'POST' });
  refresh();
  return data;
}

async function toggleAgentSafe(agentId) {
  const data = await fetchJSON(`/v1/agents/${agentId}/safe`, { method: 'POST' });
  refresh();
  return data;
}

// --- Render Functions (pure — take data, return nothing, mutate DOM) ---

function renderActuators(actuators) {
  const tbody = document.getElementById('actuators-body');
  tbody.innerHTML = actuators.map(a => `
    <tr>
      <td>${escapeHtml(a.name)}</td>
      <td>${escapeHtml(a.type)}</td>
      <td><span class="badge badge-${a.status === 'online' ? 'online' : 'offline'}">${a.status}</span></td>
    </tr>
  `).join('');
}

function renderAgents(agents) {
  const tbody = document.getElementById('agents-body');
  if (agents.length === 0) {
    tbody.innerHTML = '<tr><td colspan="3" style="color:#8b949e">Auth required to list agents</td></tr>';
    return;
  }
  tbody.innerHTML = agents.map(a => `
    <tr>
      <td>${escapeHtml(a.name)}</td>
      <td><span class="badge ${a.safe ? 'badge-safe' : 'badge-live'}">${a.safe ? 'SAFE' : 'LIVE'}</span></td>
      <td><button class="btn ${a.safe ? 'btn-safe' : 'btn-danger'}" onclick="toggleAgentSafe('${a.id}')">${a.safe ? 'Enable' : 'Disable'}</button></td>
    </tr>
  `).join('');
}

function renderSafeBanner(globalSafe) {
  const banner = document.getElementById('safe-banner');
  banner.classList.toggle('active', globalSafe);
}

function renderSecretsCount(count) {
  document.getElementById('secrets-count').textContent = `${count} secrets stored (encrypted at rest)`;
}

function renderStatus(msg) {
  document.getElementById('status').textContent = msg;
}

// --- Helpers ---

function escapeHtml(str) {
  const div = document.createElement('div');
  div.textContent = str;
  return div.innerHTML;
}

// --- Main ---

async function refresh() {
  try {
    const [health, actuators] = await Promise.all([
      getHealth(),
      getActuators(),
    ]);

    renderActuators(actuators);
    renderAgents([]); // TODO: needs auth
    renderStatus(`Connected — schema v${health.schema_version} — ${actuators.length} actuators — ${new Date().toLocaleTimeString()}`);
  } catch (err) {
    renderStatus(`Error: ${err.message}`);
  }
}

// Make toggle functions available globally
window.toggleGlobalSafe = toggleGlobalSafe;
window.toggleAgentSafe = toggleAgentSafe;

// Initial load + auto-refresh
refresh();
setInterval(refresh, 10000);
