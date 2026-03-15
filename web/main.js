// Botster Spine v2 Dashboard — Plain JS, no build step.

// ─── API Helper ────────────────────────────────────────────────────────────────

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

function showError(msg) {
  const el = document.getElementById('error');
  el.textContent = msg;
  el.style.display = 'block';
  setTimeout(() => { el.style.display = 'none'; }, 5000);
}

// ─── Tab Switching ─────────────────────────────────────────────────────────────

function switchTab(tabName, el) {
  document.querySelectorAll('.tab-content').forEach(t => t.classList.remove('active'));
  document.querySelectorAll('nav a[data-tab]').forEach(a => a.classList.remove('active'));
  document.getElementById('tab-' + tabName).classList.add('active');
  if (el) el.classList.add('active');
}

// ─── Card Renderers ────────────────────────────────────────────────────────────

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

function renderActuator(actuator) {
  const statusClass = actuator.status === 'online' ? 'online' : 'offline';
  const typeClass = actuator.type || 'vps';
  const esc = actuator.name.replace(/'/g, '&#39;');
  return `
    <div class="card">
      <div class="row">
        <div style="flex:1;">
          <span class="name">${actuator.name}</span>
          <span class="badge ${statusClass}">${actuator.status}</span>
          <span class="badge ${typeClass}">${actuator.type}</span>
          <div class="meta">ID: ${actuator.id}${actuator.last_seen_at ? ' · Last seen: ' + new Date(actuator.last_seen_at).toLocaleString() : ''}</div>
          <div style="margin-top:8px; display:flex; align-items:center; gap:8px;">
            <span class="meta" style="margin:0;">Capabilities:</span>
            <input id="caps-${actuator.id}" placeholder="exec, notify, wake"
              style="flex:1; max-width:360px; background:#0d1117; color:#c9d1d9; border:1px solid #30363d; padding:6px; border-radius:6px; font-size:0.85em;" />
            <button onclick="saveActuatorCapabilities('${actuator.id}')">Save</button>
          </div>
        </div>
        <button class="primary" onclick="openLogTail('actuator-${actuator.id}', '${esc}', '/dashboard/api/actuators/${actuator.id}/logs?limit=200')">
          Log Tail
        </button>
      </div>
    </div>
  `;
}

// ─── Dashboard Data Loading ────────────────────────────────────────────────────

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
  }
}

// ─── Actions ───────────────────────────────────────────────────────────────────

async function toggleGlobalSafe() {
  const result = await api('/dashboard/api/safe', { method: 'POST' });
  if (result) loadDashboard();
}

async function toggleAgentSafe(agentId) {
  const result = await api('/dashboard/api/agents/' + agentId + '/safe', { method: 'POST' });
  if (result) loadDashboard();
}

async function logout() {
  await api('/auth/logout', { method: 'POST' });
  window.location.href = '/login';
}

// ─── Actuator Capabilities ─────────────────────────────────────────────────────

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
  if (res) await loadActuatorCapabilities(actuatorId);
}

// ─── Reusable Log Tail Panel ───────────────────────────────────────────────────

let _logTail = { id: null, url: null, timer: null, paused: false, autoScroll: true };

function openLogTail(id, title, url) {
  closeLogTail();

  _logTail.id = id;
  _logTail.url = url;
  _logTail.paused = false;
  _logTail.autoScroll = true;

  document.getElementById('log-tail-title').textContent = 'Log Tail — ' + title;
  document.getElementById('log-tail-body').innerHTML = '<div class="log-empty">Loading...</div>';
  document.getElementById('log-tail-footer').textContent = '—';

  const statusEl = document.getElementById('log-tail-status');
  statusEl.textContent = 'live';
  statusEl.classList.remove('paused');

  document.getElementById('log-tail-overlay').classList.add('open');

  const bodyEl = document.getElementById('log-tail-body');
  if (!bodyEl.dataset.scrollBound) {
    bodyEl.addEventListener('scroll', () => {
      // Newest entries are rendered at top; if user scrolls away from top, stop auto-follow.
      _logTail.autoScroll = bodyEl.scrollTop <= 8;
    });
    bodyEl.dataset.scrollBound = '1';
  }

  fetchLogTail();
  _logTail.timer = setInterval(() => {
    if (!_logTail.paused) fetchLogTail();
  }, 2000);
}

function closeLogTail() {
  document.getElementById('log-tail-overlay').classList.remove('open');
  if (_logTail.timer) {
    clearInterval(_logTail.timer);
    _logTail.timer = null;
  }
  _logTail.id = null;
  _logTail.url = null;
}

