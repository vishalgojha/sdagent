const $ = id => document.getElementById(id);

async function api(url, opts) {
  const res = await fetch(url, Object.assign({ cache: "no-store" }, opts || {}));
  const data = await res.json().catch(function() { return {}; });
  if (!res.ok && data && data.error) throw new Error(data.error);
  return data;
}

function esc(v) { return String(v == null ? "" : v).replace(/[&<>]/g, function(c) { return {"&":"&amp;","<":"&lt;",">":"&gt;"}[c]; }); }
function pill(el, label, kind) { el.className = "pill " + (kind || ""); el.innerHTML = "<i></i>" + esc(label); }
function fmtDur(ms) { if (!ms) return ""; var s = Math.round(ms / 1000), m = Math.floor(s / 60), r = s % 60; return m + ":" + ("0" + r).slice(-2); }
function nowTime() { var d = new Date(); return ("0" + d.getHours()).slice(-2) + ":" + ("0" + d.getMinutes()).slice(-2); }

/* ---------- View switching ---------- */
function setupNav() {
  var buttons = document.querySelectorAll("[data-view]");
  var views = {};
  document.querySelectorAll(".view").forEach(function(v) { views[v.id.replace("view-", "")] = v; });
  function go(name) {
    buttons.forEach(function(b) { b.classList.toggle("active", b.dataset.view === name); });
    Object.keys(views).forEach(function(k) { views[k].classList.toggle("active", k === name); });
    if (history.replaceState) history.replaceState(null, "", "#/" + name);
    if (name === "tasks") refreshDashboard();
    if (name === "memory") refreshDashboard();
    if (name === "bills" || name === "learnings") refreshDashboard();
    if (name === "email") refreshServices();
  }
  buttons.forEach(function(b) { b.addEventListener("click", function() { go(b.dataset.view); }); });
  var start = (location.hash || "#/status").replace("#/", "");
  if (!views[start]) start = "status";
  go(start);
}

/* ---------- WhatsApp status ---------- */

