// Jira's script: input.do is hear, for listen.yaml, say, for say.yaml, history,
// for history.yaml, attachments, for attachments.yaml, or send, for send.yaml. It
// speaks the REST API of the Jira Cloud at input.site through api.js.
const call = require("./api.js").api(input.site, input.email)

// open says everyone who can read the issue can read a comment: one with a
// visibility is for a role or a group, and jsdPublic false is Service Management's
// internal comment.
const open = (c) => !c.visibility && c.jsdPublic !== false

// when reads one of Jira's times, as 2026-10-05T09:00:00.000+0000.
const when = (text) => Date.parse(String(text).replace(/([+-]\d\d)(\d\d)$/, "$1:$2"))

// file describes one of an issue's attachments for a message.
const file = (a) => ({ name: a.filename, type: a.mimeType || "application/octet-stream", size: a.size, id: String(a.id) })

// names says whether wiki markup shows a file, as !name! or !name|…!, or links to
// it, as [^name]. An external image, as !https://…!, names no file.
const names = (body, a) => ["!" + a.filename + "!", "!" + a.filename + "|", "[^" + a.filename + "]"].some((s) => body.includes(s))

// shared gives the issue's files except each whose name a restricted comment
// names, since Jira tells such a file from another only by its name.
const shared = (f) => {
  const restricted = ((f.comment && f.comment.comments) || []).filter((c) => !open(c)).map((c) => c.body || "").join("\n")
  return (f.attachment || []).filter((a) => !names(restricted, a))
}

// linked gives the files a comment names. Of two with one name, it gives the
// newest.
function linked(files, body) {
  const shown = files.filter((a) => names(body, a)).sort((a, b) => when(a.created) - when(b.created))
  return [...new Map(shown.map((a) => [a.filename, a])).values()].map(file)
}

// seconds reads a length of time: a number with s, m or h.
function seconds(text) {
  const m = /^(\d+)(s|m|h)$/.exec(String(text))
  if (!m) throw new Error("settle is a number with s, m or h, as 2m, and not " + text)
  return Number(m[1]) * { s: 1, m: 60, h: 3600 }[m[2]]
}

if (input.do === "hear") {
  // The cursor holds the time read up to, what was heard at that moment, and the
  // issues opened and not yet heard.
  const now = Date.now()
  if (!input.cursor) return { cursor: { since: new Date(now).toISOString(), seen: [], waiting: [] } }
  const since = Date.parse(input.cursor.since)
  const seen = input.cursor.seen
  const waiting = (input.cursor.waiting || []).slice()
  const settle = seconds(input.settle)
  const me = call("GET", "/myself").accountId
  const mention = "[~accountid:" + me + "]"
  const heard = []
  // hear keeps a message that names the bot, and with overhear one that does
  // not, marked unaddressed.
  const hear = (message) => {
    const named = message.text.includes(mention)
    message.text = message.text.replaceAll(mention, "").trim()
    if (named) heard.push(message)
    else if (input.overhear) heard.push(Object.assign(message, { unaddressed: true }))
  }
  const message = (issue, from, text, at, files) => {
    const m = { bridge: "jira", text: text, chat: issue, at: at, from: from, place: issue.slice(0, issue.lastIndexOf("-")), settings: { site: input.site, email: input.email } }
    if (files.length) m.attachments = files
    return m
  }
  const minutes = Math.ceil((now - since) / 60000) + 2
  const jql = "project in (" + input.projects.map((p) => JSON.stringify(p)).join(", ") + ") AND updated >= -" + minutes + "m ORDER BY updated ASC"
  // What came at the cursor's own moment is heard unless the job before heard it.
  const things = []
  const add = (at, id, issue, author, text, files) => {
    if (author && author.accountId !== me && (at > since || (at === since && !seen.includes(id)))) things.push({ at: at, id: id, issue: issue, from: author.accountId, text: text, files: files })
  }
  let token
  for (let page = 0; page < 10; page++) {
    const query = { jql: jql, fields: "summary,description,reporter,created,comment,attachment", maxResults: "50" }
    if (token) query.nextPageToken = token
    const found = call("GET", "/search/jql", { query: query })
    for (const issue of found.issues || []) {
      const f = issue.fields || {}
      add(when(f.created), "issue:" + issue.id, issue.key, f.reporter, (f.summary || "") + "\n\n" + (f.description || ""), [])
      // An answer is a public comment, so what only some can read is not heard.
      for (const c of ((f.comment && f.comment.comments) || []).filter(open)) add(when(c.created), "comment:" + c.id, issue.key, c.author, c.body || "", linked(shared(f), c.body || ""))
    }
    token = found.nextPageToken
    if (!token || found.isLast) break
  }
  things.sort((a, b) => a.at - b.at)
  // An opened issue waits. A comment on an issue still waiting is read with it, as
  // its history, and the latest that names the bot is kept for when the issue is
  // heard.
  for (const t of things) {
    if (t.id.startsWith("issue:")) waiting.push({ chat: t.issue, from: t.from, at: t.id })
    else {
      const w = waiting.find((w) => w.chat === t.issue)
      if (!w) hear(message(t.issue, t.from, t.text, t.id, t.files))
      else if (t.text.includes(mention)) w.comment = message(t.issue, t.from, t.text, t.id, t.files)
    }
  }
  // An opened issue is heard once nobody has changed it for settle, as it is then,
  // with its shared files. When it does not name the bot and a comment made while
  // it waited does, that comment is heard in its place.
  const still = []
  for (const w of waiting) {
    const f = (call("GET", "/issue/" + encodeURIComponent(w.chat), { query: { fields: "summary,description,updated,attachment,comment" }, maybe: true }) || {}).fields
    if (!f) continue
    if (now - when(f.updated) < settle * 1000) {
      still.push(w)
      continue
    }
    const text = (f.summary || "") + "\n\n" + (f.description || "")
    hear(w.comment && !text.includes(mention) ? w.comment : message(w.chat, w.from, text, w.at, shared(f).map(file)))
  }
  for (const m of heard) emit("message", m)
  if (!things.length) return { cursor: Object.assign({}, input.cursor, { waiting: still }) }
  const last = things[things.length - 1].at
  const at = things.filter((t) => t.at === last).map((t) => t.id)
  return { cursor: { since: new Date(last).toISOString(), seen: last === since ? seen.concat(at) : at, waiting: still } }
}