function toggleLogTailPause() {
  _logTail.paused = !_logTail.paused;
  const statusEl = document.getElementById('log-tail-status');
  if (_logTail.paused) {
    statusEl.textContent = 'paused';
    statusEl.classList.add('paused');
  } else {
    statusEl.textContent = 'live';
    statusEl.classList.remove('paused');
    fetchLogTail();
  }
}

async function fetchLogTail() {
  if (!_logTail.url) return;

  const res = await api(_logTail.url);
  if (!res) return;

  const entries = res.entries || [];
  const body = document.getElementById('log-tail-body');
  const footer = document.getElementById('log-tail-footer');

  // Preserve viewport when user is inspecting older entries.
  const prevTop = body.scrollTop;
  const prevHeight = body.scrollHeight;

  if (entries.length === 0) {
    body.innerHTML = '<div class="log-empty">No log entries yet. Waiting for activity...</div>';
    footer.textContent = 'No entries';
    return;
  }

  const html = entries.map(e => {
    const action = e.action || '';
    const detail = e.detail || '';
    const ts = e.created_at || '';
    const completed = e.completed_at || '';

    let lineClass = 'log-line';
    if (action.includes('error') || action.includes('FAILED')) lineClass += ' error';
    else if (action.includes('pending')) lineClass += ' pending';

    const timeStr = ts ? formatTime(ts) : '';
    const completedStr = completed ? ' → ' + formatTime(completed) : '';

    return `<div class="${lineClass}"><span class="ts">${timeStr}${completedStr}</span> <span class="action">${escHtml(action)}</span> <span class="detail">${escHtml(detail)}</span></div>`;
  }).join('');

  body.innerHTML = html;
  footer.textContent = entries.length + ' entries · updated ' + new Date().toLocaleTimeString();

  if (_logTail.autoScroll) {
    // Newest-first list: pin to top while live-following.
    body.scrollTop = 0;
  } else {
    // Keep current viewport stable as newer rows are inserted at top.
    const newHeight = body.scrollHeight;
    body.scrollTop = prevTop + (newHeight - prevHeight);
  }
}

function formatTime(iso) {
  try {
    const d = new Date(iso);
    return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' }) + ' ' + d.toLocaleDateString([], { month: 'short', day: 'numeric' });
  } catch (_) {
    return iso;
  }
}

function escHtml(s) {
  const d = document.createElement('div');
  d.textContent = s;
  return d.innerHTML;
}

// ─── Inference Live Stream ─────────────────────────────────────────────────────

let _inferenceES = null;

function toggleInferenceStream() {
  const btn = document.getElementById('toggle-inference-stream');
  const out = document.getElementById('inference-stream-body');
  if (!btn || !out) return;

  if (_inferenceES) {
    _inferenceES.close();
    _inferenceES = null;
    btn.textContent = 'Start Stream';
    const marker = document.createElement('div');
    marker.className = 'log-line';
    marker.innerHTML = '<span class="ts">' + new Date().toLocaleTimeString() + '</span> <span class="action">[stream stopped]</span>';
    out.prepend(marker);
    return;
  }

  const marker = document.createElement('div');
  marker.className = 'log-line';
  marker.innerHTML = '<span class="ts">' + new Date().toLocaleTimeString() + '</span> <span class="action">[connecting...]</span>';
  out.prepend(marker);

  _inferenceES = new EventSource('/dashboard/api/inference/stream');
  _inferenceES.onmessage = (evt) => {
    try {
      const ev = JSON.parse(evt.data);
      const when = ev.timestamp || ev.created_at || '';
      const who = ev.agent_name || ev.agent_id || '';
      const status = ev.status || ev.phase || '';
      const model = ev.model || '';

      const line = document.createElement('div');
      line.className = 'log-line';
      const dataHtml = ev.data ? `<pre class="response-body">${escHtml(ev.data)}</pre>` : '';
      line.innerHTML = `<span class="ts">${formatTime(when)}</span> <span class="action">${escHtml(who)}</span> <span class="detail">${escHtml(model)} ${escHtml(status)}</span>${dataHtml}`;
      out.prepend(line);

      // Cap at 300 lines
      while (out.children.length > 300) out.removeChild(out.lastChild);
    } catch (_) {}
  };

  _inferenceES.onerror = () => {
    btn.textContent = 'Start Stream';
    const errLine = document.createElement('div');
    errLine.className = 'log-line error';
    errLine.innerHTML = '<span class="ts">' + new Date().toLocaleTimeString() + '</span> <span class="action">[stream disconnected]</span>';
    out.prepend(errLine);
    if (_inferenceES) _inferenceES.close();
    _inferenceES = null;
  };

  btn.textContent = 'Stop Stream';
}

// ─── Init ──────────────────────────────────────────────────────────────────────

loadDashboard();
setInterval(loadDashboard, 10000);