function refreshStatus() {
  api("/api/whatsapp/status").then(function(s) {
    var hdr = $("hdrBadge");
    if (s.logged_in && s.connected) { hdr.className = "badge on"; hdr.textContent = "● online"; }
    else if (s.pairing) { hdr.className = "badge off"; hdr.textContent = "pairing"; }
    else { hdr.className = "badge off"; hdr.textContent = "offline"; }

    pill($("pillLogin"), s.logged_in ? "WhatsApp linked" : "Not linked", s.logged_in ? "ok" : "warn");
    pill($("pillConn"), s.connected ? "Connected" : (s.pairing ? "Pairing" : "Disconnected"), s.connected ? "ok" : (s.pairing ? "warn" : "err"));

    var vLabel = s.voice_ready
      ? "Voice: " + (s.voice_provider === "sarvam" ? "Sarvam" : s.voice_provider === "elevenlabs" ? "ElevenLabs" : "on")
      : (s.voice_replies ? "Voice: not configured" : "Voice: off");
    pill($("pillVoice"), vLabel, s.voice_ready ? "ok" : "warn");
    pill($("pillSTT"), s.stt_ready ? "Voice notes: on" : "Voice notes: off", s.stt_ready ? "ok" : "warn");

    $("waStatus").textContent = s.last_error ? ("Last error: " + s.last_error) : (s.pairing ? "Pairing in progress - scan the QR with WhatsApp." : "");

    if (s.pairing && !s.logged_in) {
      var qrBox = $("qrBox");
      // Keep a displayed QR stable while the status endpoint polls. During
      // startup the QR endpoint can briefly return 404; show a useful state
      // and retry instead of leaving a blank box or flickering every 4s.
      if (!qrBox.querySelector("img")) {
        qrBox.innerHTML = '<p class="empty qr-wait">Waiting for WhatsApp QR…<br><small>This usually appears within a few seconds.</small></p>';
        var qrImg = new Image();
        qrImg.alt = "WhatsApp pairing QR";
        qrImg.onload = function() {
          qrBox.replaceChildren(qrImg);
        };
        qrImg.onerror = function() {
          qrImg.remove();
          setTimeout(refreshStatus, 5000);
        };
        qrImg.src = "/api/whatsapp/qr.png?t=" + Date.now();
      }
    } else if (!s.logged_in) {
      $("qrBox").innerHTML = '<p class="empty">Not paired - WhatsApp will request a scan when the app starts.</p>';
    } else {
      $("qrBox").innerHTML = '<p class="empty">Session stored - no QR needed.</p>';
    }

    var chips = "";
    if (s.voice_provider === "sarvam") chips += chip("Sarvam voice", true);
    else if (s.voice_provider === "elevenlabs") chips += chip("ElevenLabs voice", true);
    chips += chip("Voice notes (Sarvam STT)", s.stt_ready);
    chips += chip("Gmail", s.gmail);
    $("capChips").innerHTML = chips;
    $("connectionActions").innerHTML = s.logged_in
      ? (s.connected ? '<button class="link-btn" id="waDisconnect" type="button">Disconnect WhatsApp</button>' : '<button class="btn" id="waReconnect" type="button">Reconnect WhatsApp</button>')
      : '<button class="btn" id="waReconnect" type="button">Connect WhatsApp</button>';
    var reconnectBtn = $("waReconnect");
    if (reconnectBtn) reconnectBtn.addEventListener("click", function(){ reconnectBtn.disabled=true; reconnectBtn.textContent="Connecting…"; api("/api/whatsapp/reconnect", {method:"POST"}).then(function(){ toast("WhatsApp connection started"); setTimeout(refreshStatus, 800); }).catch(function(e){ reconnectBtn.disabled=false; reconnectBtn.textContent="Reconnect WhatsApp"; toast(e.message); }); });
    var waBtn = $("waDisconnect");
    if (waBtn) waBtn.addEventListener("click", function() {
      confirmAction("Disconnect WhatsApp from Sheetal’s assistant? You can pair it again later.", function(){ api("/api/whatsapp/disconnect", {method:"POST"}).then(function(){ toast("WhatsApp disconnected"); refreshStatus(); }).catch(function(e){ toast(e.message); }); });
    });
  }).catch(function(e) {
    pill($("pillLogin"), "Offline", "err");
    pill($("pillConn"), "Unreachable", "err");
    $("waStatus").textContent = e.message;
  });
}

function chip(label, on) {
  return '<span class="chip' + (on ? " ok" : "") + '"><b></b>' + esc(label) + (on ? " on" : " off") + "</span>";
}
function toast(message) { var el=$("toast"); if(!el) return; el.textContent=message; el.classList.add("show"); clearTimeout(window.__toastTimer); window.__toastTimer=setTimeout(function(){el.classList.remove("show");},3200); }
function confirmAction(message, onOk, label) { var o=$("confirmOverlay"); $("confirmText").textContent=message; $("confirmOk").textContent=label || "Disconnect"; o.hidden=false; var close=function(){o.hidden=true;}; $("confirmCancel").onclick=close; $("confirmOk").onclick=function(){close();onOk();}; }

/* ---------- Chat ---------- */

