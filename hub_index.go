package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// hub_index serves the public hub landing page: architecture docs, product
// catalog cards, and a per-product config builder modal (YAML + Tugconf)
// with scale / link / multi-instance controls.

func (h *hubServer) handleCatalogAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"registry": h.cfg.Public,
		"engine":   defaultK3sEngineImage,
		"products": hubBuilderCatalog(),
		"architectures": hubArchitecturesPublic(),
		"links": map[string]string{
			"docs":          "https://github.com/TunnelTug/tunneltug/blob/main/docs/ARCHITECTURES.md",
			"site_example":  "https://github.com/TunnelTug/tunneltug/blob/main/config/site.example.yaml",
			"tug_example":   "https://github.com/TunnelTug/tunneltug/blob/main/config/site.example.tug",
		},
	})
}

// hubBuilderProduct is one catalog row for the hub UI builder.
type hubBuilderProduct struct {
	Name        string `json:"name"`
	Display     string `json:"display"`
	Repo        string `json:"repo"`
	Desc        string `json:"desc"`
	Port        int    `json:"port"`
	Kind        string `json:"kind"` // app | platform | kernel | edge | engine
	ZeroConfig  string `json:"zero_config_port"`
	DefaultLink []string `json:"default_links,omitempty"`
}

func hubBuilderCatalog() []hubBuilderProduct {
	// Ports align with stackCatalog zero-config defaults.
	ports := map[string]int{
		"williwaw": 3081, "motionkb": 8090, "ack": 3083, "mail": 3086, "search": 3087,
		"social": 3085, "platform": 8443, "services": 8080, "name": 8447, "dbsc_relay": 8450,
		"anycast": 9099, "orchid_ingest": 8451, "auth": 8460, "iam": 8461, "access": 8462,
		"scim": 8463, "pki": 8464, "workflows": 8465, "topology": 8466, "nameservice": 8467,
		"servicekeys": 8468, "vpi": 8469, "logs": 8470, "ultimate_db": 8480, "ultimate_keystore": 8481,
		"tunneltug": 0,
	}
	kinds := map[string]string{
		"ultimate_db": "kernel", "ultimate_keystore": "kernel",
		"anycast": "edge", "dbsc_relay": "edge", "tunneltug": "engine",
		"platform": "platform", "services": "platform", "name": "platform",
		"auth": "platform", "iam": "platform", "access": "platform", "scim": "platform",
		"pki": "platform", "workflows": "platform", "topology": "platform",
		"nameservice": "platform", "servicekeys": "platform", "vpi": "platform",
		"logs": "platform", "orchid_ingest": "platform",
	}
	defaults := map[string][]string{
		"williwaw": {"social", "ack", "ultimate_db", "ultimate_keystore"},
		"motionkb": {"social", "williwaw", "ultimate_db"},
		"ack":      {"social", "ultimate_db"},
		"mail":     {"ultimate_db", "ultimate_keystore"},
		"search":   {"ultimate_db"},
		"social":   {"ultimate_db"},
	}
	var out []hubBuilderProduct
	for _, row := range HubProductCatalogPublic() {
		name := row["name"]
		p := ports[name]
		k := kinds[name]
		if k == "" {
			k = "app"
		}
		item := hubBuilderProduct{
			Name: name, Display: row["display"], Repo: row["repo"], Desc: row["desc"],
			Port: p, Kind: k, DefaultLink: defaults[name],
		}
		if p > 0 {
			item.ZeroConfig = strconv.Itoa(p)
		}
		out = append(out, item)
	}
	return out
}

