// Botster Spine v2 Dashboard — Plain JS, no build step.

/** @param {string} url @param {object} [opts] @returns {Promise<any>} */
async function api(url, opts = {}) {
  try {
    const res = await fetch(url, {
      headers: { 'Content-Type': 'application/json', ...opts.headers },
      credentials: 'same-origin',
      ...opts,
    });
    if (res.status === 401 || res.status === 303) {
      window.location.href = '/login';
      return null;
    }
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

// --- Render functions ---

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
  const typeClass = actuator.type || 'vps';
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
  // Check auth first
  const auth = await api('/auth/status');
  if (!auth || !auth.authenticated) {
    window.location.href = '/login';
    return;
  }
  document.getElementById('user-email').textContent = auth.email;

  // Load dashboard data (session-authenticated endpoints)
  const [health, dashboard] = await Promise.all([
    api('/health'),
    api('/dashboard/api/data'),
  ]);

  if (health) {
    document.getElementById('schema-version').textContent = 'v' + health.schema_version;
  }

  if (dashboard) {
    document.getElementById('agent-count').textContent = dashboard.agents ? dashboard.agents.length : 0;
    document.getElementById('actuator-count').textContent = dashboard.actuators ? dashboard.actuators.length : 0;
    document.getElementById('secret-count').textContent = dashboard.secret_count || 0;

    const banner = document.getElementById('safe-banner');
    banner.classList.toggle('active', !!dashboard.global_safe);

    if (dashboard.agents) {
      document.getElementById('agents-list').innerHTML = dashboard.agents.map(renderAgent).join('');
    }
    if (dashboard.actuators) {
      document.getElementById('actuators-list').innerHTML = dashboard.actuators.map(renderActuator).join('');
    }
  }
}

// --- Actions ---

async function toggleGlobalSafe() {
  const result = await api('/dashboard/api/safe', { method: 'POST' });
  if (result) {
    loadDashboard();
  }
}

async function toggleAgentSafe(agentId) {
  const result = await api('/dashboard/api/agents/' + agentId + '/safe', { method: 'POST' });
  if (result) {
    loadDashboard();
  }
}

async function logout() {
  await api('/auth/logout', { method: 'POST' });
  window.location.href = '/login';
}

// --- Init ---
loadDashboard();
setInterval(loadDashboard, 10000);