var chatSessions = [];
var activeChatId = "";
var CHAT_STORE = "sdchat_sessions_v1";
function chatId() { return "chat-" + Date.now() + "-" + Math.random().toString(36).slice(2, 7); }
function chatTitle(session) { if (session.title) return session.title; var first = (session.messages || []).find(function(m) { return m.role === "user" && m.text; }); return first ? first.text.replace(/\s+/g, " ").slice(0, 38) : "New task"; }
function chatPreview(session) { var last = (session.messages || []).slice(-1)[0]; return last ? last.text.replace(/\s+/g, " ").slice(0, 58) : "No messages yet"; }
function persistSessions() { try { localStorage.setItem(CHAT_STORE, JSON.stringify(chatSessions.slice(-50))); } catch (_) {} }
function formatChatDate(value) { var d = new Date(value || Date.now()); if (isNaN(d.getTime())) return ""; return d.toLocaleDateString([], {month:"short", day:"numeric"}) + " · " + d.toLocaleTimeString([], {hour:"2-digit", minute:"2-digit"}); }
function renderChatHistory() {
  var list = $("historyList"); if (!list) return;
  $("historyCount").textContent = chatSessions.length ? String(chatSessions.length) : "";
  list.innerHTML = chatSessions.slice().reverse().map(function(s) { return '<div class="history-row"><button class="history-item' + (s.id === activeChatId ? ' active' : '') + '" type="button" data-chat-id="' + esc(s.id) + '"><strong>' + esc(chatTitle(s)) + '</strong><small>' + esc(chatPreview(s)) + ' · ' + esc(formatChatDate(s.updatedAt || s.createdAt)) + '</small></button><div class="history-menu"><button class="history-menu-trigger" type="button" data-menu-toggle aria-label="Chat actions">⋯</button><div class="history-menu-list" role="menu"><button type="button" data-chat-action="rename" data-chat-id="' + esc(s.id) + '" role="menuitem">Rename</button><button type="button" data-chat-action="delete" data-chat-id="' + esc(s.id) + '" role="menuitem">Delete</button></div></div></div>'; }).join("") || '<div class="history-empty">Your conversations will appear here.</div>';
}
function activeChat() { return chatSessions.find(function(s) { return s.id === activeChatId; }); }
function createChat(withWelcome) {
  var now = new Date().toISOString();
  var session = {id:chatId(), createdAt:now, updatedAt:now, messages:[]};
  chatSessions.push(session); activeChatId = session.id; $("thread").replaceChildren();
  if (withWelcome !== false) appendMsg("bot", "Good to see you, Sheetal. What would you like Agent V to take care of?"); else { persistSessions(); renderChatHistory(); }
  return session;
}
function switchChat(id) {
  var session = chatSessions.find(function(s) { return s.id === id; }); if (!session) return;
  activeChatId = id; $("thread").replaceChildren();
  (session.messages || []).filter(function(row) { return !(row.role === "bot" && /I'm not fully sure what you want yet/i.test(row.text || "")); }).forEach(function(row) { appendMsg(row.role === "user" ? "user" : "bot", row.text || "", {persist:false, time:row.time}); });
  renderChatHistory();
}

function appendMsg(role, text, options) {
  options = options || {};
  var thread = $("thread");
  var el = document.createElement("div");
  el.className = "msg " + role;
  el.textContent = text;
  var t = document.createElement("span");
  t.className = "t";
  t.textContent = options.time || nowTime();
  el.appendChild(t);
  var actions = document.createElement("div"); actions.className = "msg-actions";
  var copy = document.createElement("button"); copy.type="button"; copy.className="icon-btn"; copy.textContent="⧉"; copy.title="Copy"; copy.onclick=function(){ navigator.clipboard.writeText(text).then(function(){toast("Copied!");}); }; actions.appendChild(copy);
  if (role === "bot") { var retry=document.createElement("button"); retry.type="button"; retry.className="icon-btn"; retry.textContent="↻"; retry.title="Retry"; retry.setAttribute("aria-label","Retry response"); retry.onclick=function(){ var session=activeChat(); var rows=(session && session.messages) || []; var last=rows.filter(function(x){return x.role==="user";}).pop(); if(last){ $("cmdInput").value=last.text; $("cmdSend").click(); } }; actions.appendChild(retry); }
  if (role === "user") { var edit=document.createElement("button"); edit.type="button"; edit.className="icon-btn"; edit.textContent="✎"; edit.title="Edit"; edit.setAttribute("aria-label","Edit message"); edit.onclick=function(){ $("cmdInput").value=text; $("cmdInput").focus(); toast("Ready to edit"); }; actions.appendChild(edit); }
  el.appendChild(actions);
  thread.appendChild(el);
  thread.scrollTop = thread.scrollHeight;
  if (options.persist !== false) saveChat();
  return el;
}

function saveChat() {
  var session = activeChat(); if (!session) return;
  var rows = Array.from(document.querySelectorAll("#thread .msg")).map(function(el) {
    var t = el.querySelector(".t");
    return { role: el.classList.contains("user") ? "user" : "bot", text: (el.firstChild && el.firstChild.nodeValue) || el.textContent.replace(t ? t.textContent : "", "").trim(), time: t ? t.textContent : "" };
  });
  session.messages = rows.slice(-100); session.updatedAt = new Date().toISOString(); persistSessions(); renderChatHistory();
}

function loadChat() {
  try { chatSessions = JSON.parse(localStorage.getItem(CHAT_STORE) || "[]"); } catch (_) { chatSessions = []; }
  if (!Array.isArray(chatSessions)) chatSessions = [];
  chatSessions = chatSessions.filter(function(s) { return s && s.id && Array.isArray(s.messages); });
  if (!chatSessions.length) {
    var legacy = []; try { legacy = JSON.parse(localStorage.getItem("sdchat_history_v3") || "[]"); } catch (_) {}
    var now = new Date().toISOString(); chatSessions.push({id:chatId(), createdAt:now, updatedAt:now, messages:legacy});
  }
  activeChatId = chatSessions[chatSessions.length - 1].id; switchChat(activeChatId);
  if (!activeChat().messages.length) appendMsg("bot", "Good to see you, Sheetal. What would you like Agent V to take care of?");
}

function setupCommand() {
  var input = $("cmdInput"), btn = $("cmdSend");
  function ask() {
    var msg = (input.value || "").trim();
    if (!msg) return;
    appendMsg("user", msg);
    input.value = "";
    btn.disabled = true; btn.textContent = "…";
    var pending = appendMsg("bot", "…");
    api("/api/command", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ message: msg })
    }).then(function(d) {
      pending.textContent = d.reply || "Done.";
      var tm = document.createElement("span"); tm.className = "t"; tm.textContent = nowTime(); pending.appendChild(tm); saveChat();
      btn.disabled = false; btn.textContent = "SEND";
      refreshDashboard();
    }).catch(function(e) {
      pending.textContent = "Error: " + e.message;
      btn.disabled = false; btn.textContent = "SEND";
    });
  }
  btn.addEventListener("click", ask);
  input.addEventListener("keydown", function(e) { if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); ask(); } });
}