func hubArchitecturesPublic() []map[string]string {
	return []map[string]string{
		{"id": "A1", "name": "Dev tunnel", "summary": "Single server + client exposing localhost with self-signed TLS."},
		{"id": "A2", "name": "Production direct apex", "summary": "One shared tunnel on the apex domain (-routing direct -prod)."},
		{"id": "A3", "name": "Subdomain multi-tenant", "summary": "Wildcard edge; many clients, one server."},
		{"id": "A4", "name": "LB + barge fleet", "summary": "Horizontal tunnel HA with dynamic registration (k3s default)."},
		{"id": "A5", "name": "Product stack", "summary": "Single-region k3s Deployments for hub product images."},
		{"id": "A6", "name": "Multi-PoP + kernel mesh", "summary": "Global ingresses; local embeds; real-time kernel replication peers."},
		{"id": "A7", "name": "Anycast edge", "summary": "BGP health-gated split-horizon DNS face with withdraw on failure."},
		{"id": "A8", "name": "Vhosts + tunnels", "summary": "Product apex next to user tunnel subdomains."},
		{"id": "A9", "name": "Mesh-only private", "summary": "Private TLD overlay without public barge haul."},
		{"id": "A10", "name": "Hub image pipeline", "summary": "OCI hub public pull / authed push into fleets."},
		{"id": "R1", "name": "Resilient: HA tunnel edge", "summary": "LB + ≥2 barges, heartbeats, optional snapshots."},
		{"id": "R2", "name": "Resilient: global anycast", "summary": "A6+A7 multi-PoP stack with health-gated anycast."},
		{"id": "R3", "name": "Resilient: vhost failover", "summary": "Same public hostname; cut over upstream to standby."},
		{"id": "R4", "name": "Resilient: kernel replication", "summary": "Local primary + AddPeer mesh; never prefer-remote-only."},
		{"id": "R5", "name": "Resilient: image integrity", "summary": "SDF fleet+digest attestation; public pull, authed push."},
	}
}

func (h *hubServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	catalogJSON, _ := json.Marshal(hubBuilderCatalog())
	archJSON, _ := json.Marshal(hubArchitecturesPublic())
	fmt.Fprint(w, hubIndexHTML(h.cfg.Public, defaultK3sEngineImage, h.cfg.S3URL, h.cfg.Bucket, string(catalogJSON), string(archJSON)))
}

