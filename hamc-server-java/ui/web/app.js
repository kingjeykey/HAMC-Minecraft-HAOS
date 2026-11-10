'use strict';
(function () {
  var BASE = location.pathname.replace(/[^/]*$/, '');
  var $ = function (id) { return document.getElementById(id); };
  var nf = new Intl.NumberFormat('de-DE', { maximumFractionDigits: 1 });
  var last = { stats: null, hist: [], diag: null };

  function esc(s) { return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) { return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]; }); }
  function fmt(n, d) { return n == null ? '–' : new Intl.NumberFormat('de-DE', { maximumFractionDigits: d == null ? 1 : d }).format(n); }
  function fmtSize(b) {
    if (b == null) return '';
    var u = ['B', 'KB', 'MB', 'GB', 'TB'], i = 0;
    while (b >= 1024 && i < u.length - 1) { b /= 1024; i++; }
    return nf.format(b) + ' ' + u[i];
  }
  function fmtDate(t) {
    if (!t) return '';
    return new Date(t * 1000).toLocaleString('de-DE', { dateStyle: 'short', timeStyle: 'short' });
  }
  function toast(msg) {
    var t = $('toast'); t.textContent = msg; t.hidden = false;
    clearTimeout(toast.t); toast.t = setTimeout(function () { t.hidden = true; }, 4000);
  }
  function api(method, path, body) {
    var opt = { method: method, headers: { 'X-Requested-With': 'hamc' } };
    if (body !== undefined) { opt.headers['Content-Type'] = 'application/json'; opt.body = JSON.stringify(body); }
    return fetch(BASE + 'api/' + path, opt).then(function (r) {
      return r.json().catch(function () { return {}; }).then(function (j) {
        if (!r.ok) { var e = new Error(j.error || ('Fehler ' + r.status)); e.status = r.status; throw e; }
        return j;
      });
    });
  }

  /* ---------- Dialog ---------- */
  function dialog(html, opts) {
    opts = opts || {};
    return new Promise(function (resolve) {
      var d = $('dlg');
      d.className = opts.wide ? 'wide' : '';
      d.innerHTML = '<form method="dialog">' + html + '</form>';
      d.returnValue = '';
      var f = d.querySelector('form');
      f.addEventListener('submit', function (e) {
        if (opts.validate && d.returnValue !== 'cancel') {
          var msg = opts.validate(f);
          if (msg) { e.preventDefault(); var er = f.querySelector('.err'); if (er) er.textContent = msg; }
        }
      });
      var cancel = f.querySelector('[value="cancel"]');
      if (cancel) cancel.addEventListener('click', function () { d.returnValue = 'cancel'; });
      d.onclose = function () { resolve(d.returnValue === 'ok' ? f : null); };
      d.showModal();
      var focus = f.querySelector('input,textarea'); if (focus) { focus.focus(); if (focus.select && focus.tagName === 'INPUT') focus.select(); }
    });
  }
  function btns(okText, danger) {
    return '<div class="btns"><button class="btn ' + (danger ? 'danger' : 'primary') + '" value="ok" type="submit" onclick="this.form.closest(\'dialog\').returnValue=\'ok\'">' + esc(okText) + '</button>' +
      '<button class="btn" value="cancel" type="submit" formnovalidate>Abbrechen</button></div>';
  }
  function okValue() { $('dlg').returnValue = 'ok'; }
  function confirmDlg(title, text, okText) {
    return dialog('<h3>' + esc(title) + '</h3><p>' + esc(text) + '</p>' + btns(okText, true)).then(function (f) { return !!f; });
  }
  function promptDlg(title, label, value, okText, hint) {
    return dialog('<h3>' + esc(title) + '</h3>' + (hint ? '<p class="hint">' + esc(hint) + '</p>' : '') +
      '<label>' + esc(label) + '<input type="text" name="v" value="' + esc(value || '') + '" required></label><p class="err"></p>' + btns(okText), {
      validate: function (f) { return f.v.value.trim() ? '' : 'Bitte einen Wert eingeben.'; }
    }).then(function (f) { return f ? f.v.value.trim() : null; });
  }

  /* ---------- Avatare ---------- */
  function hash(s) { var h = 2166136261; for (var i = 0; i < s.length; i++) { h ^= s.charCodeAt(i); h = Math.imul(h, 16777619); } return h >>> 0; }
  function avatar(name) {
    var h = hash(name || '?'), hue = h % 360, r = '';
    var fg = 'hsl(' + hue + ',55%,52%)', bg = 'hsl(' + hue + ',25%,28%)';
    var bits = h;
    r += '<rect width="5" height="5" fill="' + bg + '"/>';
    for (var y = 0; y < 5; y++) for (var x = 0; x < 3; x++) {
      bits = Math.imul(bits, 1103515245) + 12345 >>> 0;
      if ((bits >> 16) & 1) {
        r += '<rect x="' + x + '" y="' + y + '" width="1" height="1" fill="' + fg + '"/>';
        if (x < 2) r += '<rect x="' + (4 - x) + '" y="' + y + '" width="1" height="1" fill="' + fg + '"/>';
      }
    }
    return '<svg class="avatar" width="24" height="24" viewBox="0 0 5 5" aria-hidden="true">' + r + '</svg>';
  }

  /* ---------- Tabs ---------- */
  var tabs = ['overview', 'console', 'whitelist', 'mods', 'files'], cur = null;
  function showTab(name) {
    if (tabs.indexOf(name) < 0) name = 'overview';
    cur = name;
    tabs.forEach(function (t) {
      $('tab-' + t).hidden = t !== name;
      document.querySelector('[data-tab="' + t + '"]').setAttribute('aria-selected', t === name ? 'true' : 'false');
    });
    try { history.replaceState(null, '', '#' + name); } catch (e) { /* ignore */ }
    if (name === 'console') consoleOpen(); else consoleClose();
    if (name === 'whitelist') wlLoad();
    if (name === 'files') filesLoad(fpath);
    if (name === 'mods') modLoad();
    if (name === 'overview') render();
  }
  document.querySelector('.tabs').addEventListener('click', function (e) {
    var b = e.target.closest('[data-tab]'); if (b) showTab(b.dataset.tab);
  });

  /* ---------- Polling ---------- */
  function poll() {
    if (document.hidden) return;
    Promise.all([api('GET', 'stats'), api('GET', 'history'), api('GET', 'diag')]).then(function (r) {
      last.stats = r[0]; last.hist = r[1] || []; last.diag = r[2];
      render();
    }).catch(function (e) {
      var s = $('state'); s.textContent = 'Keine Verbindung'; s.dataset.s = 'bad';
    });
  }
  document.addEventListener('visibilitychange', function () { if (!document.hidden) poll(); });
  setInterval(poll, 5000);

  /* ---------- Übersicht ---------- */
  function render() {
    var s = last.stats; if (!s) return;
    var st = $('state');
    st.textContent = s.online ? 'Online' : 'Offline';
    st.dataset.s = s.online ? 'ok' : 'bad';
    if (cur !== 'overview') return;
    renderBudget(s); renderPlayers(s); renderFacts(s); renderSparks(); renderHa();
    $('diag').textContent = JSON.stringify(last.diag, null, 2);
    var note = $('ovNote');
    if (s.online && !s.rcon_ok) {
      note.hidden = false;
      note.textContent = 'RCON ist nicht erreichbar' + (s.rcon_error ? ' (' + s.rcon_error + ')' : '') + '. Ohne RCON gibt es keine TPS-Werte, keine Konsoleneingabe und keine Whitelist-Änderungen.';
    } else note.hidden = true;
  }
  function renderBudget(s) {
    var el = $('budget'), slots = 25, on = 0, cls = 'hot-ok', verdict = '', big = '', sub = '';
    if (!s.online) {
      el.innerHTML = '<h2>Performance</h2><div class="big off">Offline</div><p class="muted">Der Server antwortet nicht. Beim Starten kann das einige Minuten dauern.</p>';
      return;
    }
    if (s.mspt != null) {
      var m = s.mspt;
      on = Math.max(1, Math.min(slots, Math.round(m / 50 * slots)));
      if (m < 30) { cls = 'hot-ok'; verdict = 'Läuft flüssig'; } else if (m <= 50) { cls = 'hot-warn'; verdict = 'Wird knapp'; } else { cls = 'hot-bad'; verdict = 'Überlastet'; }
      big = fmt(m) + ' <small>ms pro Tick</small>';
      sub = 'Ein Tick darf höchstens 50 ms dauern.' + (s.tps != null ? ' TPS: ' + fmt(s.tps) + ' von 20.' : '') + (s.mspt_p95 != null ? ' 95 % der Ticks unter ' + fmt(s.mspt_p95) + ' ms.' : '');
    } else if (s.tps != null) {
      on = Math.round((20 - Math.min(20, s.tps)) / 20 * slots);
      if (s.tps >= 19) { cls = 'hot-ok'; verdict = 'Läuft flüssig'; } else if (s.tps >= 15) { cls = 'hot-warn'; verdict = 'Wird knapp'; } else { cls = 'hot-bad'; verdict = 'Überlastet'; }
      big = fmt(s.tps) + ' <small>TPS von 20</small>';
      sub = 'Dieser Servertyp liefert keine Tickzeit, nur die Ticks pro Sekunde.';
      on = Math.max(1, on);
    } else {
      el.innerHTML = '<h2>Performance</h2><div class="big">–</div><p class="muted">' + (s.rcon_ok ? 'Dieser Server meldet keine TPS-Werte. Details stehen unter Technische Details.' : 'Für TPS-Werte wird RCON benötigt.') + '</p>';
      return;
    }
    var h = '';
    for (var i = 0; i < slots; i++) h += '<span class="slot' + (i < on ? ' on' : '') + '"></span>';
    el.className = 'budget ' + cls;
    el.innerHTML = '<h2>Performance</h2><div class="big">' + big + '</div><div class="hotbar" role="img" aria-label="' + esc(verdict) + '">' + h + '</div><p><span class="verdict">' + verdict + '.</span> <span class="muted">' + esc(sub) + '</span></p>';
  }
  function renderPlayers(s) {
    var h = '<h2>Spieler<span class="count">' + s.players_online + ' von ' + (s.players_max || '–') + '</span></h2>';
    if (!s.online) h += '<p class="muted">Server ist offline.</p>';
    else if (!s.players || !s.players.length) h += '<p class="muted">' + (s.players_online ? 'Namen werden vom Server nicht mitgeteilt.' : 'Niemand ist online.') + '</p>';
    else h += '<ul class="plist">' + s.players.map(function (p) { return '<li>' + avatar(p) + esc(p) + '</li>'; }).join('') + '</ul>';
    $('players').innerHTML = h;
  }
  function renderFacts(s) {
    var a = s.addon || {}, rows = [
      ['Version', s.online ? esc(s.version || '–') : '–'],
      ['MOTD', s.online ? esc(s.motd || '–') : '–'],
      ['Antwortzeit', s.latency_ms != null ? fmt(s.latency_ms, 0) + ' ms' : '–'],
      ['Prozessor (Add-on)', a.cpu != null ? fmt(a.cpu) + ' %' : '–'],
      ['Arbeitsspeicher', a.mem_used_mb == null ? '–' : fmt(a.mem_used_mb, 0) + ' MB von ' + fmt(a.mem_limit_mb, 0) + ' MB (' + fmt(a.mem_percent) + ' %)'],
      ['Freier Speicher', fmt(s.disk_free_gb) + ' GB von ' + fmt(s.disk_total_gb) + ' GB'],
      ['Whitelist', (s.whitelist_enabled ? 'aktiv, ' : 'aus, ') + s.whitelist_count + ' Einträge'],
      ['Tick-Messung', s.tick_method ? esc(s.tick_method) : '–']
    ];
    $('facts').innerHTML = rows.map(function (r) { return '<dt>' + r[0] + '</dt><dd>' + r[1] + '</dd>'; }).join('');
  }
  function spark(label, key, unit, digits) {
    var pts = last.hist.map(function (p) { return p[key]; });
    var vals = pts.filter(function (v) { return v != null; });
    if (vals.length < 2) return '<div class="spark"><div class="lbl"><span>' + label + '</span><span class="val">–</span></div></div>';
    var min = Math.min.apply(null, vals), max = Math.max.apply(null, vals);
    if (max === min) { max += 1; min -= 1; }
    var n = pts.length, d = '', pen = false;
    pts.forEach(function (v, i) {
      if (v == null) { pen = false; return; }
      var x = n > 1 ? i / (n - 1) * 100 : 0, y = 38 - (v - min) / (max - min) * 36;
      d += (pen ? 'L' : 'M') + x.toFixed(2) + ' ' + y.toFixed(2); pen = true;
    });
    var lastV = vals[vals.length - 1];
    return '<div class="spark"><div class="lbl"><span>' + label + '</span><span class="val">' + fmt(lastV, digits) + ' ' + unit + '</span></div>' +
      '<svg viewBox="0 0 100 40" preserveAspectRatio="none" role="img" aria-label="' + esc(label) + ' im Verlauf"><line x1="0" y1="39" x2="100" y2="39"/><path d="' + d + '"/></svg></div>';
  }
  function renderSparks() {
    $('sparks').innerHTML = spark('Spieler', 'players', '', 0) + spark('Tickzeit', 'mspt', 'ms', 1) + spark('TPS', 'tps', '', 1) + spark('Prozessor', 'cpu', '%', 1) + spark('Arbeitsspeicher', 'mem', '%', 1);
  }
  function renderHa() {
    var d = last.diag || {}, m = d.mqtt || '';
    var t;
    if (m.indexOf('verbunden') === 0) t = 'MQTT ist ' + m + '. Die Werte erscheinen in Home Assistant unter dem Gerät „HAMC Minecraft Server“ (Einstellungen, Geräte & Dienste, MQTT).';
    else if (m === 'deaktiviert') t = 'MQTT ist in der Add-on-Konfiguration ausgeschaltet.';
    else t = 'MQTT: ' + (m || 'wird gestartet') + '. Installiere den Mosquitto-Broker und die MQTT-Integration, oder trage den Broker in der Add-on-Konfiguration ein.';
    $('haInfo').textContent = t;
  }

  /* ---------- Konsole ---------- */
  var es = null, hist = [], hpos = -1, MAXLN = 2000;
  function addLine(text, cls) {
    var o = $('conOut'), el = document.createElement('div');
    el.className = 'ln' + (cls ? ' ' + cls : '');
    if (!cls) {
      if (/\/(ERROR|FATAL)\]|Exception|\bERROR\b/.test(text)) el.className += ' err';
      else if (/\/WARN\]|\bWARN\b/.test(text)) el.className += ' warn';
    }
    el.textContent = text; o.appendChild(el);
    while (o.childNodes.length > MAXLN) o.removeChild(o.firstChild);
    if ($('conAuto').checked) o.scrollTop = o.scrollHeight;
  }
  function consoleOpen() {
    var s = last.stats;
    var n = $('conNote');
    if (s && s.online && !s.rcon_ok) { n.hidden = false; n.textContent = 'Eingaben gehen nur mit RCON. Das Log kannst du trotzdem lesen.'; } else n.hidden = true;
    if (es) return;
    $('conOut').innerHTML = '';
    $('conStatus').textContent = 'Verbinde';
    es = new EventSource(BASE + 'api/console/stream');
    es.onopen = function () { $('conStatus').textContent = 'Live'; };
    es.onmessage = function (e) { addLine(e.data); };
    es.addEventListener('info', function (e) { addLine(e.data, 'info'); });
    es.onerror = function () { $('conStatus').textContent = 'Verbindung unterbrochen, versuche erneut'; };
  }
  function consoleClose() { if (es) { es.close(); es = null; } }
  $('conClear').addEventListener('click', function () { $('conOut').innerHTML = ''; });
  function sendCmd(cmd) {
    cmd = cmd.trim(); if (!cmd) return Promise.resolve();
    addLine('> ' + cmd, 'cmd');
    return api('POST', 'console/command', { cmd: cmd }).then(function (r) {
      if (r.output) r.output.split('\n').forEach(function (l) { addLine(l, 'resp'); });
    }).catch(function (e) { addLine(e.message, 'err'); });
  }
  $('conForm').addEventListener('submit', function (e) {
    e.preventDefault();
    var v = $('conIn').value;
    if (!v.trim()) return;
    if (hist[hist.length - 1] !== v) hist.push(v);
    hpos = hist.length; $('conIn').value = '';
    sendCmd(v);
  });
  $('conIn').addEventListener('keydown', function (e) {
    if (e.key === 'ArrowUp' && hist.length) { e.preventDefault(); hpos = Math.max(0, hpos - 1); this.value = hist[hpos]; }
    else if (e.key === 'ArrowDown' && hist.length) { e.preventDefault(); hpos = Math.min(hist.length, hpos + 1); this.value = hist[hpos] || ''; }
  });
  document.querySelector('#tab-console .chips').addEventListener('click', function (e) {
    var b = e.target.closest('[data-cmd]'); if (b) sendCmd(b.dataset.cmd);
  });

  /* ---------- Whitelist ---------- */
  var wlData = null;
  function wlMsg(text, kind) {
    var m = $('wlMsg'); if (!text) { m.hidden = true; return; }
    m.hidden = false; m.textContent = text; m.className = 'msg ' + (kind || '');
  }
  function wlTranslate(out) {
    if (!out) return '';
    var o = out.trim();
    var m;
    if ((m = /^Added (.+) to the whitelist/.exec(o))) return m[1] + ' wurde zur Whitelist hinzugefügt.';
    if ((m = /^Removed (.+) from the whitelist/.exec(o))) return m[1] + ' wurde von der Whitelist entfernt.';
    if (/already whitelisted/i.test(o)) return 'Dieser Spieler steht schon auf der Whitelist.';
    if (/not whitelisted|does not exist/i.test(o)) return 'Dieser Spieler steht nicht auf der Whitelist.';
    if (/Player does not exist|That player does not exist/i.test(o)) return 'Diesen Spieler gibt es nicht.';
    return o;
  }
  function wlLoad() {
    api('GET', 'whitelist').then(function (d) { wlData = d; wlRender(); }).catch(function (e) {
      wlMsg(e.message, 'bad');
    });
  }
  function wlRender() {
    var d = wlData; if (!d) return;
    var n = $('wlNote'), noRcon = !d.rcon_ok;
    if (noRcon) {
      n.hidden = false;
      n.textContent = d.server_online ? 'RCON ist nicht erreichbar' + (d.rcon_error ? ' (' + d.rcon_error + ')' : '') + '. Änderungen sind erst möglich, wenn RCON funktioniert.' : 'Der Server ist offline. Änderungen sind erst möglich, wenn er läuft.';
    } else n.hidden = true;
    $('wlEnabled').checked = !!d.enabled;
    ['wlEnabled', 'wlReload', 'wlName'].forEach(function (id) { $(id).disabled = noRcon; });
    $('wlForm').querySelector('button').disabled = noRcon;
    var list = d.players || [];
    $('wlEmpty').hidden = list.length > 0;
    $('wlBody').innerHTML = list.map(function (p) {
      return '<tr><td><div class="nm-cell">' + avatar(p.name) + esc(p.name) + '</div></td><td class="uuid">' + esc(p.uuid) + '</td><td class="ac"><button class="lnk danger" data-rm="' + esc(p.name) + '"' + (noRcon ? ' disabled' : '') + '>Entfernen</button></td></tr>';
    }).join('');
    var sy = d.sync || {}, t;
    if (sy.error) t = 'Abgleich mit der Add-on-Konfiguration fehlgeschlagen: ' + sy.error;
    else if (sy.last_sync) t = 'Mit der Add-on-Konfiguration abgeglichen: ' + new Date(sy.last_sync * 1000).toLocaleString('de-DE', { dateStyle: 'short', timeStyle: 'medium' });
    else t = 'Änderungen werden automatisch in die Option WHITELIST der Konfiguration übernommen.';
    $('wlSync').textContent = t;
  }
  function wlAct(path, body, okText) {
    return api('POST', path, body).then(function (r) {
      var text = wlTranslate(r.output) || okText;
      wlMsg(r.sync_error ? text + ' Abgleich mit der Konfiguration fehlgeschlagen: ' + r.sync_error : text, r.sync_error ? 'warn' : 'ok');
      wlLoad();
    }).catch(function (e) { wlMsg(e.message, 'bad'); });
  }
  $('wlForm').addEventListener('submit', function (e) {
    e.preventDefault();
    var n = $('wlName').value.trim();
    if (!/^[A-Za-z0-9_.\-]{1,32}$/.test(n)) { wlMsg('Ein Spielername besteht aus 1 bis 32 Buchstaben, Ziffern, Punkt, Bindestrich oder Unterstrich.', 'bad'); return; }
    wlAct('whitelist/add', { name: n }, n + ' wurde hinzugefügt.').then(function () { $('wlName').value = ''; });
  });
  $('wlBody').addEventListener('click', function (e) {
    var b = e.target.closest('[data-rm]'); if (!b) return;
    var n = b.dataset.rm;
    confirmDlg(n + ' entfernen?', n + ' kann danach nicht mehr beitreten.', 'Entfernen').then(function (ok) {
      if (ok) wlAct('whitelist/remove', { name: n }, n + ' wurde entfernt.');
    });
  });
  $('wlEnabled').addEventListener('change', function () {
    var on = this.checked, box = this;
    wlAct('whitelist/toggle', { on: on }, on ? 'Whitelist ist jetzt aktiv.' : 'Whitelist ist jetzt aus.').then(function () { box.checked = !!(wlData && wlData.enabled); });
  });
  $('wlReload').addEventListener('click', function () { wlAct('whitelist/reload', {}, 'Whitelist neu eingelesen.'); });

  /* ---------- Dateien ---------- */
  var fpath = '', fdata = null;
  var SHORT = [['Hauptordner', ''], ['Plugins', 'plugins'], ['Mods', 'mods'], ['Konfiguration', 'config'], ['Logs', 'logs']];
  function ico(dir) {
    return dir ? '<svg class="ico dir" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d="M3 6a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/></svg>'
      : '<svg class="ico file" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true"><path d="M6 3h8l5 5v13H6z"/><path d="M14 3v5h5"/></svg>';
  }
  function join(a, b) { return a ? a + '/' + b : b; }
  function q(p) { return encodeURIComponent(p); }
  function normPath(p) { p = String(p || '').replace(/^[\/.]+|\/+$/g, ''); return p; }
  function filesLoad(p) {
    api('GET', 'files/list?path=' + q(p)).then(function (d) {
      fdata = d; fpath = normPath(d.path);
      filesRender();
    }).catch(function (e) {
      toast(e.message);
      if (p) { fpath = ''; filesLoad(''); }
    });
    jobsPoll();
  }
  function filesRender() {
    var d = fdata, parts = fpath ? fpath.split('/') : [], acc = '';
    var c = '<button data-go="">Server</button>';
    parts.forEach(function (n, i) {
      acc = join(acc, n);
      c += '<span>/</span>' + (i === parts.length - 1 ? '<span class="cur">' + esc(n) + '</span>' : '<button data-go="' + esc(acc) + '">' + esc(n) + '</button>');
    });
    $('crumbs').innerHTML = c;
    $('fShort').innerHTML = SHORT.map(function (s) { return '<button class="chip" data-go="' + s[1] + '">' + s[0] + '</button>'; }).join('');
    $('fFree').textContent = d.disk_free != null ? fmtSize(d.disk_free) + ' frei' : '';
    var rows = (d.entries || []).slice().sort(function (a, b) { return (b.dir - a.dir) || a.name.localeCompare(b.name, 'de', { numeric: true }); });
    if (fpath) rows.unshift({ up: true, name: '..', dir: true });
    $('fEmpty').hidden = rows.length > 0;
    $('fBody').innerHTML = rows.map(function (e) {
      if (e.up) return '<tr><td><div class="nm-cell">' + ico(true) + '<button class="nmbtn" data-go="' + esc(parentOf(fpath)) + '">Eine Ebene hoch</button></div></td><td></td><td class="md"></td><td></td></tr>';
      var p = join(fpath, e.name), a = '';
      a += '<button class="lnk" data-act="dl" data-p="' + esc(p) + '">' + (e.dir ? 'Als ZIP' : 'Laden') + '</button>';
      if (!e.dir && /\.zip$/i.test(e.name)) a += '<button class="lnk" data-act="unzip" data-p="' + esc(p) + '">Entpacken</button>';
      if (!e.dir && /\.mrpack$/i.test(e.name)) a += '<button class="lnk" data-act="mrpack" data-p="' + esc(p) + '">Modpack installieren</button>';
      if (!e.dir) a += '<button class="lnk" data-act="edit" data-p="' + esc(p) + '">Bearbeiten</button>';
      a += '<button class="lnk" data-act="ren" data-p="' + esc(p) + '" data-n="' + esc(e.name) + '">Umbenennen</button>';
      a += '<button class="lnk danger" data-act="del" data-p="' + esc(p) + '" data-n="' + esc(e.name) + '">Löschen</button>';
      var name = e.dir ? '<button class="nmbtn" data-go="' + esc(p) + '">' + esc(e.name) + '</button>' : '<span>' + esc(e.name) + '</span>';
      return '<tr><td><div class="nm-cell">' + ico(e.dir) + name + (e.world ? '<span class="tag">Welt</span>' : '') + '</div></td><td class="sz">' + (e.dir ? '' : fmtSize(e.size)) + '</td><td class="md">' + fmtDate(e.mod) + '</td><td class="ac">' + a + '</td></tr>';
    }).join('');
  }
  function parentOf(p) { var i = p.lastIndexOf('/'); return i < 0 ? '' : p.slice(0, i); }
  function go(e) { var b = e.target.closest('[data-go]'); if (b) filesLoad(b.dataset.go); }
  $('crumbs').addEventListener('click', go);
  $('fShort').addEventListener('click', go);
  $('fBody').addEventListener('click', function (e) {
    go(e);
    var b = e.target.closest('[data-act]'); if (!b) return;
    var p = b.dataset.p, n = b.dataset.n, act = b.dataset.act;
    if (act === 'dl') {
      var a = document.createElement('a'); a.href = BASE + 'api/files/download?path=' + q(p); a.download = ''; document.body.appendChild(a); a.click(); a.remove();
    } else if (act === 'del') {
      confirmDlg(n + ' löschen?', 'Das lässt sich nicht rückgängig machen. Ordner werden mit allem Inhalt gelöscht.', 'Löschen').then(function (ok) {
        if (ok) api('POST', 'files/delete', { path: p }).then(function () { toast('Gelöscht'); filesLoad(fpath); }).catch(function (er) { toast(er.message); });
      });
    } else if (act === 'ren') {
      promptDlg('Umbenennen', 'Neuer Name', n, 'Umbenennen').then(function (v) {
        if (v && v !== n) api('POST', 'files/rename', { path: p, name: v }).then(function () { filesLoad(fpath); }).catch(function (er) { toast(er.message); });
      });
    } else if (act === 'unzip') {
      api('POST', 'files/unzip', { path: p }).then(function (r) { toast('Entpacken gestartet'); jobsPoll(); }).catch(function (er) { toast(er.message); });
    } else if (act === 'mrpack') installMrpack(p);
    else if (act === 'edit') editFile(p);
  });
  function installMrpack(p) {
    api('POST', 'mrpack/inspect', { path: p }).then(function (m) {
      var loader = m.type ? m.loader + (m.loader_version ? ' ' + m.loader_version : '') : 'unbekannt';
      var h = '<h3>' + esc(m.name || 'Modpack') + (m.version ? ' ' + esc(m.version) : '') + '</h3>' +
        (m.summary ? '<p class="hint">' + esc(m.summary) + '</p>' : '') +
        '<dl class="facts" style="grid-template-columns:9rem 1fr;margin-bottom:12px">' +
        '<dt>Minecraft</dt><dd>' + esc(m.minecraft || '–') + '</dd>' +
        '<dt>Mod-Loader</dt><dd>' + esc(loader) + '</dd>' +
        '<dt>Dateien</dt><dd>' + m.files + ' (' + fmtSize(m.size) + ')' + (m.skipped_client ? ', übersprungen (nur für Clients): ' + m.skipped_client : '') + '</dd>' +
        (m.current_type ? '<dt>Jetzt eingestellt</dt><dd>' + esc(m.current_type) + ' ' + esc(m.current_version || '') + '</dd>' : '') + '</dl>';
      if (m.can_apply) h += '<label class="check"><input type="checkbox" name="apply" checked> Servertyp ' + esc(m.type) + ' und Version ' + esc(m.minecraft) + ' in der Konfiguration eintragen</label>';
      else h += '<p class="hint">Servertyp und Version musst du selbst in der Konfiguration einstellen.</p>';
      h += '<p class="hint" style="margin-top:10px">Vorhandene Mods werden nicht gelöscht. Nach der Installation das Add-on neu starten. Das Add-on wählt die Loader-Version selbst, meist die neueste.</p>' + btns('Installieren');
      return dialog(h).then(function (f) {
        if (!f) return;
        var apply = !!(f.apply && f.apply.checked);
        return api('POST', 'mrpack/install', { path: p, apply: apply }).then(function (r) {
          toast(r.apply_error ? 'Installation gestartet. Konfiguration nicht geändert: ' + r.apply_error : 'Installation gestartet' + (r.applied ? ', Konfiguration ist angepasst' : ''));
          jobsPoll();
        });
      });
    }).catch(function (e) { toast(e.message); });
  }
  function editFile(p) {
    api('GET', 'files/read?path=' + q(p)).then(function (r) {
      dialog('<h3>' + esc(p) + '</h3><p class="hint">Änderungen wirken meist erst nach einem Neustart des Servers.</p><textarea name="c" spellcheck="false"></textarea><p class="err"></p>' + btns('Speichern'), { wide: true });
      $('dlg').querySelector('textarea').value = r.content;
    }).catch(function (e) {
      toast(e.status === 415 ? 'Diese Datei ist keine Textdatei und kann nicht bearbeitet werden.' : e.message);
    });
  }
  // Der Editor wird ohne Promise-Verkettung über das close-Ereignis gespeichert.
  $('dlg').addEventListener('close', function () {
    var d = $('dlg'), t = d.querySelector('textarea[name="c"]');
    if (!t || d.returnValue !== 'ok') return;
    var p = d.querySelector('h3').textContent;
    api('POST', 'files/write', { path: p, content: t.value }).then(function () { toast('Gespeichert'); filesLoad(fpath); }).catch(function (e) { toast(e.message); });
  });

  /* Hochladen */
  function upload(files, dest, after) {
    if (dest == null) dest = fpath;
    after = after || function () { filesLoad(fpath); };
    files = Array.prototype.slice.call(files); if (!files.length) return;
    var i = 0;
    $('upBar').hidden = false;
    (function next() {
      if (i >= files.length) {
        $('upBar').hidden = true; toast(files.length + (files.length === 1 ? ' Datei hochgeladen' : ' Dateien hochgeladen')); after();
        var pack = files.filter(function (f) { return /\.mrpack$/i.test(f.name); })[0];
        if (pack && dest === fpath) installMrpack(join(fpath, pack.name));
        return;
      }
      var f = files[i++], fd = new FormData(); fd.append('file', f, f.name);
      var x = new XMLHttpRequest();
      x.open('POST', BASE + 'api/files/upload?path=' + q(dest));
      x.setRequestHeader('X-Requested-With', 'hamc');
      x.upload.onprogress = function (e) {
        if (e.lengthComputable) { $('upProg').value = e.loaded / e.total * 100; $('upTxt').textContent = f.name + ' (' + i + '/' + files.length + '): ' + fmtSize(e.loaded) + ' von ' + fmtSize(e.total); }
      };
      x.onerror = function () { toast('Upload von ' + f.name + ' fehlgeschlagen'); next(); };
      x.onload = function () {
        if (x.status >= 300) { var m = 'Fehler ' + x.status; try { m = JSON.parse(x.responseText).error || m; } catch (e) { /* ignore */ } toast(f.name + ': ' + m); }
        next();
      };
      x.send(fd);
    })();
  }
  $('fUpload').addEventListener('click', function () { $('fPick').click(); });
  $('fPick').addEventListener('change', function () { upload(this.files); this.value = ''; });
  var drop = $('fDrop');
  ['dragenter', 'dragover'].forEach(function (ev) { drop.addEventListener(ev, function (e) { e.preventDefault(); drop.classList.add('over'); }); });
  ['dragleave', 'drop'].forEach(function (ev) { drop.addEventListener(ev, function (e) { e.preventDefault(); drop.classList.remove('over'); }); });
  drop.addEventListener('drop', function (e) {
    var items = e.dataTransfer.items, files = [], skipped = false;
    if (items && items.length && items[0].webkitGetAsEntry) {
      for (var i = 0; i < items.length; i++) {
        var en = items[i].webkitGetAsEntry();
        if (en && en.isDirectory) skipped = true; else if (items[i].getAsFile()) files.push(items[i].getAsFile());
      }
    } else files = Array.prototype.slice.call(e.dataTransfer.files);
    if (skipped) toast('Ordner können nicht hochgeladen werden. Packe sie als ZIP und entpacke sie danach hier.');
    upload(files);
  });
  $('fMkdir').addEventListener('click', function () {
    promptDlg('Neuer Ordner', 'Name', '', 'Anlegen').then(function (v) {
      if (v) api('POST', 'files/mkdir', { path: fpath, name: v }).then(function () { filesLoad(fpath); }).catch(function (e) { toast(e.message); });
    });
  });
  $('fFetch').addEventListener('click', function () {
    promptDlg('Von Adresse laden', 'Adresse (http oder https)', '', 'Laden', 'Lädt die Datei direkt auf den Server, zum Beispiel ein Plugin von der Download-Seite. Das ist die beste Lösung für große Dateien.').then(function (v) {
      if (v) api('POST', 'files/fetch', { path: fpath, url: v }).then(function () { toast('Download gestartet'); jobsPoll(); }).catch(function (e) { toast(e.message); });
    });
  });

  /* Hintergrundaufgaben */
  var jobTimer = null, seenDone = {};
  function jobsPoll() {
    api('GET', 'jobs').then(function (list) {
      var running = false, h = '', refresh = false;
      list.slice(0, 5).forEach(function (j) {
        if (j.status === 'running') running = true;
        if (j.status !== 'running' && !seenDone[j.id]) { seenDone[j.id] = 1; if (j.status === 'done' && Date.now() - new Date(j.ended).getTime() < 15000) refresh = true; }
        var pct = j.total > 0 ? Math.min(100, j.done / j.total * 100) : null;
        if (j.status === 'running' || Date.now() - new Date(j.ended || j.started).getTime() < 60000) {
          h += '<div class="job ' + j.status + '"><span>' + esc(j.name) + '</span>' +
            (j.status === 'running' ? (pct == null ? '<span class="muted">' + fmtSize(j.done) + '</span>' : '<progress max="100" value="' + pct + '"></progress><span class="muted">' + fmt(pct, 0) + ' %</span>') :
              j.status === 'done' ? '<span>fertig</span>' : '<span>Fehler: ' + esc(j.error) + '</span>') + '</div>';
        }
      });
      $('jobs').innerHTML = h; $('modJobs').innerHTML = h;
      if (refresh && cur === 'files') api('GET', 'files/list?path=' + q(fpath)).then(function (d) { fdata = d; filesRender(); }).catch(function () {});
      if (refresh && cur === 'mods') modReload();
      clearTimeout(jobTimer);
      if (running && (cur === 'files' || cur === 'mods')) jobTimer = setTimeout(jobsPoll, 2000);
    }).catch(function () {});
  }


  /* ---------- Mods und Plugins ---------- */
  var modInfo = null, modDir = null, modUpd = {}, modRows = [];
  function modLabel(info) {
    var f = info.folders || [];
    if (!info.supported) return 'Mods';
    if (f.length > 1) return 'Mods & Plugins';
    return f[0] === 'plugins' ? 'Plugins' : 'Mods';
  }
  function modInfoLoad() {
    return api('GET', 'mods/info').then(function (i) {
      modInfo = i; $('modTabBtn').textContent = modLabel(i);
      if (!modDir || i.folders.indexOf(modDir) < 0) modDir = i.folders[0];
      return i;
    });
  }
  function modNoun() { return modDir === 'plugins' ? 'Plugin' : 'Mod'; }
  function modLoad() {
    modInfoLoad().then(function (i) {
      var n = $('modNote');
      if (!i.supported) {
        n.hidden = false;
        n.textContent = 'Dein Servertyp' + (i.type ? ' (' + i.type + ')' : '') + ' unterstützt keine Mods oder Plugins. Stelle in der Konfiguration unter TYPE zum Beispiel FABRIC, NEOFORGE, FORGE, QUILT oder PAPER ein und starte das Add-on neu.';
      } else n.hidden = true;
      ['modSearchBtn', 'modCheck'].forEach(function (id) { $(id).disabled = !i.supported; });
      var fo = $('modFolders');
      if (i.folders.length > 1) {
        fo.hidden = false;
        fo.innerHTML = i.folders.map(function (f) { return '<button class="chip" data-dir="' + f + '" aria-pressed="' + (f === modDir) + '">' + (f === 'mods' ? 'Mods' : 'Plugins') + '</button>'; }).join('');
      } else fo.hidden = true;
      jobsPoll();
      return modReload();
    }).catch(function (e) { toast(e.message); });
  }
  $('modFolders').addEventListener('click', function (e) {
    var b = e.target.closest('[data-dir]'); if (!b) return;
    modDir = b.dataset.dir; modUpd = {}; $('modUpdates').hidden = true; modLoad();
  });
  function modReload() {
    return api('GET', 'mods/list?dir=' + q(modDir)).then(function (r) { modRows = r.mods || []; modRender(); });
  }
  function modRender() {
    var rows = modRows;
    $('modEmpty').hidden = rows.length > 0;
    $('modEmpty').textContent = 'Im Ordner ' + modDir + ' liegt noch nichts. Suche auf Modrinth oder lade eine .jar-Datei hoch.';
    $('modBody').innerHTML = rows.map(function (m) {
      var u = modUpd[m.file], title = m.name || m.file.replace(/\.jar(\.disabled)?$/, '');
      var ver = m.version ? esc(m.version) : '–';
      var name = esc(title) + (m.enabled ? '' : '<span class="badge off">Aus</span>') + (u ? '<span class="badge">Update ' + esc(u.latest) + '</span>' : '') + '<span class="sub">' + esc(m.file) + (m.kind ? ' · ' + esc(m.kind) : '') + '</span>';
      var a = '';
      if (u) a += '<button class="lnk" data-m="upd" data-f="' + esc(m.file) + '">Aktualisieren</button>';
      a += '<button class="lnk" data-m="tog" data-f="' + esc(m.file) + '">' + (m.enabled ? 'Ausschalten' : 'Einschalten') + '</button>';
      a += '<button class="lnk danger" data-m="del" data-f="' + esc(m.file) + '" data-n="' + esc(title) + '">Löschen</button>';
      return '<tr class="' + (m.enabled ? '' : 'off') + '"><td>' + name + '</td><td class="ver">' + ver + '</td><td class="sz">' + fmtSize(m.size) + '</td><td class="ac">' + a + '</td></tr>';
    }).join('');
  }
  $('modBody').addEventListener('click', function (e) {
    var b = e.target.closest('[data-m]'); if (!b) return;
    var f = b.dataset.f, act = b.dataset.m;
    if (act === 'tog') {
      api('POST', 'mods/toggle', { dir: modDir, file: f }).then(function (r) { toast(r.enabled ? 'Eingeschaltet' : 'Ausgeschaltet'); delete modUpd[f]; modReload(); }).catch(function (er) { toast(er.message); });
    } else if (act === 'del') {
      confirmDlg(b.dataset.n + ' löschen?', 'Die Datei ' + f + ' wird vom Server entfernt. Wenn du den ' + modNoun() + ' nur testen willst, nutze Ausschalten.', 'Löschen').then(function (ok) {
        if (ok) api('POST', 'mods/delete', { dir: modDir, file: f }).then(function () { toast('Gelöscht'); modReload(); }).catch(function (er) { toast(er.message); });
      });
    } else if (act === 'upd') modApply([f]);
  });
  function modApply(files) {
    var items = files.map(function (f) { return { file: f, version_id: modUpd[f].version_id }; });
    api('POST', 'mods/update', { dir: modDir, items: items }).then(function () {
      toast('Update gestartet'); files.forEach(function (f) { delete modUpd[f]; }); modUpdatesNote(null); jobsPoll();
    }).catch(function (e) { toast(e.message); });
  }
  function modUpdatesNote(r) {
    var n = $('modUpdates'), keys = Object.keys(modUpd);
    if (!r && !keys.length) { n.hidden = true; return; }
    n.hidden = false;
    var t = keys.length ? keys.length + (keys.length === 1 ? ' Update verfügbar. ' : ' Updates verfügbar. ') : 'Alles aktuell. ';
    if (r && r.unknown) t += r.unknown + ' Dateien kennt Modrinth nicht, sie werden nicht geprüft. ';
    n.innerHTML = esc(t) + (keys.length > 1 ? '<button class="btn small" id="modAll" type="button">Alle aktualisieren</button>' : '');
    var all = $('modAll'); if (all) all.addEventListener('click', function () { modApply(Object.keys(modUpd)); });
  }
  $('modCheck').addEventListener('click', function () {
    var b = this; b.disabled = true; b.textContent = 'Prüfe';
    api('POST', 'mods/updates', { dir: modDir }).then(function (r) {
      modUpd = {}; (r.updates || []).forEach(function (u) { modUpd[u.file] = u; });
      modUpdatesNote(r); modRender();
    }).catch(function (e) { toast(e.message); }).then(function () { b.disabled = !(modInfo && modInfo.supported); b.textContent = 'Updates prüfen'; });
  });
  $('modUpload').addEventListener('click', function () { $('modPick').click(); });
  $('modPick').addEventListener('change', function () {
    var all = Array.prototype.slice.call(this.files), jars = all.filter(function (f) { return /\.jar$/i.test(f.name); });
    this.value = '';
    if (jars.length < all.length) toast('Nur .jar-Dateien werden angenommen');
    if (jars.length) {
      api('GET', 'files/list?path=' + q(modDir)).catch(function () { return api('POST', 'files/mkdir', { path: '', name: modDir }); }).then(function () { upload(jars, modDir, modReload); });
    }
  });

  /* Modrinth-Suche */
  $('modSearchBtn').addEventListener('click', function () {
    var info = modInfo; if (!info || !info.supported) return;
    var timer = null, results = [];
    var html = '<h3>' + (modDir === 'plugins' ? 'Plugins' : 'Mods') + ' auf Modrinth suchen</h3>' +
      '<div class="mr-bar"><input type="text" id="mrQ" placeholder="Name oder Stichwort, leer zeigt die beliebtesten" aria-label="Suche" autocomplete="off">' +
      '<input type="text" id="mrV" class="ver" value="' + esc(info.mc || '') + '" placeholder="MC-Version" aria-label="Minecraft-Version"></div>' +
      '<p class="hint" id="mrHint" style="margin:8px 0 0"></p><div class="mr-list" id="mrList"></div>' +
      '<div class="btns"><button class="btn" value="cancel" type="submit" formnovalidate>Schließen</button></div>';
    dialog(html, { wide: true });
    var Q = $('mrQ'), V = $('mrV'), L = $('mrList'), H = $('mrHint');
    function run() {
      H.textContent = 'Suche';
      api('GET', 'mods/search?q=' + q(Q.value) + '&mc=' + q(V.value.trim())).then(function (r) {
        results = r.hits;
        H.textContent = r.total + ' Treffer für ' + (r.loaders || []).join(', ') + (r.mc ? ' auf Minecraft ' + r.mc : '') + (r.total > results.length ? ', die ersten ' + results.length + ' werden angezeigt' : '');
        L.innerHTML = results.map(function (h, i) {
          var client = h.server_side === 'unsupported';
          return '<div class="mr-hit">' + (h.icon ? '<img src="' + esc(h.icon) + '" alt="" loading="lazy" referrerpolicy="no-referrer">' : '<span class="noicon"></span>') +
            '<div class="body"><div class="t">' + esc(h.title) + ' <span class="muted" style="font-weight:400">von ' + esc(h.author || '?') + ' · ' + fmt(h.downloads / 1000, 0) + ' Tsd. Downloads</span></div><div class="d">' + esc(h.description) + '</div></div>' +
            (client ? '<span class="muted">Nur Client</span>' : '<button type="button" class="btn small primary" data-i="' + i + '">Installieren</button>') + '</div>';
        }).join('') || '<p class="muted">Nichts gefunden. Prüfe die Minecraft-Version oder suche anders.</p>';
      }).catch(function (e) { H.textContent = e.message; L.innerHTML = ''; });
    }
    Q.addEventListener('input', function () { clearTimeout(timer); timer = setTimeout(run, 350); });
    V.addEventListener('change', run);
    [Q, V].forEach(function (el) { el.addEventListener('keydown', function (e) { if (e.key === 'Enter') { e.preventDefault(); clearTimeout(timer); run(); } }); });
    L.addEventListener('click', function (e) {
      var b = e.target.closest('[data-i]'); if (!b) return;
      var h = results[+b.dataset.i];
      b.disabled = true; b.textContent = 'Gestartet';
      api('POST', 'mods/install', { dir: modDir, project: h.id, title: h.title, mc: V.value.trim() }).then(function () { toast(h.title + ': Installation gestartet'); jobsPoll(); }).catch(function (er) { toast(er.message); b.disabled = false; b.textContent = 'Installieren'; });
    });
    run(); Q.focus();
  });

  /* ---------- Start ---------- */
  var h = (location.hash || '').replace('#', '');
  showTab(tabs.indexOf(h) >= 0 ? h : 'overview');
  poll();
  modInfoLoad().catch(function () {});
})();