if (input.do === "history") {
  // The issue and its comments before this one, the issue with its shared
  // files. A message that opens an issue holds the issue itself and its files,
  // and has only the comments made while it waited.
  const at = String(input.at || "")
  const comment = at.startsWith("comment:")
  const id = comment ? Number(at.slice("comment:".length)) : Infinity
  const issue = "/issue/" + encodeURIComponent(String(input.to))
  const me = call("GET", "/myself").accountId
  const name = (author) => (author.accountId === me ? "you" : author.displayName || author.accountId)
  const entries = []
  if (comment) {
    const f = call("GET", issue, { query: { fields: "summary,description,reporter,attachment,comment" } }).fields || {}
    entries.push({ line: name(f.reporter || {}) + ": " + (f.summary || "") + "\n\n" + (f.description || ""), attachments: shared(f).map(file) })
  }
  const comments = call("GET", issue + "/comment", { query: { orderBy: "-created", maxResults: "30" } }).comments || []
  // A restricted comment is left out: the token may read it, and whoever reads
  // the answer may not.
  for (const c of comments.reverse().filter(open)) {
    if (Number(c.id) < id) entries.push({ line: name(c.author || {}) + ": " + c.body })
  }
  return require("../../blocks/thread.js").history(entries)
}

if (input.do === "attachments") {
  // Each file's contents, read from the site itself: by default Jira answers with
  // a redirect to another host, which the token may not go to, so the call asks
  // for none and follows none. A file over input.most bytes is left as it is.
  return input.attachments.map((a) => {
    if (a.size > input.most) return a
    const bytes = call("GET", "/attachment/content/" + encodeURIComponent(a.id), { query: { redirect: "false" }, redirects: false, as: "bytes", timeout: 120 })
    return Object.assign({}, a, { file: blobs.make(bytes, { type: a.type }) })
  })
}

if (input.do === "send") {
  // The file attached to the issue, then a comment that shows it, so the issue's
  // readers see it: !name! for a PNG, JPEG or GIF, which Jira shows in the
  // comment, and [^name] for any other. The comment names the file as Jira keeps
  // it. Each [, ], |, ! and ^ in the name is sent as _, since they end the markup.
  const issue = "/issue/" + encodeURIComponent(String(input.to))
  const plain = String(input.name).replace(/[\[\]|!^]/g, "_")
  const name = call("POST", issue + "/attachments", { upload: { file: { data: input.file, name: plain } }, headers: { "X-Atlassian-Token": "no-check" }, timeout: 120 })[0].filename
  const shown = /^image\/(png|jpeg|gif)$/.test(input.file.type) ? "!" + name + "!" : "[^" + name + "]"
  return { sent: "comment:" + call("POST", issue + "/comment", { json: { body: shown } }).id }
}

// say: a comment on the issue, in parts Jira takes, each chart as its table.
if (!input.text) throw new Error("there is nothing to say")
const longest = 32767
const issue = "/issue/" + encodeURIComponent(String(input.to))
let text = require("../../blocks/rich.js").plain(String(input.text), "jira")
let sent
while (text.length) {
  sent = call("POST", issue + "/comment", { json: { body: text.slice(0, longest) } })
  text = text.slice(longest)
}
return { sent: "comment:" + sent.id }