function setupNewChat() {
  $("newChatBtn").addEventListener("click", function() {
    createChat(true);
    $("cmdInput").focus();
  });
  $("historyList").addEventListener("click", function(e) {
    var toggle = e.target.closest("[data-menu-toggle]");
    if (toggle) {
      var menu = toggle.closest(".history-menu");
      document.querySelectorAll(".history-menu.open").forEach(function(m) {
        if (m !== menu) { m.classList.remove("open"); m.querySelector(".history-menu-list").style.cssText = ""; }
      });
      var opening = !menu.classList.contains("open");
      menu.classList.toggle("open", opening);
      var list = menu.querySelector(".history-menu-list");
      if (opening) {
        var rect = toggle.getBoundingClientRect();
        var width = 110;
        var left = Math.max(8, Math.min(window.innerWidth - width - 8, rect.right - width));
        var top = rect.bottom + 4;
        if (top + 70 > window.innerHeight) top = Math.max(8, rect.top - 74);
        list.style.left = left + "px";
        list.style.top = top + "px";
      } else list.style.cssText = "";
      return;
    }
    var action = e.target.closest("[data-chat-action]");
    if (action) {
      var session = chatSessions.find(function(s) { return s.id === action.dataset.chatId; });
      if (!session) return;
      if (action.dataset.chatAction === "rename") {
        var name = window.prompt("Rename chat", chatTitle(session));
        if (name && name.trim()) { session.title = name.trim().slice(0, 60); session.updatedAt = new Date().toISOString(); persistSessions(); renderChatHistory(); }
      } else if (action.dataset.chatAction === "delete") {
        confirmAction("Delete this chat? This cannot be undone.", function() {
          chatSessions = chatSessions.filter(function(s) { return s.id !== session.id; });
          if (!chatSessions.length) createChat(true); else if (activeChatId === session.id) switchChat(chatSessions[chatSessions.length - 1].id); else { persistSessions(); renderChatHistory(); }
        }, "Delete");
      }
      return;
    }
    var item = e.target.closest("[data-chat-id]");
    if (item) switchChat(item.dataset.chatId);
  });
  document.addEventListener("click", function(e) { if (!e.target.closest(".history-menu")) document.querySelectorAll(".history-menu.open").forEach(function(m) { m.classList.remove("open"); m.querySelector(".history-menu-list").style.cssText = ""; }); });
  $("historySearch").addEventListener("input", function() {
    var query = this.value.trim().toLowerCase();
    document.querySelectorAll("#historyList [data-chat-id]").forEach(function(item) {
      item.hidden = query && !item.textContent.toLowerCase().includes(query);
    });
  });
}

