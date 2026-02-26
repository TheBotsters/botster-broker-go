// Botster Broker Dashboard — Plain JS, no build step, pure functions only.
// This is the Smalltalk/Self ideal: source IS what runs.

/** @param {string} url @param {object} [opts] @returns {Promise<any>} */
async function api(url, opts = {}) {
  try {
    const res = await fetch(url, {
      headers: { 'Content-Type': 'application/json', ...opts.headers },
      ...opts,
    });
    return await res.json();
  } catch (err) {
    showError(err.message);
    return null;
  }
}

/** @param {string} msg */
function showError(msg) {
  const el = document.getElementById('error');
  el.textContent = msg;
  el.style.display = 'block';
  setTimeout(() => { el.style.display = 'none'; }, 5000);
}

// --- Render functions (pure — take data, return HTML strings) ---

/** @param {object} agent @returns {string} */
function renderAgent(agent) {
  const safeBadge = agent.safe ? '<span class="badge safe">SAFE</span>' : '';
  return `
    <div class="card">
      <div class="row">
        <div>
          <span class="name">${agent.name}</span> ${safeBadge}
          <div class="meta">ID: ${agent.id}</div>
        </div>
        <button class="${agent.safe ? 'danger' : ''}" onclick="toggleAgentSafe('${agent.id}')">
          ${agent.safe ? 'Disable Safe Mode' : 'Enable Safe Mode'}
        </button>
      </div>
    </div>
  `;
}

/** @param {object} actuator @returns {string} */
function renderActuator(actuator) {
  const statusClass = actuator.status === 'online' ? 'online' : 'offline';
  const typeClass = actuator.type === 'brain' ? 'brain' : 'vps';
  return `
    <div class="card">
      <div class="row">
        <div>
          <span class="name">${actuator.name}</span>
          <span class="badge ${statusClass}">${actuator.status}</span>
          <span class="badge ${typeClass}">${actuator.type}</span>
          <div class="meta">ID: ${actuator.id}${actuator.last_seen_at ? ' · Last seen: ' + new Date(actuator.last_seen_at).toLocaleString() : ''}</div>
        </div>
      </div>
    </div>
  `;
}

// --- Data loading ---

async function loadDashboard() {
  const [health, actuators] = await Promise.all([
    api('/health'),
    api('/v1/actuators'),
  ]);

  if (health) {
    document.getElementById('schema-version').textContent = 'v' + health.schema_version;
  }

  if (actuators) {
    document.getElementById('actuator-count').textContent = actuators.length;
    document.getElementById('actuators-list').innerHTML = actuators.map(renderActuator).join('');
  }

  // These need auth — skip if no token set
  // For now, show counts from actuators response
}

// --- Actions ---

async function toggleGlobalSafe() {
  const result = await api('/v1/dashboard/safe', { method: 'POST' });
  if (result) {
    const banner = document.getElementById('safe-banner');
    banner.classList.toggle('active', result.global_safe);
    loadDashboard();
  }
}

async function toggleAgentSafe(agentId) {
  const result = await api(`/v1/agents/${agentId}/safe`, { method: 'POST' });
  if (result) {
    loadDashboard();
  }
}

// Make functions available globally for onclick handlers
window.toggleGlobalSafe = toggleGlobalSafe;
window.toggleAgentSafe = toggleAgentSafe;

// --- Init ---
loadDashboard();
// Refresh every 10 seconds
setInterval(loadDashboard, 10000);
