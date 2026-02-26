// Botster Broker Dashboard — pure functions, no framework, no build step.
// The source is what runs. Like Self intended.

/** @typedef {{ id: string, name: string, safe: boolean }} Agent */
/** @typedef {{ id: string, name: string, type: string, status: string, enabled: boolean, last_seen_at: string|null }} Actuator */

// --- State (minimal, no shared mutation) ---
let globalSafe = false;

// --- API helpers ---

async function fetchJSON(url, opts = {}) {
  const res = await fetch(url, {
    headers: { 'Content-Type': 'application/json', ...opts.headers },
    ...opts,
  });
  return res.json();
}

// --- Pure render functions ---

function renderAgent(agent) {
  const safeBadge = agent.safe
    ? '<span class="badge badge-safe">SAFE</span>'
    : '<span class="badge badge-live">LIVE</span>';
  const btnClass = agent.safe ? 'btn btn-safe' : 'btn btn-danger';
  const btnLabel = agent.safe ? 'Unlock' : 'Lock';

  return `
    <div class="list-item">
      <div>
        <span class="item-name">${agent.name}</span>
        ${safeBadge}
      </div>
      <button class="${btnClass}" onclick="toggleAgentSafe('${agent.id}')">${btnLabel}</button>
    </div>
  `;
}

function renderActuator(act) {
  const statusBadge = act.status === 'online'
    ? '<span class="badge badge-online">online</span>'
    : '<span class="badge badge-offline">offline</span>';
  const typeBadge = act.type === 'brain'
    ? '<span class="badge badge-brain">brain</span>'
    : '<span class="badge badge-vps">vps</span>';

  const lastSeen = act.last_seen_at
    ? new Date(act.last_seen_at).toLocaleString()
    : 'never';

  return `
    <div class="list-item">
      <div>
        <span class="item-name">${act.name}</span>
        ${typeBadge} ${statusBadge}
        <div class="item-meta">Last seen: ${lastSeen}</div>
      </div>
    </div>
  `;
}

// --- Load data ---

async function loadDashboard() {
  try {
    const [health, actuators] = await Promise.all([
      fetchJSON('/health'),
      fetchJSON('/v1/actuators'),
    ]);

    // Update stats
    document.getElementById('actuator-count').textContent = actuators.length;

    // Render actuators
    document.getElementById('actuators-list').innerHTML =
      actuators.map(renderActuator).join('') || '<div class="item-meta">No actuators</div>';

    // Schema version in status
    document.getElementById('status').textContent =
      `Schema v${health.schema_version} • ${actuators.length} actuators • ${new Date().toLocaleTimeString()}`;

  } catch (err) {
    document.getElementById('status').textContent = `Error: ${err.message}`;
  }
}

// --- Actions ---

window.toggleGlobalSafe = async function() {
  const result = await fetchJSON('/v1/dashboard/safe', { method: 'POST' });
  globalSafe = result.global_safe;
  document.getElementById('safe-banner').classList.toggle('active', globalSafe);
  const btn = document.getElementById('safe-toggle');
  if (globalSafe) {
    btn.className = 'btn btn-safe';
    btn.textContent = 'Disable Safe Mode';
  } else {
    btn.className = 'btn btn-danger';
    btn.textContent = 'Enable Safe Mode';
  }
};

window.toggleAgentSafe = async function(agentId) {
  await fetchJSON(`/v1/agents/${agentId}/safe`, { method: 'POST' });
  loadDashboard();
};

// --- Init ---
loadDashboard();

// Refresh every 10 seconds
setInterval(loadDashboard, 10000);