/* ---------- Dashboard (tasks / plan / memory) ---------- */

function refreshDashboard() {
  api("/api/dashboard").then(function(d) {
    var tasks = (d.tasks || []).slice(0, 10);
    $("taskList").innerHTML = tasks.length
      ? tasks.map(function(t) { return "<li><span>" + esc(t.title) + (t.priority === "high" ? " (high)" : "") + "</span><em>" + esc(t.due || t.priority) + "</em></li>"; }).join("")
      : '<li class="empty">No open tasks.</li>';

    var plan = (d.plans || [])[0];
    $("planList").innerHTML = (plan && plan.items && plan.items.length)
      ? plan.items.map(function(x) { return "<li><span>" + esc(x) + "</span></li>"; }).join("")
      : '<li class="empty">No plan for today.</li>';

    var m = d.memory || {};
    $("prefList").innerHTML = (m.preferences || []).length
      ? m.preferences.map(function(p) { return "<li><span>" + esc(p) + "</span></li>"; }).join("")
      : '<li class="empty">Nothing yet.</li>';
    $("tasteList").innerHTML = (m.taste_notes || []).length
      ? m.taste_notes.map(function(t) { return "<li><span>" + esc(t) + "</span></li>"; }).join("")
      : '<li class="empty">Nothing yet.</li>';

    var bills = (d.bills || []).slice(0, 20);
    $("billList").innerHTML = bills.length
      ? bills.map(function(b) {
          var amount = b.amount ? "₹" + Number(b.amount).toLocaleString("en-IN") : "Amount not set";
          var due = b.due_date ? "due " + b.due_date : "due date not set";
          var state = b.status === "paid" ? "paid" : due;
          return "<li><span><strong>" + esc(b.name) + "</strong><br><small>" + esc(b.kind || "recurring") + (b.frequency ? " · " + esc(b.frequency) : "") + "</small></span><em>" + esc(amount) + " · " + esc(state) + "</em></li>";
        }).join("")
      : '<li class="empty">No bills or EMIs yet. Add one above or message Agent V on WhatsApp.</li>';

    var learnings = (d.learnings || []).slice(0, 20);
    $("learningList").innerHTML = learnings.length
      ? learnings.map(function(l) {
          var date = l.created_at ? new Date(l.created_at).toLocaleDateString([], {month:"short", day:"numeric"}) : "";
          return "<li><span><strong>" + esc(l.summary) + "</strong>" + (l.context ? "<br><small>" + esc(l.context) + "</small>" : "") + "</span><em>" + esc(l.status || "active") + (date ? " · " + esc(date) : "") + "</em></li>";
        }).join("")
      : '<li class="empty">Nothing recorded yet. Correct Agent V naturally when it gets something wrong.</li>';
  }).catch(function() {});
}

