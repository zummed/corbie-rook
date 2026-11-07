// Charts and layouts in a text, shared by the bridges. A chart is a fenced block marked
// chart that holds one JSON object:
//
//   { "type": "bar", "title": "Tickets", "labels": ["Jan", "Feb"],
//     "series": [{ "name": "Email", "values": [140, 165] }], "x": "Month", "y": "Tickets" }
//
// type is bar, line, area or pie; a pie takes the first series. A layout is a
// fenced block marked blocks that holds a JSON list of Slack's Block Kit blocks.
// parts cuts a text into its markdown, its charts and its layouts. table gives a
// chart as a table, in markdown or in Jira's markup, and words gives a layout's
// text. plain gives the text with every chart as its table and every layout as
// its text.
const fence = /^```(chart|blocks)[ \t]*\n([\s\S]*?)\n```[ \t]*$/gm

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

// layout reads a layout's JSON, or gives null for one that is not a list of blocks.
function layout(json) {
  let b
  try {
    b = JSON.parse(json)
  } catch (e) {
    return null
  }
  return Array.isArray(b) && b.length && b.every((x) => x && typeof x.type === "string") ? b : null
}

exports.parts = (text) => {
  const out = []
  let from = 0
  for (const m of String(text).matchAll(fence)) {
    const p = m[1] === "chart" ? { chart: chart(m[2]) } : { blocks: layout(m[2]) }
    if (!p.chart && !p.blocks) continue
    if (m.index > from) out.push({ markdown: text.slice(from, m.index) })
    out.push(p)
    from = m.index + m[0].length
  }
  if (from < text.length) out.push({ markdown: text.slice(from) })
  return out.map((p) => (p.markdown === undefined ? p : { markdown: p.markdown.replace(/^\n+|\n+$/g, "") })).filter((p) => p.markdown === undefined || p.markdown)
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

// words gives each text in a layout's blocks, a line each: a header's, a
// section's and its fields', a context's.
exports.words = (blocks) => {
  const found = []
  const walk = (x) => {
    if (Array.isArray(x)) return x.forEach(walk)
    if (!x || typeof x !== "object") return
    if ((x.type === "plain_text" || x.type === "mrkdwn") && typeof x.text === "string") return found.push(x.text)
    if (x.type === "markdown" && typeof x.text === "string") return found.push(x.text)
    Object.values(x).forEach(walk)
  }
  walk(blocks)
  return found.join("\n")
}

exports.plain = (text, markup) =>
  exports.parts(text).map((p) => (p.chart ? exports.table(p.chart, markup) : p.blocks ? exports.words(p.blocks) : p.markdown)).filter((s) => s).join("\n\n")
