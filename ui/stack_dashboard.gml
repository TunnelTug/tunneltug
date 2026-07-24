html(
  head(
    meta:charset."utf-8"(),
    meta:name."viewport":content."width=device-width, initial-scale=1.0"(),
    title("{{.Title}}"),
    style(
      rule(":root",
        "--bg-app: #1e1f22",
        "--bg-panel: #313338",
        "--bg-elevated: #2b2d31",
        "--bg-hover: #35373c",
        "--text: #dbdee1",
        "--text-muted: #949ba4",
        "--text-heading: #f2f3f5",
        "--brand: #5865f2",
        "--brand-hover: #4752c4",
        "--link: #00a8fc",
        "--online: #23a559",
        "--warn: #f0b232",
        "--danger: #f23f43",
        "--border: #1e1f22",
        "--radius: 8px"
      ),
      rule("*", "box-sizing: border-box"),
      rule("body",
        "background: var(--bg-app)",
        "color: var(--text)",
        "font-family: 'Segoe UI', system-ui, -apple-system, sans-serif",
        "margin: 0",
        "min-height: 100vh",
        "font-size: 15px",
        "line-height: 1.4"
      ),
      rule("a", "color: var(--link)", "text-decoration: none"),
      rule("a:hover", "text-decoration: underline"),
      rule("code",
        "font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace",
        "font-size: 0.88em",
        "background: var(--bg-elevated)",
        "padding: 0.1em 0.35em",
        "border-radius: 4px"
      ),
      rule(".tt-shell", "max-width: 1040px", "margin: 0 auto", "padding: 2rem 1.25rem 3rem"),
      rule(".tt-top",
        "display: flex",
        "align-items: center",
        "justify-content: space-between",
        "gap: 1rem",
        "margin-bottom: 1.25rem",
        "flex-wrap: wrap"
      ),
      rule(".tt-brand",
        "display: flex",
        "align-items: center",
        "gap: 0.75rem",
        "color: var(--text-heading)",
        "font-weight: 600",
        "font-size: 1.25rem"
      ),
      rule(".tt-mark",
        "width: 36px",
        "height: 36px",
        "border-radius: var(--radius)",
        "background: linear-gradient(135deg, #5865f2, #00a8fc)",
        "display: inline-flex",
        "align-items: center",
        "justify-content: center",
        "color: #fff",
        "font-weight: 700",
        "font-size: 0.95rem"
      ),
      rule(".tt-meta", "color: var(--text-muted)", "margin: 0 0 0.75rem", "font-size: 0.95rem"),
      rule(".tt-actions", "display: flex", "gap: 0.75rem", "flex-wrap: wrap", "margin: 0 0 1.5rem"),
      rule(".tt-chip",
        "display: inline-flex",
        "align-items: center",
        "gap: 0.35rem",
        "padding: 0.35rem 0.7rem",
        "border-radius: 999px",
        "background: var(--bg-elevated)",
        "color: var(--text)",
        "font-size: 0.88rem"
      ),
      rule(".tt-chip a", "color: var(--link)"),
      rule(".tt-panel",
        "background: var(--bg-panel)",
        "border-radius: 12px",
        "overflow: hidden",
        "border: 1px solid #3f4147"
      ),
      rule("table.tt-table", "width: 100%", "border-collapse: collapse"),
      rule("table.tt-table th",
        "text-align: left",
        "padding: 0.75rem 1rem",
        "color: var(--text-muted)",
        "font-size: 0.8rem",
        "text-transform: uppercase",
        "letter-spacing: 0.04em",
        "background: var(--bg-elevated)",
        "border-bottom: 1px solid #3f4147"
      ),
      rule("table.tt-table td",
        "padding: 0.85rem 1rem",
        "border-bottom: 1px solid #3f4147",
        "vertical-align: top"
      ),
      rule("table.tt-table tr:last-child td", "border-bottom: none"),
      rule("table.tt-table tr:hover td", "background: var(--bg-hover)"),
      rule(".phase-ready", "color: var(--online)", "font-weight: 600"),
      rule(".phase-prog", "color: var(--warn)", "font-weight: 600"),
      rule(".phase-bad", "color: var(--danger)", "font-weight: 600"),
      rule(".tt-img", "display: block", "margin-top: 0.25rem", "color: var(--text-muted)", "font-size: 0.78rem", "word-break: break-all"),
      rule(".tt-note", "margin-top: 1.25rem", "color: var(--text-muted)", "font-size: 0.9rem")
    )
  ),
  body(
    div.tt-shell(
      div.tt-top(
        div.tt-brand(
          span.tt-mark("TT"),
          span("{{.Title}}")
        ),
        span.tt-chip("k3s · no kubectl")
      ),
      p.tt-meta(
        "namespace ", code("{{.Namespace}}"),
        " · tag ", code("{{.Tag}}"),
        " · mesh zone ", code("{{.MeshZone}}"),
        " · host_ports ", code("{{.HostPorts}}"),
        " · gateway ", code("{{.GatewayOn}}")
      ),
      div.tt-actions(
        span.tt-chip(a:href."/_tunneltug/stack"("JSON status")),
        span.tt-chip(a:href."{{.MeshAuthURL}}"("Sign in · mesh HTTPS")),
        span.tt-chip(a:href."{{.MeshShellURL}}"("gonode CLI · mesh")),
        span.tt-chip(code("/apps/{name}/"), " one-port gateway"),
        span.tt-chip("mesh DNS + meshTLS (not localhost)")
      ),
      div.tt-panel(
        table.tt-table(
          thead(
            tr(
              th("App"),
              th("Phase"),
              th("Ready"),
              th("Port"),
              th("Mesh address"),
              th("Open")
            )
          ),
          tbody(
            "{{range .Apps}}",
            tr(
              td(
                strong("{{.Name}}"),
                code.tt-img("{{.Image}}")
              ),
              td:class."{{.PhaseClass}}"("{{.Phase}}"),
              td("{{.Ready}} / {{.Desired}}"),
              td("{{.Port}}"),
              td(
                a:href."{{.MeshURL}}"(code("{{.MeshFQDN}}")),
                code.tt-img("{{.MeshURL}}")
              ),
              td(
                a:href."{{.AccessURL}}"("open"),
                "{{if .ShowGateway}} · {{end}}",
                "{{if .ShowGateway}}",
                a:href."{{.GatewayURL}}"("gateway"),
                "{{end}}",
                " · ",
                a:href."{{.MeshURL}}"("mesh"),
                " · ",
                a:href."{{.ShellURL}}"("shell")
              )
            ),
            "{{end}}"
          )
        )
      ),
      p.tt-note("{{.Note}}"),
      p.tt-note("Auth + shell require mesh DNS + meshTLS HTTPS (WebAuthn cannot use http/localhost). Sign in on stack.{mesh-zone}, then gonode Junos CLI. Install mesh-root.crt.")
    )
  )
)
