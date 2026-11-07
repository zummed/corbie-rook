// Jira's script. input.do names what it does, for the machine that runs it:
//
//   hear          listen.yaml
//   say           issue/comment/create.yaml and chat/ask.yaml
//   ask           chat/ask.yaml
//   history       issue/comment/list.yaml
//   attachments   attachment/get.yaml
//   send          issue/attachments/create.yaml
//
// It calls the REST API of the Jira Cloud at input.site through api.js.
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

// thread reads an issue for a message: the issue itself with its shared files,
// when the message is a comment, then each public comment before the comment
// whose ID is before, oldest first. With before null the message is the issue,
// and every public comment is read. It reads the latest 50 comments, and
// complete says whether that was all of them. A restricted comment is left
// out: the token may read it, and whoever reads the answer may not.
function thread(key, before, me) {
  const most = 50
  const issue = "/issue/" + encodeURIComponent(String(key))
  const name = (author) => (author.accountId === me ? "you" : author.displayName || author.accountId)
  const entries = []
  if (before !== null) {
    const f = call("GET", issue, { query: { fields: "summary,description,reporter,attachment,comment" } }).fields || {}
    entries.push({ line: name(f.reporter || {}) + ": " + (f.summary || "") + "\n\n" + (f.description || ""), attachments: shared(f).map(file) })
  }
  const page = call("GET", issue + "/comment", { query: { orderBy: "-created", maxResults: String(most) } })
  const comments = page.comments || []
  for (const c of comments.slice().reverse().filter(open)) {
    if (before === null || Number(c.id) < before) entries.push({ line: name(c.author || {}) + ": " + (c.body || "") })
  }
  return { entries: entries, complete: page.total === undefined ? comments.length < most : page.total <= comments.length }
}

