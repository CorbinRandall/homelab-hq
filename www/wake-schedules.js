/* Scheduled wake CRUD — loaded after app.js (uses showToast, esc) */

const DAY_NAMES = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
const PRESETS = {
  everyday: [0, 1, 2, 3, 4, 5, 6],
  weekdays: [1, 2, 3, 4, 5],
  weekends: [0, 6],
};

const wakeList = document.getElementById('wakeList');
const wakeEmpty = document.getElementById('wakeEmpty');
const wakeTz = document.getElementById('wakeTz');
const addWakeBtn = document.getElementById('addWakeBtn');
const addSleepBtn = document.getElementById('addSleepBtn');
const addShutdownBtn = document.getElementById('addShutdownBtn');
const wakeDialog = document.getElementById('wakeDialog');
const wakeForm = document.getElementById('wakeForm');
const wakeDialogTitle = document.getElementById('wakeDialogTitle');
const wakeLabel = document.getElementById('wakeLabel');
const wakeTime = document.getElementById('wakeTime');
const wakeAction = document.getElementById('wakeAction');
const wakeDayGrid = document.getElementById('wakeDayGrid');
const wakeEnabled = document.getElementById('wakeEnabled');
const wakeCancelBtn = document.getElementById('wakeCancelBtn');

let wakeSchedules = [];
let wakeTimezone = '';
let editingWakeId = null;

function formatWakeTime(hour, minute) {
  const h = Number(hour);
  const m = Number(minute);
  const d = new Date(2000, 0, 1, h, m);
  return d.toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' });
}

function formatWakeDays(days) {
  const sorted = [...days].sort((a, b) => a - b);
  if (sorted.length === 7) return 'Every day';
  if (sorted.length === 5 && PRESETS.weekdays.every((d, i) => d === sorted[i])) return 'Weekdays';
  if (sorted.length === 2 && PRESETS.weekends.every((d, i) => d === sorted[i])) return 'Weekends';
  return sorted.map((d) => DAY_NAMES[d]).join(', ');
}

function buildDayGrid(selected) {
  wakeDayGrid.innerHTML = DAY_NAMES.map(
    (name, i) => `
      <label class="day-chip">
        <input type="checkbox" name="wakeDay" value="${i}"${selected.includes(i) ? ' checked' : ''}>
        ${name}
      </label>`
  ).join('');
}

function selectedDays() {
  return [...wakeDayGrid.querySelectorAll('input[name="wakeDay"]:checked')].map((el) =>
    Number(el.value)
  );
}

function setSelectedDays(days) {
  wakeDayGrid.querySelectorAll('input[name="wakeDay"]').forEach((el) => {
    el.checked = days.includes(Number(el.value));
  });
}

function renderWakeSchedules() {
  if (wakeSchedules.length === 0) {
    wakeList.innerHTML = '';
    wakeEmpty.hidden = false;
    return;
  }
  wakeEmpty.hidden = true;
  wakeList.innerHTML = wakeSchedules
    .slice()
    .sort((a, b) => a.hour - b.hour || a.minute - b.minute)
    .map(
      (s) => `
      <div class="wake-row${s.enabled ? '' : ' disabled'}" data-id="${esc(s.id)}">
        <label class="wake-toggle" title="${s.enabled ? 'Disable' : 'Enable'}">
          <input type="checkbox" class="wake-enable-toggle" data-id="${esc(s.id)}"${s.enabled ? ' checked' : ''}>
          <span></span>
        </label>
        <span class="wake-time">${esc(formatWakeTime(s.hour, s.minute))}</span>
        <span class="wake-label-tag">${s.action === 'shutdown' ? 'Shutdown' : (s.action === 'sleep' ? 'Sleep' : 'Turn on')}</span>
        <span class="wake-days">${esc(formatWakeDays(s.days))}</span>
        ${s.label ? `<span class="wake-label-tag">${esc(s.label)}</span>` : ''}
        <div class="wake-actions">
          <button type="button" class="btn-small wake-edit-btn" data-id="${esc(s.id)}">Edit</button>
          <button type="button" class="btn-small danger wake-delete-btn" data-id="${esc(s.id)}">Delete</button>
        </div>
      </div>`
    )
    .join('');

  wakeList.querySelectorAll('.wake-enable-toggle').forEach((el) => {
    el.addEventListener('change', () => toggleWakeSchedule(el.dataset.id, el.checked));
  });
  wakeList.querySelectorAll('.wake-edit-btn').forEach((el) => {
    el.addEventListener('click', () => openWakeDialog(el.dataset.id));
  });
  wakeList.querySelectorAll('.wake-delete-btn').forEach((el) => {
    el.addEventListener('click', () => deleteWakeSchedule(el.dataset.id));
  });
}

