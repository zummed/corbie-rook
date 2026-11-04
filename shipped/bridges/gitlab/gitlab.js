// GitLab's script: input.do is hear, for listen.yaml, say, for say.yaml,
// history, for history.yaml, or attachments, for attachments.yaml. It speaks the
// REST API of the GitLab at input.site through api.js.
const call = require("./api.js").api(input.site)
const project = (chat) => chat.slice(0, chat.lastIndexOf("#"))
const issuePath = (chat) => "/projects/" + encodeURIComponent(project(chat)) + "/issues/" + chat.slice(chat.lastIndexOf("#") + 1)

// GitLab records an issue made in its web UI as a work item.
const issues = ["Issue", "WorkItem"]

// The files a text links to. GitLab writes an upload as ![alt](/uploads/<secret>/<name>)
// or [name](/uploads/<secret>/<name>), a path in the issue's own project; a link
// to another project or host is not taken. GitLab gives no file's type, so its
// name's extension does.
const types = { png: "image/png", jpg: "image/jpeg", jpeg: "image/jpeg", gif: "image/gif", webp: "image/webp", pdf: "application/pdf", txt: "text/plain", log: "text/plain", md: "text/markdown", csv: "text/csv", json: "application/json" }
function attachments(text, project) {
  const found = new Map()
  for (const m of text.matchAll(/\]\(\/uploads\/([0-9a-f]{32})\/([^\/()\s?#]+)\)/g)) {
    const id = "/projects/" + encodeURIComponent(project) + "/uploads/" + m[1] + "/" + m[2]
    const name = decoded(m[2])
    const extension = (/\.(\w+)$/.exec(name) || [])[1] || ""
    found.set(id, { name: name, type: types[extension.toLowerCase()] || "application/octet-stream", id: id })
  }
  return [...found.values()]
}

// decoded reads a name as a link writes it, percent-encoded or not.
function decoded(text) {
  try {
    return decodeURIComponent(text)
  } catch {
    return text
  }
}

// seconds reads a length of time: a number with s, m or h.
function seconds(text) {
  const m = /^(\d+)(s|m|h)$/.exec(String(text))
  if (!m) throw new Error("settle is a number with s, m or h, as 2m, and not " + text)
  return Number(m[1]) * { s: 1, m: 60, h: 3600 }[m[2]]
}

if (input.do === "hear") {
  // The cursor holds the latest event read of each project, and the issues
  // opened and not yet heard.
  const at = Object.assign({}, (input.cursor || {}).at)
  const waiting = ((input.cursor || {}).waiting || []).slice()
  const settle = seconds(input.settle)
  const heard = []
  const me = call("GET", "/user")
  const mention = new RegExp("(^|[^\\w@])@" + me.username.replace(/[.\-]/g, "\\$&") + "(?![\\w.-])", "g")
  // hear keeps a message that names the bot, and with overhear one that does
  // not, marked unaddressed.
  const hear = (message) => {
    const named = message.text.search(mention) >= 0
    message.text = message.text.replace(mention, "$1").trim()
    const files = attachments(message.text, message.place)
    if (files.length) message.attachments = files
    if (named) heard.push(message)
    else if (input.overhear) heard.push(Object.assign(message, { unaddressed: true }))
  }
  for (const p of input.projects) {
    const id = "/projects/" + encodeURIComponent(p)
    if (at[p] === undefined) {
      const latest = call("GET", id + "/events", { query: { per_page: "1" } })
      at[p] = latest.length ? latest[0].id : 0
      continue
    }
    // Events come newest first: read back to the cursor, then take them in the
    // order they happened.
    const events = []
    for (let page = 1; page <= 10; page++) {
      const some = call("GET", id + "/events", { query: { per_page: "100", page: String(page) } })
      const newer = some.filter((e) => e.id > at[p])
      events.push(...newer)
      if (newer.length < some.length || some.length < 100) break
    }
    events.reverse()
    for (const e of events) {
      at[p] = Math.max(at[p], e.id)
      if (e.author_id === me.id || !e.author_username) continue
      // An answer is a public note, so what only members can read is not heard.
      // A note on an issue still waiting is read with it, as its history.
      if (e.note && issues.includes(e.note.noteable_type) && !e.note.system && !e.note.internal && !e.note.confidential) {
        const chat = p + "#" + e.note.noteable_iid
        if (waiting.some((w) => w.chat === chat)) continue
        hear({ bridge: "gitlab", text: String(e.note.body || "").trim(), chat: chat, from: String(e.author_username), place: p, at: e.note.id, settings: { site: input.site } })
      } else if (!e.note && issues.includes(e.target_type) && e.action_name === "opened") {
        waiting.push({ chat: p + "#" + e.target_iid, from: String(e.author_username) })
      }
    }
  }
  // An opened issue is heard once nobody has changed it for settle, as it is then.
  const still = []
  for (const w of waiting) {
    const issue = call("GET", issuePath(w.chat), { maybe: true })
    if (!issue) continue
    if (Date.now() - Date.parse(issue.updated_at) < settle * 1000) {
      still.push(w)
      continue
    }
    const text = ((issue.title || "") + "\n\n" + (issue.description || "")).trim()
    hear({ bridge: "gitlab", text: text, chat: w.chat, from: w.from, place: project(w.chat), settings: { site: input.site } })
  }
  // Said only now, when nothing is left to fail: a job that fails keeps the old
  // cursor, and its next ask hears the same again.
  for (const message of heard) emit("message", message)
  return { cursor: { at: at, waiting: still } }
}

if (input.do === "history") {
  // The issue and its comments before this one. A message that opens an issue
  // holds the issue itself, and has only the comments made while it waited.
  const me = call("GET", "/user").username
  const name = (author) => (author.username === me ? "you" : author.username)
  const lines = []
  if (input.at) {
    const opened = call("GET", issuePath(String(input.to)), { maybe: true })
    if (opened) lines.push(name(opened.author || {}) + ": " + opened.title + "\n\n" + (opened.description || ""))
  }
  const notes = call("GET", issuePath(String(input.to)) + "/notes", { query: { sort: "desc", per_page: "30" }, maybe: true }) || []
  // An internal note is left out: the token may read it, and whoever reads the
  // answer may not.
  for (const note of notes.reverse()) {
    if (note.system || note.internal || note.confidential || (input.at && note.id >= input.at)) continue
    lines.push(name(note.author || {}) + ": " + note.body)
  }
  return lines.length ? require("../../blocks/thread.js").text(lines) : null
}

if (input.do === "attachments") {
  // Each file's contents, downloaded through the API with the token. A file over
  // input.most bytes is left as it is. GitLab answers every file as
  // application/octet-stream, so each keeps the type its name gave it.
  const request = require("./api.js").request(input.site)
  return input.attachments.map((a) => {
    const head = request("HEAD", a.id)
    if (head.status !== 200) throw new Error("GitLab answered the size of " + a.name + " with " + head.status)
    const length = head.headers["content-length"]
    const size = length ? Number(length) : null
    if (size > input.most) return { name: a.name, type: a.type, size: size }
    const reply = request("GET", a.id, { as: "bytes", timeout: 120 })
    if (reply.status !== 200) throw new Error("GitLab answered the download of " + a.name + " with " + reply.status)
    return { name: a.name, type: a.type, size: size, file: blobs.make(reply.body, { type: a.type }) }
  })
}

// say: a comment on the issue, each chart as its table.
const to = String(input.to)
if (to.lastIndexOf("#") < 0) throw new Error("GitLab has no issue " + to)
if (!input.text) throw new Error("there is nothing to say")
const note = call("POST", issuePath(to) + "/notes", { json: { body: require("../../blocks/rich.js").plain(String(input.text)) } })
return { sent: "note:" + note.id }
