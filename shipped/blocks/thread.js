// What the bridges share to give a thread's history. history gives entries,
// oldest first, each { line, attachments }, as { text, attachments }: the latest
// entries as one text that names each entry's files, no more than a model needs,
// and at most the latest ten files on them, each marked earlier. With no entries it gives null. A thread's
// words cannot close the <thread> tag the prompt puts them in.
const mostLines = 20
const mostChars = 8000
const mostFiles = 10

exports.history = (entries) => {
  if (!entries.length) return null
  const latest = entries.slice(-mostLines)
  const line = (e) => e.line + ((e.attachments || []).length ? " (files: " + e.attachments.map((a) => a.name).join(", ") + ")" : "")
  const text = latest.map(line).join("\n\n").replace(/<\/?thread/gi, "(thread")
  const files = latest.flatMap((e) => e.attachments || []).slice(-mostFiles).map((a) => Object.assign({}, a, { earlier: true }))
  return { text: text.length > mostChars ? "…" + text.slice(1 - mostChars) : text, attachments: files }
}