async function loadWakeSchedules() {
  try {
    const r = await fetch('/api/wake-schedules', { cache: 'no-store' });
    if (!r.ok) throw new Error('load failed');
    const data = await r.json();
    wakeSchedules = data.schedules || [];
    wakeTimezone = data.timezone || '';
    wakeTz.textContent = wakeTimezone
      ? `Times in ${wakeTimezone.replace(/_/g, ' ')} · checked every minute`
      : 'Auto-wake when Unraid is asleep';
    renderWakeSchedules();
  } catch {
    wakeTz.textContent = 'Could not load wake schedules';
  }
}

function openWakeDialog(id, initialAction = 'wake') {
  editingWakeId = id || null;
  if (id) {
    const s = wakeSchedules.find((x) => x.id === id);
    if (!s) return;
    wakeDialogTitle.textContent = 'Edit wake schedule';
    wakeLabel.value = s.label || '';
    wakeAction.value = s.action || 'wake';
    wakeTime.value = `${String(s.hour).padStart(2, '0')}:${String(s.minute).padStart(2, '0')}`;
    buildDayGrid(s.days);
    wakeEnabled.checked = s.enabled !== false;
  } else {
    const actionName = initialAction === 'sleep' ? 'sleep' : (initialAction === 'shutdown' ? 'shutdown' : 'turn on');
    wakeDialogTitle.textContent = `Add ${actionName} schedule`;
    wakeLabel.value = '';
    wakeAction.value = initialAction;
    wakeTime.value = '09:00';
    buildDayGrid(PRESETS.everyday);
    wakeEnabled.checked = true;
  }
  wakeDialog.showModal();
}

function closeWakeDialog() {
  wakeDialog.close();
  editingWakeId = null;
}

async function saveWakeSchedule(ev) {
  ev.preventDefault();
  const days = selectedDays();
  if (days.length === 0) {
    showToast('Pick at least one day');
    return;
  }
  const [hour, minute] = wakeTime.value.split(':').map(Number);
  const payload = {
    label: wakeLabel.value.trim(),
    action: wakeAction.value,
    hour,
    minute,
    days,
    enabled: wakeEnabled.checked,
  };

  try {
    let r;
    if (editingWakeId) {
      r = await fetch(`/api/wake-schedules/${encodeURIComponent(editingWakeId)}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      });
    } else {
      r = await fetch('/api/wake-schedules', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      });
    }
    const data = await r.json();
    if (!r.ok || !data.ok) throw new Error(data.error || 'save failed');
    closeWakeDialog();
    showToast(editingWakeId ? 'Schedule updated' : 'Schedule added');
    await loadWakeSchedules();
  } catch (e) {
    showToast(e.message || 'Save failed');
  }
}

async function toggleWakeSchedule(id, enabled) {
  try {
    const r = await fetch(`/api/wake-schedules/${encodeURIComponent(id)}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ enabled }),
    });
    const data = await r.json();
    if (!r.ok || !data.ok) throw new Error(data.error || 'update failed');
    await loadWakeSchedules();
  } catch {
    showToast('Toggle failed');
    await loadWakeSchedules();
  }
}

async function deleteWakeSchedule(id) {
  const s = wakeSchedules.find((x) => x.id === id);
  const label = s ? formatWakeTime(s.hour, s.minute) : 'this schedule';
  if (!confirm(`Delete wake at ${label}?`)) return;
  try {
    const r = await fetch(`/api/wake-schedules/${encodeURIComponent(id)}`, { method: 'DELETE' });
    const data = await r.json();
    if (!r.ok || !data.ok) throw new Error(data.error || 'delete failed');
    showToast('Schedule deleted');
    await loadWakeSchedules();
  } catch {
    showToast('Delete failed');
  }
}

addWakeBtn.addEventListener('click', () => openWakeDialog(null, 'wake'));
addSleepBtn.addEventListener('click', () => openWakeDialog(null, 'sleep'));
addShutdownBtn.addEventListener('click', () => openWakeDialog(null, 'shutdown'));
wakeCancelBtn.addEventListener('click', closeWakeDialog);
wakeForm.addEventListener('submit', saveWakeSchedule);
wakeDialog.addEventListener('cancel', () => {
  editingWakeId = null;
});

document.querySelectorAll('.preset-btn').forEach((btn) => {
  btn.addEventListener('click', () => {
    const preset = PRESETS[btn.dataset.preset];
    if (preset) setSelectedDays(preset);
  });
});

buildDayGrid(PRESETS.everyday);
loadWakeSchedules();
