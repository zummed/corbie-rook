// Charts in a text, shared by the bridges. A chart is a fenced block marked
// chart that holds one JSON object:
//
//   { "type": "bar", "title": "Tickets", "labels": ["Jan", "Feb"],
//     "series": [{ "name": "Email", "values": [140, 165] }], "x": "Month", "y": "Tickets" }
//
// type is bar, line, area or pie; a pie takes the first series. parts cuts a text
// into its markdown and its charts. table gives a chart as a table, in markdown
// or in Jira's markup. plain gives the text with every chart as its table.
const fence = /^```chart[ \t]*\n([\s\S]*?)\n```[ \t]*$/gm

// chart reads a chart's JSON, or gives null for one that does not fit.
function chart(json) {
  let c
  try {
    c = JSON.parse(json)
  } catch (e) {
    return null
  }
  const ok = c && ["bar", "line", "area", "pie"].includes(c.type) && Array.isArray(c.labels) && Array.isArray(c.series) && c.series.length &&
    c.series.every((s) => s && Array.isArray(s.values) && s.values.length === c.labels.length && s.values.every((v) => typeof v === "number"))
  return ok ? c : null
}

exports.parts = (text) => {
  const out = []
  let from = 0
  for (const m of String(text).matchAll(fence)) {
    const c = chart(m[1])
    if (!c) continue
    if (m.index > from) out.push({ markdown: text.slice(from, m.index) })
    out.push({ chart: c })
    from = m.index + m[0].length
  }
  if (from < text.length) out.push({ markdown: text.slice(from) })
  return out.map((p) => (p.markdown === undefined ? p : { markdown: p.markdown.replace(/^\n+|\n+$/g, "") })).filter((p) => p.chart || p.markdown)
}

exports.table = (c, markup) => {
  const series = c.type === "pie" ? c.series.slice(0, 1) : c.series
  const head = [c.x || "", ...series.map((s, n) => s.name || (series.length > 1 ? "Series " + (n + 1) : c.y || "Value"))]
  const rows = c.labels.map((label, n) => [String(label), ...series.map((s) => String(s.values[n]))])
  const cell = (v) => String(v).replace(/\|/g, "\\|")
  const lines = markup === "jira"
    ? ["||" + head.map(cell).join("||") + "||", ...rows.map((r) => "|" + r.map(cell).join("|") + "|")]
    : ["| " + head.map(cell).join(" | ") + " |", "|" + head.map(() => " --- |").join(""), ...rows.map((r) => "| " + r.map(cell).join(" | ") + " |")]
  return (c.title ? (markup === "jira" ? "*" + c.title + "*" : "**" + c.title + "**") + "\n\n" : "") + lines.join("\n")
}

exports.plain = (text, markup) =>
  exports.parts(text).map((p) => (p.chart ? exports.table(p.chart, markup) : p.markdown)).join("\n\n")