func hubIndexHTML(public, engine, s3, bucket, catalogJSON, archJSON string) string {
	// Escape for embedding in <script type="application/json">
	catalogJSON = strings.ReplaceAll(catalogJSON, "</", "<\\/")
	archJSON = strings.ReplaceAll(archJSON, "</", "<\\/")
	public = htmlEsc(public)
	engine = htmlEsc(engine)
	s3 = htmlEsc(s3)
	bucket = htmlEsc(bucket)

	return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>hub.tunneltug.com — TunnelTug registry &amp; config builder</title>
<style>
:root{--bg:#0a0e17;--panel:#111827;--panel2:#0f172a;--border:#1f2937;--text:#f3f4f6;--muted:#9ca3af;--accent:#22d3ee;--accent2:#f97316;--ok:#34d399;--danger:#f87171}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--text);font-family:ui-sans-serif,system-ui,sans-serif;line-height:1.55}
a{color:var(--accent);text-decoration:none}a:hover{text-decoration:underline}
.wrap{max-width:1100px;margin:0 auto;padding:40px 20px 80px}
h1{font-size:1.85rem;margin:0 0 8px;letter-spacing:-.02em}
h2{font-size:1.1rem;margin:0 0 12px;color:var(--accent);text-transform:uppercase;letter-spacing:.05em}
.lead{color:var(--muted);margin:0 0 24px;max-width:62ch}
.badge{display:inline-block;padding:2px 10px;border-radius:999px;font-size:12px;font-weight:600;background:rgba(34,211,238,.15);color:var(--accent);margin:0 6px 16px 0}
.card{background:var(--panel);border:1px solid var(--border);border-radius:12px;padding:18px 20px;margin:0 0 16px}
.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(240px,1fr));gap:12px}
.arch{background:var(--panel2);border:1px solid var(--border);border-radius:10px;padding:14px 14px 12px;cursor:default}
.arch .id{font-size:11px;font-weight:700;color:var(--accent2);letter-spacing:.06em}
.arch h3{margin:4px 0 6px;font-size:14px}
.arch p{margin:0;font-size:12px;color:var(--muted)}
.prod{background:var(--panel2);border:1px solid var(--border);border-radius:10px;padding:14px;display:flex;flex-direction:column;gap:8px;min-height:150px}
.prod h3{margin:0;font-size:15px}
.prod .repo{font-size:11px;color:var(--muted);font-family:ui-monospace,monospace;word-break:break-all}
.prod p{margin:0;font-size:12px;color:var(--muted);flex:1}
.prod .meta{font-size:11px;color:var(--accent)}
.btn{appearance:none;border:1px solid var(--border);background:#1e293b;color:var(--text);border-radius:8px;padding:8px 12px;font-size:13px;font-weight:600;cursor:pointer}
.btn:hover{border-color:var(--accent);color:var(--accent)}
.btn.primary{background:rgba(34,211,238,.15);border-color:rgba(34,211,238,.45);color:var(--accent)}
.btn.primary:hover{background:rgba(34,211,238,.25)}
.btn.block{width:100%}
.kind{font-size:10px;text-transform:uppercase;letter-spacing:.06em;color:var(--muted)}
.foot{margin-top:36px;padding-top:18px;border-top:1px solid var(--border);color:var(--muted);font-size:13px}
/* modal */
.overlay{position:fixed;inset:0;background:rgba(0,0,0,.65);display:none;align-items:center;justify-content:center;padding:16px;z-index:50}
.overlay.open{display:flex}
.modal{background:var(--panel);border:1px solid var(--border);border-radius:14px;width:min(920px,100%);max-height:92vh;display:flex;flex-direction:column;box-shadow:0 24px 80px rgba(0,0,0,.5)}
.modal header{padding:16px 18px;border-bottom:1px solid var(--border);display:flex;align-items:flex-start;justify-content:space-between;gap:12px}
.modal header h2{margin:0;font-size:1.05rem;text-transform:none;letter-spacing:0;color:var(--text)}
.modal header p{margin:4px 0 0;font-size:12px;color:var(--muted)}
.modal .body{padding:14px 18px;overflow:auto;flex:1}
.modal footer{padding:12px 18px;border-top:1px solid var(--border);display:flex;flex-wrap:wrap;gap:8px;align-items:center;justify-content:space-between}
.row{display:flex;flex-wrap:wrap;gap:12px;margin-bottom:12px}
.field{flex:1;min-width:140px}
.field label{display:block;font-size:11px;color:var(--muted);margin-bottom:4px;text-transform:uppercase;letter-spacing:.04em}
.field input,.field select{width:100%;background:#0b1220;border:1px solid var(--border);border-radius:8px;color:var(--text);padding:8px 10px;font-size:13px}
.field input[type=number]{max-width:120px}
.tabs{display:flex;gap:6px;margin:10px 0 8px}
.tab{padding:6px 12px;border-radius:999px;border:1px solid var(--border);background:transparent;color:var(--muted);font-size:12px;font-weight:600;cursor:pointer}
.tab.active{background:rgba(34,211,238,.12);border-color:rgba(34,211,238,.4);color:var(--accent)}
pre.cfg{margin:0;background:#0b1220;border:1px solid var(--border);border-radius:10px;padding:12px;font-size:11.5px;line-height:1.45;overflow:auto;max-height:280px;white-space:pre-wrap;word-break:break-word;font-family:ui-monospace,SFMono-Regular,Menlo,monospace}
.links{display:grid;grid-template-columns:repeat(auto-fill,minmax(160px,1fr));gap:6px;max-height:140px;overflow:auto;padding:8px;background:#0b1220;border:1px solid var(--border);border-radius:8px}
.links label{display:flex;align-items:center;gap:6px;font-size:12px;color:var(--muted);cursor:pointer}
.links input{accent-color:var(--accent)}
.hint{font-size:12px;color:var(--muted);margin:0 0 10px}
.x{background:transparent;border:0;color:var(--muted);font-size:22px;line-height:1;cursor:pointer;padding:0 4px}
.x:hover{color:var(--text)}
.actions{display:flex;flex-wrap:wrap;gap:8px}
.toast{position:fixed;bottom:20px;left:50%;transform:translateX(-50%);background:#134e4a;color:#ccfbf1;padding:8px 14px;border-radius:8px;font-size:13px;display:none;z-index:60}
.toast.show{display:block}
ul.compact{margin:0;padding-left:1.15rem;color:var(--muted);font-size:13px}
ul.compact li{margin:4px 0}
code{font-size:12px;color:#e6edf3}
</style>
</head>
<body>
<div class="wrap">
  <div class="badge">MIT License</div>
  <div class="badge">public pull · authenticated push</div>
  <h1>TunnelTug image hub</h1>
  <p class="lead">OCI registry for fleets and product images. Browse <strong>architectures</strong> and open any product’s <strong>config builder</strong> for site YAML + Junos-like Tugconf — scale replicas, link services, or run multiple instances of the same service.</p>

  <div class="card">
    <h2>Registry</h2>
    <ul class="compact">
      <li>Host: <code>` + public + `</code></li>
      <li>Engine: <code>` + engine + `</code></li>
      <li>Storage: <code>` + s3 + `/s3/` + bucket + `</code></li>
      <li>API <a href="/v2/">/v2/</a> · Health <a href="/_tunneltug/hub/health">/_tunneltug/hub/health</a> · Catalog <a href="/_tunneltug/hub/catalog">/_tunneltug/hub/catalog</a></li>
    </ul>
  </div>

  <div class="card">
    <h2>Architectures</h2>
    <p class="hint">Full write-up: <a href="https://github.com/TunnelTug/tunneltug/blob/main/docs/ARCHITECTURES.md" target="_blank" rel="noopener">docs/ARCHITECTURES.md</a>. Resilient designs (R1–R5) combine HA barges, anycast, multi-PoP kernel mesh, and image integrity.</p>
    <div class="grid" id="arch-grid"></div>
  </div>

  <div class="card">
    <h2>Resilient designs (quick)</h2>
    <ul class="compact">
      <li><strong>R1 HA tunnel edge</strong> — LB + ≥2 k3s barges, heartbeats, optional snapshots</li>
      <li><strong>R2 Global anycast</strong> — multi-PoP stack + health-gated BGP withdraw</li>
      <li><strong>R3 Vhost failover</strong> — same public hostname, cut over upstream</li>
      <li><strong>R4 Kernel replication</strong> — local embeds primary; AddPeer for real-time sync (never prefer-remote-only)</li>
      <li><strong>R5 Image integrity</strong> — SDF fleet+digest; public pull, authed push</li>
    </ul>
  </div>

  <div class="card">
    <h2>Catalog — configure each image</h2>
    <p class="hint">Click <strong>Configure</strong> for YAML + Tugconf. Scale, link other services, or add multiple instances of the same product.</p>
    <div class="grid" id="prod-grid"></div>
  </div>

  <p class="foot">SPDX-License-Identifier: MIT · Copyright 2026 TunnelTug Contributors · <a href="https://github.com/TunnelTug/tunneltug">source</a> · <a href="https://github.com/TunnelTug/tunneltug/blob/main/LICENSE">LICENSE</a></p>
</div>

<div class="overlay" id="overlay" role="dialog" aria-modal="true" aria-labelledby="modal-title">
  <div class="modal">
    <header>
      <div>
        <h2 id="modal-title">Config builder</h2>
        <p id="modal-sub">Generate stack / site YAML and Tugconf</p>
      </div>
      <button type="button" class="x" id="modal-close" aria-label="Close">&times;</button>
    </header>
    <div class="body">
      <div class="row">
        <div class="field">
          <label>Architecture</label>
          <select id="f-arch">
            <option value="A5">A5 Product stack (single region)</option>
            <option value="A6">A6 Multi-PoP + kernel mesh</option>
            <option value="R2">R2 Resilient global anycast</option>
            <option value="A4">A4 LB + barge fleet (+ stack)</option>
            <option value="R1">R1 Resilient HA tunnel edge</option>
          </select>
        </div>
        <div class="field">
          <label>Domain</label>
          <input id="f-domain" type="text" placeholder="example.com" value="example.com"/>
        </div>
        <div class="field">
          <label>Tag</label>
          <input id="f-tag" type="text" placeholder="latest" value="latest"/>
        </div>
      </div>
      <div class="row">
        <div class="field">
          <label>Replicas (scale)</label>
          <input id="f-replicas" type="number" min="1" max="64" value="2"/>
        </div>
        <div class="field">
          <label>Instances of this service</label>
          <input id="f-instances" type="number" min="1" max="16" value="1" title="Multiple named copies of the same product"/>
        </div>
        <div class="field">
          <label>PoP IDs (multi-PoP)</label>
          <input id="f-pops" type="text" placeholder="sfo,ams" value="sfo,ams"/>
        </div>
      </div>
      <p class="hint">Link other services into the same stack (kernel recommended for real-time replication peers — not a remote-only store).</p>
      <div class="links" id="link-box"></div>
      <div class="tabs">
        <button type="button" class="tab active" data-tab="yaml">YAML</button>
        <button type="button" class="tab" data-tab="tug">Tugconf</button>
        <button type="button" class="tab" data-tab="run">Run commands</button>
      </div>
      <pre class="cfg" id="cfg-out"></pre>
    </div>
    <footer>
      <div class="actions">
        <button type="button" class="btn primary" id="btn-scale">Scale &amp; link</button>
        <button type="button" class="btn" id="btn-copy">Copy</button>
        <button type="button" class="btn" id="btn-download">Download</button>
      </div>
      <span class="hint" style="margin:0">Applies scale, links, and multi-instance into the config above</span>
    </footer>
  </div>
</div>
<div class="toast" id="toast">Copied</div>

<script type="application/json" id="catalog-data">` + catalogJSON + `</script>
<script type="application/json" id="arch-data">` + archJSON + `</script>
<script>
(function(){
  const catalog = JSON.parse(document.getElementById('catalog-data').textContent);
  const archs = JSON.parse(document.getElementById('arch-data').textContent);
  const archGrid = document.getElementById('arch-grid');
  const prodGrid = document.getElementById('prod-grid');
  const overlay = document.getElementById('overlay');
  const cfgOut = document.getElementById('cfg-out');
  const linkBox = document.getElementById('link-box');
  const toast = document.getElementById('toast');
  let active = null;
  let tab = 'yaml';
  let lastYaml = '', lastTug = '', lastRun = '';

  archs.forEach(a => {
    const el = document.createElement('div');
    el.className = 'arch';
    el.innerHTML = '<div class="id">'+esc(a.id)+'</div><h3>'+esc(a.name)+'</h3><p>'+esc(a.summary)+'</p>';
    archGrid.appendChild(el);
  });

  catalog.forEach(p => {
    const el = document.createElement('div');
    el.className = 'prod';
    const port = p.zero_config_port ? ('Zero Config Port: '+p.zero_config_port) : 'Fleet engine';
    el.innerHTML =
      '<div class="kind">'+esc(p.kind)+'</div>'+
      '<h3>'+esc(p.display)+'</h3>'+
      '<div class="repo">'+esc(p.repo)+'</div>'+
      '<p>'+esc(p.desc)+'</p>'+
      '<div class="meta">'+esc(port)+'</div>'+
      '<button type="button" class="btn primary block" data-name="'+esc(p.name)+'">Configure</button>';
    el.querySelector('button').addEventListener('click', () => openModal(p));
    prodGrid.appendChild(el);
  });

  function esc(s){ return String(s==null?'':s).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c])); }

  function openModal(p){
    active = p;
    document.getElementById('modal-title').textContent = p.display + ' — config builder';
    document.getElementById('modal-sub').textContent = p.repo + (p.zero_config_port ? ' · port '+p.zero_config_port : '');
    linkBox.innerHTML = '';
    catalog.forEach(o => {
      if (o.name === p.name) return;
      const id = 'lnk-'+o.name;
      const lab = document.createElement('label');
      const checked = (p.default_links||[]).indexOf(o.name) >= 0;
      lab.innerHTML = '<input type="checkbox" id="'+id+'" value="'+esc(o.name)+'"'+(checked?' checked':'')+'/> '+esc(o.display);
      linkBox.appendChild(lab);
    });
    // Prefer multi-PoP for kernel products
    if (p.kind === 'kernel') document.getElementById('f-arch').value = 'A6';
    else document.getElementById('f-arch').value = 'A5';
    regenerate();
    overlay.classList.add('open');
  }

  function selectedLinks(){
    return Array.from(linkBox.querySelectorAll('input:checked')).map(i => i.value);
  }

  function bargeNames(base, instances){
    const n = Math.max(1, parseInt(instances,10)||1);
    if (n === 1) return [base];
    const out = [];
    for (let i=1;i<=n;i++) out.push(base+'-'+i);
    return out;
  }

  function buildConfigs(){
    if (!active) return;
    const arch = document.getElementById('f-arch').value;
    const domain = (document.getElementById('f-domain').value||'example.com').trim();
    const tag = (document.getElementById('f-tag').value||'latest').trim();
    const replicas = Math.max(1, parseInt(document.getElementById('f-replicas').value,10)||1);
    const instances = Math.max(1, parseInt(document.getElementById('f-instances').value,10)||1);
    const pops = (document.getElementById('f-pops').value||'sfo,ams').split(/[\s,]+/).filter(Boolean);
    const links = selectedLinks();
    const multi = bargeNames(active.name, instances);

    // --- stack barges ---
    const bargeLines = [];
    const tugBarge = [];
    multi.forEach((name, idx) => {
      const domainLine = instances>1 ? ('    domain: '+name+'.'+domain+'\n') : '';
      bargeLines.push(
        '  - name: '+active.name+'\n'+
        (instances>1 ? '    # instance '+(idx+1)+' as logical name '+name+'\n' : '')+
        '    replicas: '+replicas+'\n'+
        domainLine+
        (instances>1 ? '    env:\n      INSTANCE_NAME: "'+name+'"\n' : '')
      );
      // For multi-instance, still one catalog name but multiple entries with env INSTANCE_NAME
      // YAML name must stay catalog key — use repeated entries with env for distinction.
      tugBarge.push('set stack barge '+active.name+' replicas '+replicas);
      if (instances>1) {
        tugBarge.push('set stack barge '+active.name+' env INSTANCE_NAME '+name);
      }
    });
    // Fix multi-instance YAML: multiple entries with same name merge in stack loader (seen[base.Name]).
    // For true multi-instance, emit unique logical names only as comments and use instances via replicas;
    // OR emit site with multiple pops. Better approach for "multiple of the same":
    // - instances>1 with A6 → one product per PoP
    // - instances>1 with A5 → replicas * instances documented; also emit williwaw entries with tags
    // Stack resolve skips duplicates by catalog name. So for multi of same we bump replicas
    // and document INSTANCE counts, plus optional second deploy via domain-only variants in env.
    let effectiveReplicas = replicas;
    if (instances > 1 && (arch === 'A5' || arch === 'A4' || arch === 'R1')) {
      effectiveReplicas = replicas * instances;
    }

    const linked = [];
    const tugLinked = [];
    // Always include unique primary once
    linked.push('  - name: '+active.name+'\n    replicas: '+effectiveReplicas+'\n'+(domain?('    domain: '+active.name+'.'+domain+'\n'):''));
    tugLinked.push('set stack barge '+active.name+' name '+active.name);
    tugLinked.push('set stack barge '+active.name+' replicas '+effectiveReplicas);
    if (domain) tugLinked.push('set stack barge '+active.name+' domain '+active.name+'.'+domain);

    const seen = new Set([active.name]);
    links.forEach(n => {
      if (seen.has(n)) return;
      seen.add(n);
      linked.push('  - name: '+n+'\n    replicas: 1\n');
      tugLinked.push('set stack barge '+n+' name '+n);
      tugLinked.push('set stack barge '+n+' replicas 1');
    });
    // Kernel for replication when linking data plane
    if (!seen.has('ultimate_db') && (links.indexOf('ultimate_db')>=0 || active.kind==='kernel' || arch==='A6' || arch==='R2')) {
      if (!seen.has('ultimate_db') && active.name !== 'ultimate_db') {
        // only if not already
      }
    }
    if ((arch==='A6'||arch==='R2') && !seen.has('ultimate_db') && active.name!=='ultimate_db') {
      linked.push('  - name: ultimate_db\n    replicas: 1\n    node_id: ultimate-db\n');
      tugLinked.push('set stack barge ultimate_db name ultimate_db');
      seen.add('ultimate_db');
    }
    if ((arch==='A6'||arch==='R2') && !seen.has('ultimate_keystore') && active.name!=='ultimate_keystore') {
      linked.push('  - name: ultimate_keystore\n    replicas: 1\n    node_id: ultimate-keystore\n');
      tugLinked.push('set stack barge ultimate_keystore name ultimate_keystore');
    }

    const stackYaml =
      '# Generated by hub.tunneltug.com config builder\n'+
      '# Architecture: '+arch+' · product: '+active.name+'\n'+
      '# Local embeds stay primary; kernel peers replicate (do not prefer-remote).\n'+
      'namespace: 0trust-stack\n'+
      'tag: '+tag+'\n'+
      'hub_host: hub.tunneltug.com\n'+
      'domain: '+domain+'\n'+
      'public_scheme: https\n'+
      'barges:\n'+linked.join('');

    let yaml = stackYaml;
    let tug =
      '# Generated by hub.tunneltug.com config builder\n'+
      'set site name hub-builder\n'+
      'set site domain '+domain+'\n'+
      'set site public_scheme https\n'+
      'set site token_env TUNNELTUG_TOKEN\n'+
      'set hub host hub.tunneltug.com\n'+
      'set hub tag '+tag+'\n'+
      'set stack namespace 0trust-stack\n'+
      tugLinked.join('\n')+'\n';

    if (arch === 'A6' || arch === 'R2') {
      const popBlocks = pops.map((id, i) => {
        const other = pops.filter(x => x !== id);
        const peerURLs = other.map(o => 'udb-'+o+'=https://kernel-db.'+o+'.'+domain+':8480').join(',');
        return (
          '  - id: '+id+'\n'+
          '    region: '+id+'\n'+
          '    roles: [anycast, lb, barge, stack, kernel]\n'+
          '    domain: '+id+'.'+domain+'\n'+
          '    tunnel:\n'+
          '      public: "443"\n'+
          '      control: "9000"\n'+
          '      prod: true\n'+
          '      barge:\n'+
          '        replicas: '+Math.max(2, replicas)+'\n'+
          '        runtime: k3s\n'+
          '        fleet_id: '+id+'\n'+
          '    kernel:\n'+
          '      ultimate_db:\n'+
          '        node_id: udb-'+id+'\n'+
          '        url: https://kernel-db.'+id+'.'+domain+':8480\n'+
          (peerURLs ? '        # peers auto-expanded from kernel_mesh (full-mesh)\n' : '')+
          '      ultimate_keystore:\n'+
          '        node_id: uks-'+id+'\n'+
          '        url: https://kernel-ks.'+id+'.'+domain+':8481\n'
        );
      }).join('');
      yaml =
        'apiVersion: tunneltug/v1\nkind: Site\n\n'+
        'site:\n  name: hub-builder-'+active.name+'\n  domain: '+domain+'\n  public_scheme: https\n  token_env: TUNNELTUG_TOKEN\n\n'+
        'kernel_mesh:\n  mode: full-mesh\n  transport: https\n\n'+
        'hub:\n  host: hub.tunneltug.com\n  tag: '+tag+'\n\n'+
        'stack:\n  namespace: 0trust-stack\n  barges:\n'+linked.join('')+'\n'+
        'pops:\n'+popBlocks+
        (arch==='R2' ? '\n# Resilient: run anycast per PoP with health-gated BGP (config/anycast.example.yaml).\n' : '');

      tug =
        'set apiVersion tunneltug/v1\nset kind Site\n'+
        'set site name hub-builder-'+active.name+'\n'+
        'set site domain '+domain+'\nset site public_scheme https\nset site token_env TUNNELTUG_TOKEN\n'+
        'set kernel_mesh mode full-mesh\nset kernel_mesh transport https\n'+
        'set hub host hub.tunneltug.com\nset hub tag '+tag+'\n'+
        'set stack namespace 0trust-stack\n'+tugLinked.join('\n')+'\n';
      pops.forEach(id => {
        tug +=
          'set pop '+id+' roles [anycast,lb,barge,stack,kernel]\n'+
          'set pop '+id+' domain '+id+'.'+domain+'\n'+
          'set pop '+id+' tunnel public 443\n'+
          'set pop '+id+' tunnel control 9000\n'+
          'set pop '+id+' tunnel prod true\n'+
          'set pop '+id+' tunnel barge replicas '+Math.max(2,replicas)+'\n'+
          'set pop '+id+' tunnel barge runtime k3s\n'+
          'set pop '+id+' tunnel barge fleet_id '+id+'\n'+
          'set pop '+id+' kernel ultimate_db node_id udb-'+id+'\n'+
          'set pop '+id+' kernel ultimate_db url https://kernel-db.'+id+'.'+domain+':8480\n'+
          'set pop '+id+' kernel ultimate_keystore node_id uks-'+id+'\n'+
          'set pop '+id+' kernel ultimate_keystore url https://kernel-ks.'+id+'.'+domain+':8481\n';
      });
    }

    if (arch === 'A4' || arch === 'R1') {
      yaml =
        '# '+arch+' — LB + barge fleet; product stack co-run via -k3s-stack\n'+
        stackYaml+
        '\n# Barge fleet (engine), separate process or same host:\n'+
        '# tunneltug -mode barge -barge-runtime k3s -barge-replicas '+Math.max(2,replicas)+' \\\n'+
        '#   -barge-lb <lb-host>:443 -k3s-stack -stack-config stack.yaml -token "$TOKEN" -domain '+domain+'\n';
      tug +=
        'set process mode barge\n'+
        '# Also run: -mode lb on the edge; -mode barge with -k3s-stack and this stack\n';
    }

    lastYaml = yaml;
    lastTug = tug;
    const primaryPop = pops[0]||'sfo';
    if (arch === 'A6' || arch === 'R2') {
      lastRun =
        'export TUNNELTUG_TOKEN="$(tunneltug -gen-token)"\n\n'+
        '# Validate expansion\n'+
        'tunneltug -config site.yaml -pop '+primaryPop+' -config-check\n\n'+
        '# PoP '+primaryPop+'\n'+
        'tunneltug -config site.yaml -pop '+primaryPop+' -mode stack -token "$TUNNELTUG_TOKEN"\n\n'+
        '# Tugconf equivalent\n'+
        'tunneltug -config site.tug -pop '+primaryPop+' -config-check\n';
    } else {
      lastRun =
        'export TUNNELTUG_TOKEN="$(tunneltug -gen-token)"\n\n'+
        'tunneltug -mode stack -stack-config stack.yaml -token "$TUNNELTUG_TOKEN"\n\n'+
        '# Or site process with Tugconf stack fragment embedded in site.tug\n'+
        'tunneltug -config site.tug -mode stack -token "$TUNNELTUG_TOKEN"\n';
      if (arch === 'A4' || arch === 'R1') {
        lastRun +=
          '\n# HA barge fleet\n'+
          'tunneltug -mode barge -barge-runtime k3s -barge-replicas '+Math.max(2,replicas)+' \\\n'+
          '  -k3s-stack -stack-config stack.yaml -token "$TUNNELTUG_TOKEN" -domain '+domain+'\n';
      }
    }
  }

  function regenerate(){
    buildConfigs();
    if (tab === 'yaml') cfgOut.textContent = lastYaml;
    else if (tab === 'tug') cfgOut.textContent = lastTug;
    else cfgOut.textContent = lastRun;
  }

  document.querySelectorAll('.tab').forEach(t => {
    t.addEventListener('click', () => {
      document.querySelectorAll('.tab').forEach(x => x.classList.remove('active'));
      t.classList.add('active');
      tab = t.getAttribute('data-tab');
      regenerate();
    });
  });

  document.getElementById('btn-scale').addEventListener('click', () => {
    regenerate();
    showToast('Config updated (scale & link applied)');
  });
  document.getElementById('btn-copy').addEventListener('click', async () => {
    try {
      await navigator.clipboard.writeText(cfgOut.textContent);
      showToast('Copied to clipboard');
    } catch(e) {
      showToast('Copy failed — select text manually');
    }
  });
  document.getElementById('btn-download').addEventListener('click', () => {
    const ext = tab === 'tug' ? 'tug' : (tab === 'run' ? 'sh' : 'yaml');
    const name = (active?active.name:'site')+'.'+ext;
    const blob = new Blob([cfgOut.textContent], {type:'text/plain'});
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = name;
    a.click();
    URL.revokeObjectURL(a.href);
  });

  ['f-arch','f-domain','f-tag','f-replicas','f-instances','f-pops'].forEach(id => {
    document.getElementById(id).addEventListener('change', regenerate);
    document.getElementById(id).addEventListener('input', regenerate);
  });
  linkBox.addEventListener('change', regenerate);

  document.getElementById('modal-close').addEventListener('click', () => overlay.classList.remove('open'));
  overlay.addEventListener('click', e => { if (e.target === overlay) overlay.classList.remove('open'); });
  document.addEventListener('keydown', e => { if (e.key === 'Escape') overlay.classList.remove('open'); });

  function showToast(msg){
    toast.textContent = msg;
    toast.classList.add('show');
    setTimeout(() => toast.classList.remove('show'), 1800);
  }
})();
</script>
</body></html>`
}

func htmlEsc(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return s
}
