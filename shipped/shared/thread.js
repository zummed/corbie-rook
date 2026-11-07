// What the services share to give a thread's history, from entries, oldest
// first, each { line, attachments }. text gives them as one text that names
// each entry's files. history gives { text, attachments, complete }: the latest
// entries as text, no more than a model needs, and the latest twenty files on
// any entry, each once, without their contents. complete is true when no entry
// and no character was left out. With no entries it gives null. A thread's
// words cannot close the
// <thread> tag the prompt puts them in.
const mostLines = 20
const mostChars = 8000
const mostFiles = 20

const line = (e) => e.line + ((e.attachments || []).length ? " (files: " + e.attachments.map((a) => a.name).join(", ") + ")" : "")

exports.text = (entries) => entries.map(line).join("\n\n").replace(/<\/?thread/gi, "(thread")

exports.history = (entries) => {
  if (!entries.length) return null
  const text = exports.text(entries.slice(-mostLines))
  const files = [...new Map(entries.flatMap((e) => e.attachments || []).map((a) => [a.id, a])).values()].slice(-mostFiles)
  return {
    text: text.length > mostChars ? "…" + text.slice(1 - mostChars) : text,
    attachments: files,
    complete: entries.length <= mostLines && text.length <= mostChars,
  }
}
