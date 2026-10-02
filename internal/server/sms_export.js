(() => {
  'use strict';
  // This fixed script is CSP-hashed. Message content is never executable markup.
  const reader = document.getElementById('sms-reader');
  const list = document.getElementById('contact-list');
  const deviceList = document.getElementById('device-list');
  const deviceSelect = document.getElementById('device-select');
  const search = document.getElementById('contact-search');
  const noContacts = document.getElementById('no-contacts');
  const noSelection = document.getElementById('no-selection');
  const devices = new Map();
  let selectedDevice = null;
  let active = null;

  function element(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  const threads = Array.from(document.querySelectorAll('.sms-conversation'), (section, index) => {
    const messages = Array.from(section.querySelectorAll('.sms-message'));
    for (const message of messages) {
      const id = message.dataset.device;
      let device = devices.get(id);
      if (!device) {
        device = { id, count: 0, phones: new Set() };
        devices.set(id, device);
      }
      device.count += 1;
      if (message.dataset.phone) device.phones.add(message.dataset.phone);
    }
    const button = element('button', 'sms-thread-item');
    button.type = 'button';
    button.setAttribute('aria-controls', 'conversation-' + index);
    section.id = 'conversation-' + index;
    const row = element('span', 'thread-row-content');
    const preview = element('span', 'thread-preview');
    const peerLine = element('span', 'peer-line');
    const peer = element('span', 'peer-name', section.dataset.peer || '未知联系人');
    const unread = element('span', 'unread-dot');
    unread.title = '导出时含未读短信';
    unread.setAttribute('aria-label', '导出时含未读短信');
    const snippet = element('span', 'last-message');
    const meta = element('span', 'thread-meta');
    const time = element('span', 'thread-time');
    const phone = element('span', 'thread-phone');
    peerLine.append(peer, unread);
    preview.append(peerLine, snippet);
    meta.append(time, phone);
    row.append(preview, meta);
    button.append(row);
    const thread = { section, button, messages, visible: messages, unread, snippet, time, phone, initialized: false, epoch: 0, lastID: 0 };
    button.addEventListener('click', () => { selectThread(thread); reader.classList.add('show-thread'); });
    section.querySelector('.back-button').addEventListener('click', () => {
      reader.classList.remove('show-thread');
      button.focus({ preventScroll: true });
    });
    section.querySelector('.latest-button').addEventListener('click', () => scrollLatest(thread));
    return thread;
  });

  function scrollLatest(thread) {
    const scroll = thread.section.querySelector('.sms-detail-scroll');
    scroll.scrollTop = scroll.scrollHeight;
  }

  function renderDates(thread) {
    thread.section.querySelectorAll('.date-separator').forEach(node => node.remove());
    let previous = '';
    for (const message of thread.visible) {
      // These dates are already formatted in the export timezone. Never reinterpret them in the viewer timezone.
      const day = message.dataset.time.slice(0, 10);
      if (day !== previous) {
        const separator = element('div', 'date-separator');
        separator.append(element('span', '', day.replaceAll('-', '/')));
        message.before(separator);
        previous = day;
      }
    }
  }

  function selectThread(thread) {
    if (active && active !== thread) {
      active.section.hidden = true;
      active.button.setAttribute('aria-current', 'false');
    }
    const changed = active !== thread;
    active = thread;
    noSelection.hidden = Boolean(thread);
    if (!thread) return;
    thread.section.hidden = false;
    thread.button.setAttribute('aria-current', 'true');
    renderDates(thread);
    if (changed || !thread.initialized) {
      requestAnimationFrame(() => {
        if (active === thread && !thread.initialized && thread.section.querySelector('.sms-detail-scroll').clientHeight > 0) {
          scrollLatest(thread);
          thread.initialized = true;
        }
      });
    }
  }

  const deviceOptions = [{ id: null, count: threads.reduce((n, thread) => n + thread.messages.length, 0), phones: new Set() }, ...devices.values()];
  for (const [index, device] of deviceOptions.entries()) {
    const label = device.id === null ? '全部设备' : (device.id || '未知设备');
    const button = element('button', 'device-option');
    button.type = 'button';
    const info = element('span');
    info.append(element('span', 'device-name', label));
    info.append(element('span', 'device-description', device.id === null ? '已导出的所有短信' : (Array.from(device.phones).join(' · ') || '导出记录')));
    button.append(info, element('span', 'device-count', String(device.count)));
    button.title = label;
    button.addEventListener('click', () => setDevice(index));
    device.button = button;
    deviceList.append(button);
    const option = element('option', '', label);
    option.value = String(index);
    deviceSelect.append(option);
  }

  function setDevice(index) {
    selectedDevice = deviceOptions[index].id;
    deviceSelect.value = String(index);
    for (const [i, device] of deviceOptions.entries()) device.button.setAttribute('aria-pressed', String(i === index));
    for (const thread of threads) {
      thread.visible = thread.messages.filter(message => selectedDevice === null || message.dataset.device === selectedDevice);
      for (const message of thread.messages) message.hidden = selectedDevice !== null && message.dataset.device !== selectedDevice;
      thread.initialized = false;
      const last = thread.visible.at(-1);
      thread.epoch = last ? Number(last.dataset.epoch) : -Infinity;
      thread.lastID = last ? Number(last.dataset.id) : 0;
      thread.snippet.textContent = last ? last.querySelector('.message-bubble').textContent.replace(/\s+/g, ' ').trim() : '';
      thread.time.textContent = last ? last.dataset.time.slice(5, 16).replace('T', ' ') : '';
      thread.time.title = last ? last.dataset.time : '';
      thread.phone.textContent = last ? (last.dataset.phone || last.dataset.device) : '';
      thread.section.querySelector('.thread-local').textContent = '本机：' + (thread.phone.textContent || '未知');
      thread.phone.title = thread.phone.textContent + (thread.section.dataset.iccid ? ' · ICCID: ' + thread.section.dataset.iccid : '');
      thread.unread.hidden = !thread.visible.some(message => message.dataset.read === 'false' && message.classList.contains('incoming'));
    }
    threads.sort((a, b) => b.epoch - a.epoch || b.lastID - a.lastID);
    const fragment = document.createDocumentFragment();
    for (const thread of threads) fragment.append(thread.button);
    list.append(fragment);
    applySearch();
  }

  function applySearch() {
    const query = search.value.trim().toLocaleLowerCase();
    const matching = [];
    for (const thread of threads) {
      const matches = thread.visible.length > 0 && (!query || thread.section.dataset.peer.toLocaleLowerCase().includes(query) || thread.visible.some(message => message.querySelector('.message-bubble').textContent.toLocaleLowerCase().includes(query)));
      thread.button.hidden = !matches;
      if (matches) matching.push(thread);
    }
    noContacts.hidden = matching.length > 0;
    list.hidden = matching.length === 0;
    selectThread(active && matching.includes(active) ? active : (matching[0] || null));
    if (!active) reader.classList.remove('show-thread');
  }

  for (const thread of threads) thread.section.hidden = true;
  document.documentElement.classList.add('reader-ready');
  deviceSelect.addEventListener('change', () => setDevice(Number(deviceSelect.value)));
  search.addEventListener('input', applySearch);
  // Opening a conversation only changes this document's view, never its saved read state.
  setDevice(0);
})();
