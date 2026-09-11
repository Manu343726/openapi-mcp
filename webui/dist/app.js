/* Phase 5 web UI shell: a manifest-driven control surface over the /ui bridge.
   Each tab gets its own opaque session token (sessionStorage), so the server
   keeps per-session overlays, targets and exposure isolated. */
(() => {
  const SESSION_KEY = "openapi-mcp.ui.session";
  let session = sessionStorage.getItem(SESSION_KEY);
  if (!session) {
    session = (crypto && crypto.randomUUID)
      ? crypto.randomUUID()
      : String(Date.now()) + "-" + Math.random().toString(16).slice(2);
    sessionStorage.setItem(SESSION_KEY, session);
  }

  const $ = (id) => document.getElementById(id);

  function log(title, body) {
    const entry = document.createElement("div");
    entry.className = "entry";
    const head = document.createElement("div");
    head.className = "head";
    head.textContent = title;
    entry.appendChild(head);
    if (body instanceof Node) {
      entry.appendChild(body);
    } else {
      const pre = document.createElement("pre");
      pre.textContent = typeof body === "string" ? body : JSON.stringify(body, null, 2);
      entry.appendChild(pre);
    }
    $("log").prepend(entry);
  }

  // renderView turns a result→view payload into a table when it has rows, or a
  // simple text block otherwise.
  function renderView(view) {
    if (!view || !view.rows || view.layout !== "table") return null;
    const table = document.createElement("table");
    table.className = "result-table";
    const thead = document.createElement("thead");
    const hr = document.createElement("tr");
    (view.columns || []).forEach((c) => {
      const th = document.createElement("th");
      th.textContent = c;
      hr.appendChild(th);
    });
    thead.appendChild(hr);
    table.appendChild(thead);
    const tbody = document.createElement("tbody");
    view.rows.forEach((row) => {
      const tr = document.createElement("tr");
      (view.columns || []).forEach((c) => {
        const td = document.createElement("td");
        const v = row[c];
        td.textContent = v === undefined || v === null ? "" : String(v);
        tr.appendChild(td);
      });
      tbody.appendChild(tr);
    });
    table.appendChild(tbody);
    return table;
  }

  async function api(path, opts = {}) {
    opts.headers = Object.assign({ "X-Ui-Session": session }, opts.headers || {});
    const res = await fetch(path, opts);
    if (!res.ok) throw new Error(res.status + " " + res.statusText);
    return res.json();
  }

  function card(name, meta) {
    const el = document.createElement("div");
    el.className = "card";
    const n = document.createElement("div");
    n.className = "name";
    n.textContent = name;
    const m = document.createElement("div");
    m.className = "meta";
    m.textContent = meta;
    el.appendChild(n);
    el.appendChild(m);
    return el;
  }

  function renderManifest(m) {
    const tools = m.tools || [];
    $("toolcount").textContent = tools.length + " tools";
    $("status").textContent = "connected";

    const apis = $("apis");
    apis.innerHTML = "";
    (m.apis || []).forEach((a) => {
      apis.appendChild(card(a.name, `${a.exposed}/${a.tools} tools · mode ${a.mode || "all"} · ${a.active || "no target"}`));
    });

    const scripts = $("scripts");
    scripts.innerHTML = "";
    (m.scripts || []).forEach((s) => {
      scripts.appendChild(card(s.name, `${s.summary || ""}${s.exposed ? "" : " (hidden)"}`));
    });

    const sel = $("tool");
    sel.innerHTML = "";
    tools.forEach((name) => {
      const o = document.createElement("option");
      o.value = name;
      o.textContent = name;
      sel.appendChild(o);
    });
  }

  async function refresh() {
    try {
      renderManifest(await api("/ui/manifest"));
    } catch (e) {
      $("status").textContent = "error";
      log("manifest error", String(e));
    }
  }

  async function chat(payload) {
    try {
      const r = await api("/ui/chat", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
      });
      log(payload.tool ? "tool: " + payload.tool : "chat", renderView(r.view) || r.text || r);
    } catch (e) {
      log("error", String(e));
    }
  }

  $("composer").addEventListener("submit", (e) => {
    e.preventDefault();
    const tool = $("tool").value;
    if (!tool) return;
    let args = {};
    const raw = $("args").value.trim();
    if (raw) {
      try { args = JSON.parse(raw); } catch { log("error", "arguments must be JSON"); return; }
    }
    chat({ tool, arguments: args });
  });

  $("send").addEventListener("click", () => {
    const message = $("message").value.trim();
    if (!message) return;
    $("message").value = "";
    chat({ message });
  });

  function connectEvents() {
    const es = new EventSource("/ui/events?session=" + encodeURIComponent(session));
    es.onmessage = (ev) => {
      let msg;
      try { msg = JSON.parse(ev.data); } catch { log("event", ev.data); return; }
      if (msg.method === "notifications/tools/list_changed") {
        $("toolcount").textContent = "tools changed…";
        refresh();
      } else if (msg.method === "notifications/view") {
        const p = msg.params || {};
        log("view: " + (p.tool || ""), renderView(p) || p.text || p);
      }
    };
    es.onerror = () => { $("status").textContent = "reconnecting…"; };
  }

  refresh();
  connectEvents();
})();
