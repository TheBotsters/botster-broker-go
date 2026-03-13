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

function setPre(id, text) {
  const el = document.getElementById(id);
  if (el) el.textContent = text;
}

// --- Tab switching ---

function switchTab(tabName, el) {
  document.querySelectorAll('.tab-content').forEach(t => t.classList.remove('active'));
  document.querySelectorAll('nav a[data-tab]').forEach(a => a.classList.remove('active'));
  document.getElementById('tab-' + tabName).classList.add('active');
  if (el) el.classList.add('active');
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

let _activeActuatorLog = null;
let _actuatorLogTimer = null;

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
          <div class="meta" style="margin-top:8px;">Capabilities (comma-separated):</div>
          <input id="caps-${actuator.id}" placeholder="exec, notify, wake" style="width:360px; margin-top:4px; background:#0d1117; color:#c9d1d9; border:1px solid #30363d; padding:6px; border-radius:6px;" />
          <button style="margin-left:8px;" onclick="saveActuatorCapabilities('${actuator.id}')">Save</button>
          <button style="margin-left:8px;" onclick="openActuatorLogModal('${actuator.id}', '${actuator.name.replace(/'/g, "&#39;")}')">View Log Tail</button>
        </div>
      </div>
    </div>
  `;
}

// --- Data loading ---

let _inferenceES = null;

async function loadDashboard() {
  const auth = await api('/auth/status');
  if (!auth || !auth.authenticated) {
    window.location.href = '/login';
    return;
  }
  document.getElementById('user-email').textContent = auth.email;

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

    // Update safe mode button style
    const safeBtn = document.getElementById('toggle-global-safe');
    if (dashboard.global_safe) {
      safeBtn.classList.add('danger');
      safeBtn.textContent = 'Disable Safe Mode';
    } else {
      safeBtn.classList.remove('danger');
      safeBtn.textContent = 'Safe Mode';
    }

    if (dashboard.agents) {
      document.getElementById('agents-list').innerHTML = dashboard.agents.map(renderAgent).join('');
    }
    if (dashboard.actuators) {
      document.getElementById('actuators-list').innerHTML = dashboard.actuators.map(renderActuator).join('');

      for (const a of dashboard.actuators) {
        await loadActuatorCapabilities(a.id);
      }
    }

    await refreshInferenceTail();
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

async function loadActuatorCapabilities(actuatorId) {
  const res = await api('/dashboard/api/actuators/' + actuatorId + '/capabilities');
  if (!res) return;
  const el = document.getElementById('caps-' + actuatorId);
  if (el) el.value = (res.capabilities || []).join(', ');
}

async function saveActuatorCapabilities(actuatorId) {
  const el = document.getElementById('caps-' + actuatorId);
  if (!el) return;
  const capabilities = el.value.split(',').map(s => s.trim()).filter(Boolean);
  const res = await api('/dashboard/api/actuators/' + actuatorId + '/capabilities', {
    method: 'POST',
    body: JSON.stringify({ capabilities }),
  });
  if (res) {
    await loadActuatorCapabilities(actuatorId);
  }
}

async function loadActuatorLogs(actuatorId) {
  if (!actuatorId) {
    setPre('actuator-log-tail', 'No actuator selected.');
    return;
  }
  setPre('actuator-log-tail', 'Loading actuator logs...');
  const res = await api('/dashboard/api/actuators/' + actuatorId + '/logs?limit=100');
  if (!res) {
    setPre('actuator-log-tail', 'Failed to load actuator logs.');
    return;
  }
  const lines = (res.entries || []).map(e => `[${e.created_at}] ${e.action} ${e.detail || ''}`);
  setPre('actuator-log-tail', lines.length ? lines.join('\n') : 'No actuator log entries yet.');
}

function openActuatorLogModal(actuatorId, actuatorName) {
  _activeActuatorLog = actuatorId;
  const modal = document.getElementById('actuator-log-modal');
  const title = document.getElementById('actuator-log-title');
  if (title) title.textContent = `Actuator Log Tail — ${actuatorName}`;
  if (modal) modal.style.display = 'block';

  loadActuatorLogs(actuatorId);
  if (_actuatorLogTimer) clearInterval(_actuatorLogTimer);
  _actuatorLogTimer = setInterval(() => {
    if (_activeActuatorLog) loadActuatorLogs(_activeActuatorLog);
  }, 3000);
}

function closeActuatorLogModal() {
  const modal = document.getElementById('actuator-log-modal');
  if (modal) modal.style.display = 'none';
  _activeActuatorLog = null;
  if (_actuatorLogTimer) {
    clearInterval(_actuatorLogTimer);
    _actuatorLogTimer = null;
  }
}

async function refreshInferenceTail() {
  setPre('inference-tail', 'Loading inference logs...');
  const res = await api('/dashboard/api/inference/tail?limit=100');
  if (!res) {
    setPre('inference-tail', 'Failed to load inference logs.');
    return;
  }
  const lines = (res.entries || []).map(e => {
    const who = e.agent_id ? `agent=${e.agent_id} ` : '';
    return `[${e.created_at}] ${who}${e.action} ${e.detail || ''}`;
  });
  setPre('inference-tail', lines.length ? lines.join('\n') : 'No inference log entries yet.');
}

function toggleInferenceStream() {
  const btn = document.getElementById('toggle-inference-stream');
  const out = document.getElementById('inference-stream');
  if (!btn || !out) return;

  if (_inferenceES) {
    _inferenceES.close();
    _inferenceES = null;
    btn.textContent = 'Start Stream';
    setPre('inference-stream', '[stream stopped]\n' + out.textContent);
    return;
  }

  setPre('inference-stream', '[connecting stream...]\n' + out.textContent);
  _inferenceES = new EventSource('/dashboard/api/inference/stream');
  _inferenceES.onmessage = (evt) => {
    try {
      const ev = JSON.parse(evt.data);
      const when = ev.timestamp || ev.created_at || '';
      const who = ev.agent_name || ev.agent_id || '';
      const status = ev.status || ev.phase || '';
      const model = ev.model || '';
      const line = `[${when}] ${who} ${model} ${status}`.trim();
      out.textContent = (line + '\n' + out.textContent).split('\n').slice(0, 200).join('\n');
    } catch (_) {}
  };
  _inferenceES.onerror = () => {
    btn.textContent = 'Start Stream';
    setPre('inference-stream', '[stream disconnected]\n' + out.textContent);
    if (_inferenceES) _inferenceES.close();
    _inferenceES = null;
  };
  btn.textContent = 'Stop Stream';
}

// --- Init ---
loadDashboard();
setInterval(loadDashboard, 10000);