/* ---------- Music ---------- */

function renderQueue(q) {
  if (!q.length) { $("musicQueue").innerHTML = '<p class="empty">Queue is empty. Search and add tracks above.</p>'; return; }
  $("musicQueue").innerHTML = q.map(function(item) {
    var art = item.art ? '<img src="' + esc(item.art) + '" alt="">' : '<img alt="">';
    var dim = item.status === "done" ? ' style="opacity:.45"' : "";
    return '<div class="media"' + dim + '>' + art +
      '<div><div class="m-name">' + esc(item.name || item.uri) + '</div>' +
      '<div class="m-art">' + esc(item.artist || "") + (item.album ? " · " + esc(item.album) : "") + ' <em>' + fmtDur(item.dur_ms) + '</em></div></div>' +
      (item.status !== "done" ? '<div class="m-acts"><button class="icon-btn" data-act="remove" data-id="' + esc(item.id) + '" title="Remove">×</button></div>' : "") +
      '</div>';
  }).join("");
}

function refreshQueue() {
  api("/api/queue").then(function(d) { renderQueue(d.queue || []); }).catch(function() {});
}

function setupMusic() {
  var input = $("musicSearch"), btn = $("musicSearchBtn"), results = $("musicResults");
  function search() {
    var q = (input.value || "").trim();
    if (!q) return;
    results.innerHTML = '<p class="empty"><span class="spin"></span> Searching…</p>';
    api("/api/search?q=" + encodeURIComponent(q)).then(function(d) {
      var tracks = (d.results || []).slice(0, 6);
      if (!tracks.length) { results.innerHTML = '<p class="empty">No results for "' + esc(q) + '".</p>'; return; }
      results.innerHTML = tracks.map(function(t) {
        var art = t.art ? '<img src="' + esc(t.art) + '" alt="">' : '<img alt="">';
        return '<div class="media">' + art +
          '<div><div class="m-name">' + esc(t.name) + '</div><div class="m-art">' + esc(t.artist) + ' <em>' + fmtDur(t.dur_ms) + '</em></div></div>' +
          '<div class="m-acts"><button class="icon-btn" data-act="add" data-query="' + esc(t.name + " " + t.artist) + '" title="Add to queue">+</button></div></div>';
      }).join("");
    }).catch(function(e) { results.innerHTML = '<p class="empty">Search failed: ' + esc(e.message) + '</p>'; });
  }
  $("queueNextBtn").addEventListener("click", function() {
    api("/api/queue/next", { method: "POST" }).then(function(d) {
      if (d.item) $("waStatus").textContent = "Claimed: " + d.item.name + " - " + d.item.artist;
      refreshQueue();
    });
  });
  $("queueClearBtn").addEventListener("click", function() {
    if (!confirm("Clear the old Spotify queue?")) return;
    api("/api/queue/clear", { method: "POST" }).then(refreshQueue);
  });
  results.addEventListener("click", function(e) {
    var t = e.target.closest("[data-act]");
    if (!t || t.dataset.act !== "add") return;
    api("/api/queue", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ query: t.dataset.query }) })
      .then(function(d) { refreshQueue(); if (d.reply) $("waStatus").textContent = d.reply; });
  });
  $("musicQueue").addEventListener("click", function(e) {
    var t = e.target.closest("[data-act]");
    if (!t || t.dataset.act !== "remove") return;
    api("/api/queue/remove", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ id: t.dataset.id }) }).then(refreshQueue);
  });
  btn.addEventListener("click", search);
  input.addEventListener("keydown", function(e) { if (e.key === "Enter") search(); });
}

/* ---------- Tasks quick-add ---------- */