// wiki gives Markdown as Jira's wiki markup: headings, bold, italic, struck
// text, code, links, images, lists, quotes, rules and tables. A { and a [ that
// starts no link are escaped, since Jira reads them as markup; a mention, as
// [~accountid:…], and a link to an attachment, as [^name], are left as they are.
function wiki(markdown) {
  const out = []
  const lines = String(markdown).split("\n")
  const cells = (line) => line.trim().replace(/^\||\|$/g, "").split(/(?<!\\)\|/).map((c) => inline(c.trim()) || " ")
  const kinds = []
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]
    const fence = /^\s*(```|~~~)/.exec(line)
    if (fence) {
      out.push("{noformat}")
      for (i++; i < lines.length && !lines[i].trim().startsWith(fence[1]); i++) out.push(lines[i])
      out.push("{noformat}")
      continue
    }
    if (/^\s*\|/.test(line) && /^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$/.test(lines[i + 1] || "")) {
      out.push("||" + cells(line).join("||") + "||")
      for (i += 2; i < lines.length && /^\s*\|/.test(lines[i]); i++) out.push("|" + cells(lines[i]).join("|") + "|")
      i--
      continue
    }
    let m
    if (!/^(\s*)([-*+]|\d+[.)])\s+/.test(line)) kinds.length = 0
    if ((m = /^(#{1,6})\s+(.*?)(\s+#+)?\s*$/.exec(line))) out.push("h" + m[1].length + ". " + inline(m[2]))
    else if (/^\s*([-*_])(\s*\1){2,}\s*$/.test(line)) out.push("----")
    else if ((m = /^\s*>\s?(.*)$/.exec(line))) out.push(m[1] ? "bq. " + inline(m[1]) : "")
    else if ((m = /^(\s*)([-*+]|\d+[.)])\s+(.*)$/.exec(line))) {
      const depth = Math.floor(m[1].replace(/\t/g, "    ").length / 2) + 1
      kinds.length = depth
      kinds[depth - 1] = /\d/.test(m[2]) ? "#" : "*"
      out.push(Array.from(kinds, (k) => k || kinds[depth - 1]).join("") + " " + inline(m[3]))
    } else out.push(inline(line))
  }
  return out.join("\n")
}

// inline gives one line's Markdown as wiki markup.
function inline(text) {
  const kept = []
  const keep = (s) => "\u0000" + (kept.push(s) - 1) + "\u0000"
  let s = String(text)
    .replace(/`([^`]+)`/g, (_, code) => keep("{{" + code.replace(/[{}]/g, "\\$&") + "}}"))
    .replace(/!\[[^\]]*\]\(([^)\s]+)[^)]*\)/g, (_, url) => keep("!" + url + "!"))
    .replace(/\[([^\]]+)\]\(([^)\s]+)[^)]*\)/g, (_, words, url) => keep("[" + words.replace(/[|\]]/g, " ") + "|" + url + "]"))
    .replace(/<(https?:\/\/[^>\s]+)>/g, (_, url) => keep("[" + url + "]"))
    .replace(/\[(?=[~^])[^\]]*\]/g, (link) => keep(link))
    .replace(/[{[]/g, "\\$&")
  s = s
    .replace(/(\*\*|__)(?=\S)(.+?)(?<=\S)\1/g, (_, mark, words) => "\u0001" + words + "\u0001")
    .replace(/(?<![*\w])\*(?=[^\s*])(.+?)(?<=[^\s*])\*(?![*\w])/g, "_$1_")
    .replace(/~~(?=\S)(.+?)(?<=\S)~~/g, "-$1-")
    .replace(/\u0001/g, "*")
  return s.replace(/\u0000(\d+)\u0000/g, (_, n) => kept[Number(n)])
}

// say writes text, in Markdown, as comments on an issue, each at most as long as
// Jira takes, with each chart as its table. It gives the last comment's ID.
function say(key, text) {
  if (!text) throw new Error("there is nothing to say")
  const rich = require("../shared/rich.js")
  const longest = 32767
  const issue = "/issue/" + encodeURIComponent(String(key))
  let rest = rich.parts(String(text)).map((p) => (p.chart ? rich.table(p.chart, "jira") : wiki(p.blocks ? rich.words(p.blocks) : p.markdown))).filter((s) => s).join("\n\n")
  let sent
  while (rest.length) {
    sent = call("POST", issue + "/comment", { json: { body: rest.slice(0, longest) } })
    rest = rest.slice(longest)
  }
  return { sent: "comment:" + sent.id }
}

if (input.do === "hear") {
  // The cursor holds the time read up to, what was heard at that moment, the
  // issues opened and not yet heard, and each person's name and email, read
  // within a day.
  const now = Date.now()
  const day = 24 * 3600 * 1000
  if (!input.cursor) return { cursor: { since: new Date(now).toISOString(), seen: [], waiting: [], people: {} } }
  const since = Date.parse(input.cursor.since)
  const seen = input.cursor.seen
  const waiting = (input.cursor.waiting || []).slice()
  const settle = seconds(input.settle)
  const me = call("GET", "/myself").accountId
  const mention = "[~accountid:" + me + "]"
  const people = Object.assign({}, input.cursor.people)
  // person gives the name and email of an account. Jira gives the email only
  // where the person's privacy settings let it.
  const person = (id) => {
    if (people[id] && now - people[id].at < day) return people[id]
    const u = call("GET", "/user", { query: { accountId: id }, maybe: true }) || {}
    return (people[id] = { name: u.displayName || id, email: u.emailAddress || "", at: now })
  }
  // A question's comment shows a line for each choice: the choice and the
  // question's run ID. A comment that holds only such a line answers the question.
  const answer = (text) => /^(.+?)\s+(\d{8}T\d{6}\.\d{3}Z-[0-9a-f]{10})$/.exec(text.trim().replace(/^(\{\{|`)|(\}\}|`)$/g, "").trim())
  const heard = []
  // hear keeps a message that names the bot, and with overhear one that does
  // not, marked unaddressed. before is the comment's ID, or null for the issue.
  const hear = (issue, from, text, before, files) => {
    const named = text.includes(mention)
    if (!named && !input.overhear) return
    const who = person(from)
    const read = thread(issue, before, me)
    const m = { service: "jira", place: issue.slice(0, issue.lastIndexOf("-")), chat: issue, user: from, name: who.name, email: who.email,
      text: text.replaceAll(mention, "").trim(), history: read.entries, complete: read.complete }
    if (files.length) m.attachments = files
    if (!named) m.unaddressed = true
    heard.push(["message", m])
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
      // The bot writes public comments, so one that only some can read is not heard.
      for (const c of ((f.comment && f.comment.comments) || []).filter(open)) add(when(c.created), "comment:" + c.id, issue.key, c.author, c.body || "", linked(shared(f), c.body || ""))
    }
    token = found.nextPageToken
    if (!token || found.isLast) break
  }
  things.sort((a, b) => a.at - b.at)
  // An opened issue waits. A comment that answers a question is an answer. Any
  // other comment on an issue still waiting is read with it, as its history,
  // and the latest that names the bot is kept for when the issue is heard.
  for (const t of things) {
    const a = t.id.startsWith("comment:") && answer(t.text.replaceAll(mention, ""))
    if (a) {
      const who = person(t.from)
      heard.push(["answer", { service: "jira", run: a[2], user: t.from, name: who.name, email: who.email, choice: a[1] }])
    } else if (t.id.startsWith("issue:")) waiting.push({ chat: t.issue, from: t.from })
    else {
      const id = Number(t.id.slice("comment:".length))
      const w = waiting.find((w) => w.chat === t.issue)
      if (!w) hear(t.issue, t.from, t.text, id, t.files)
      else if (t.text.includes(mention)) w.comment = { from: t.from, text: t.text, id: id, files: t.files }
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
    if (w.comment && !text.includes(mention)) hear(w.chat, w.comment.from, w.comment.text, w.comment.id, w.comment.files)
    else hear(w.chat, w.from, text, null, shared(f).map(file))
  }
  // Each history is made a blob only now, when nothing that could fail is left.
  for (const [name, data] of heard) emit(name, name === "message" ? Object.assign(data, { history: blobs.make(data.history) }) : data)
  // At most 500 people are kept, the latest read first.
  const known = Object.fromEntries(Object.entries(people).filter(([, p]) => now - p.at < day).sort((a, b) => b[1].at - a[1].at).slice(0, 500))
  if (!things.length) return { cursor: Object.assign({}, input.cursor, { waiting: still, people: known }) }
  const last = things[things.length - 1].at
  const at = things.filter((t) => t.at === last).map((t) => t.id)
  return { cursor: { since: new Date(last).toISOString(), seen: last === since ? seen.concat(at) : at, waiting: still, people: known } }
}

if (input.do === "history") {
  // The issue and each public comment on it, as shared/thread.js gives them,
  // and whether every comment was read.
  const read = thread(input.issue, Infinity, call("GET", "/myself").accountId)
  return Object.assign({ complete: read.complete }, require("../shared/thread.js").history(read.entries))
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
  const issue = "/issue/" + encodeURIComponent(String(input.issue))
  const plain = String(input.name).replace(/[\[\]|!^]/g, "_")
  const name = call("POST", issue + "/attachments", { upload: { file: { data: input.file, name: plain } }, headers: { "X-Atlassian-Token": "no-check" }, timeout: 120 })[0].filename
  const shown = /^image\/(png|jpeg|gif)$/.test(input.file.type) ? "!" + name + "!" : "[^" + name + "]"
  return { sent: "comment:" + call("POST", issue + "/comment", { json: { body: shown } }).id }
}

if (input.do === "ask") {
  // A question: the text, then a line for each choice, which a comment that
  // holds only that line answers by. input.question is the question's run ID.
  const lines = input.choices.map((c) => "- `" + c + " " + input.question + "`")
  return say(input.issue, input.text + "\n\nTo answer, write a comment that holds only one of these lines:\n\n" + lines.join("\n"))
}

if (input.do === "say") return say(input.issue, input.text)
throw new Error("jira.js does nothing called " + input.do)