function setupTasks() {
  var input = $("taskInput"), btn = $("taskAdd");
  function add() {
    var msg = (input.value || "").trim();
    if (!msg) return;
    var text = /^(remind|add|create|task|make|set)/i.test(msg) ? msg : "remind me to " + msg;
    btn.disabled = true; btn.textContent = "…";
    api("/api/command", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ message: text }) })
      .then(function(d) { btn.disabled = false; btn.textContent = "ADD"; input.value = ""; refreshDashboard(); if (d.reply) toast(d.reply); })
      .catch(function(e) { btn.disabled = false; btn.textContent = "ADD"; toast("Error: " + e.message); });
  }
  btn.addEventListener("click", add);
  input.addEventListener("keydown", function(e) { if (e.key === "Enter") add(); });
}

function setupBills() {
  var input = $("billInput"), btn = $("billAdd");
  function add() {
    var msg = (input.value || "").trim();
    if (!msg) return;
    btn.disabled = true; btn.textContent = "…";
    var text = /^(add|create|track|bill|emi|payment)/i.test(msg) ? msg : "add bill " + msg;
    api("/api/command", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ message: text }) })
      .then(function(d) { input.value = ""; if (d.reply) toast(d.reply); refreshDashboard(); })
      .catch(function(e) { toast("Error: " + e.message); })
      .finally(function() { btn.disabled = false; btn.textContent = "ADD"; });
  }
  btn.addEventListener("click", add);
  input.addEventListener("keydown", function(e) { if (e.key === "Enter") add(); });
}

/* ---------- Email / services ---------- */

function refreshServices() {
  api("/api/email/status").then(function(e) {
    var lines = [];
	    if (e.setup_required) lines.push("Gmail is not configured yet. Add Google OAuth client credentials in Coolify.");
    else if (e.connected) lines.push("Gmail: connected");
    else if (e.configured) lines.push("Gmail: configured but not connected.");
    else lines.push("Gmail: not configured.");
    $("svcStatus").textContent = lines.join(" ");
    $("emailAccount").textContent = e.connected && e.account_email ? "Connected account: " + e.account_email : "";
    if (e.configured && !e.connected && !e.setup_required) {
      $("emailLink").innerHTML = '<a class="link-btn" href="/api/email/connect" target="_blank" rel="noopener">Connect Gmail <span aria-hidden="true">↗</span></a>';
    } else if (e.connected) {
      $("emailLink").innerHTML = '<button class="link-btn" id="emailDisconnect" type="button">Disconnect Gmail</button>';
      $("emailDisconnect").addEventListener("click", function() {
        confirmAction("Disconnect this Gmail account from Sheetal’s assistant?", function(){ api("/api/email/disconnect", {method:"POST"}).then(function(){ toast("Gmail disconnected"); refreshServices(); refreshStatus(); }).catch(function(err){ toast(err.message); }); });
      });
    } else {
      $("emailLink").innerHTML = "";
    }
    if (e.connected) {
      api("/api/email/inbox?limit=3").then(function(data) {
        var msgs = (data.messages || []).slice(0, 3);
        $("emailList").innerHTML = msgs.length
          ? msgs.map(function(m) { return '<div class="email-item"><strong>' + esc(m.subject) + '</strong><span>' + esc(m.from) + (m.unread ? " (unread)" : "") + '</span><small>' + esc(m.snippet || "") + '</small></div>'; }).join("")
          : '<p class="empty">No recent messages.</p>';
      }).catch(function() {});
    } else {
      $("emailList").innerHTML = "";
    }
  }).catch(function() { $("svcStatus").textContent = "Services status unavailable."; });
}

setupNav();
refreshStatus();
refreshDashboard();
refreshServices();
setupCommand();
loadChat();
setupNewChat();
setupTasks();
setupBills();
setInterval(refreshStatus, 12000);
setInterval(refreshDashboard, 15000);
