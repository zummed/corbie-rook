// GitLab's script. Each machine runs it with its own input.do:
//
//   hear              listen.yaml
//   ask, tell, close  chat/show.yaml: the question, a comment on an answer that does not count, the closing comment
//   note              issues/notes/create.yaml
//   notes             issues/notes/list.yaml
//   issue             issues/read.yaml
//   upload            uploads/create.yaml
//   file              repository/files/read.yaml
//   commit            repository/commits/create.yaml
//   pipeline          pipeline/create.yaml
//   attachments       files/get.yaml
//
// It speaks the REST API of the GitLab at input.site through api.js. A chat is
// an issue, written <project's path>#<issue's IID>.
const call = require("./api.js").api(input.site)
const projectPath = (project) => "/projects/" + encodeURIComponent(project)
const issuePath = (project, iid) => projectPath(project) + "/issues/" + iid

// chatOf splits a chat into its project and its issue's IID.
function chatOf(chat) {
  const at = String(chat).lastIndexOf("#")
  if (at <= 0 || !/^\d+$/.test(String(chat).slice(at + 1))) throw new Error("GitLab has no issue " + chat + ": a chat is a project's path, #, and an issue's number, as group/project#7")
  return { project: String(chat).slice(0, at), iid: Number(String(chat).slice(at + 1)) }
}

// The files a text links to. GitLab writes an upload as ![alt](/uploads/<secret>/<name>)
// or [name](/uploads/<secret>/<name>), a path in the issue's own project; a link
// to another project or host is not an attachment, nor is a name that could
// leave the upload's directory: dots only, or an escaped . / or \. GitLab gives
// no file's type, so its name's extension does.
function attachments(text, project) {
  const type = require("../shared/types.js").of
  const found = new Map()
  for (const m of String(text).matchAll(/\]\(\/uploads\/([0-9a-f]{32})\/([^\/\\()\s?#]+)\)/g)) {
    const name = m[2]
    if (/^\.+$/.test(name) || /%2e|%2f|%5c/i.test(name)) continue
    const id = projectPath(project) + "/uploads/" + m[1] + "/" + name
    found.set(id, { name: name, type: type(name, "application/octet-stream"), id: id })
  }
  return [...found.values()]
}

// A question is a comment of the bot's that ends with a line GitLab does not
// show: <!-- rook question <run> <choice> ... --> while it waits, each choice
// URL-encoded, and a later comment of the bot's that holds <!-- rook answered
// <run> --> closes it. The listener reads these lines only in the bot's own
// comments, and the bot writes them only at the end of a question and of the
// comment that closes it: every text it is given to write has them taken out.
const askedLine = /<!-- rook question (\S+)((?: \S+)+) -->/
const closedLine = /<!-- rook answered (\S+) -->/
function unmarked(text) {
  let out = String(text)
  while (askedLine.test(out) || closedLine.test(out)) out = out.replace(askedLine, "").replace(closedLine, "")
  return out
}

// public says whether everyone who can read the issue can read a note: an
// answer is public, so an internal note is not heard nor given as history.
const isPublic = (note) => !note.system && !note.internal && !note.confidential

// overlap is how far before the last ask's start a listener's ask reads.
const overlap = 120 * 1000

if (input.do === "hear") return hear()

if (input.do === "ask") {
  const { project, iid } = chatOf(input.in)
  const choices = input.choices.map(String)
  const line = "<!-- rook question " + input.question + " " + choices.map(encodeURIComponent).join(" ") + " -->"
  const body = unmarked(input.text) + "\n\nAnswer with a comment that says " + choices.map((c) => "`" + c + "`").join(" or ") + ".\n\n" + line
  call("POST", issuePath(project, iid) + "/notes", { json: { body: body } })
  return { sent: { in: input.in, question: input.question }, where: "GitLab" }
}

if (input.do === "tell") {
  // A comment on the question's issue telling input.by why their answer does
  // not count.
  const { project, iid } = chatOf(input.sent.in)
  const text = String(input.text)
  call("POST", issuePath(project, iid) + "/notes", { json: { body: unmarked(input.by + ", " + text[0].toLowerCase() + text.slice(1)) } })
  return {}
}

if (input.do === "close") {
  // A comment on the question's issue that closes the question.
  const { project, iid } = chatOf(input.sent.in)
  const body = unmarked(input.said) + "\n\n<!-- rook answered " + input.sent.question + " -->"
  call("POST", issuePath(project, iid) + "/notes", { json: { body: body } })
  return {}
}

if (input.do === "note") {
  if (!input.body) throw new Error("the comment is empty")
  const note = call("POST", issuePath(input.project, input.iid) + "/notes", { json: { body: unmarked(require("../shared/rich.js").plain(String(input.body))) } })
  return { id: note.id }
}

if (input.do === "notes") {
  // The latest hundred public comments, oldest first.
  const notes = call("GET", issuePath(input.project, input.iid) + "/notes", { query: { sort: "desc", order_by: "created_at", per_page: "100" } })
  return notes.filter(isPublic).reverse().map((n) => ({
    id: n.id, user: String(n.author.id), name: n.author.name, username: n.author.username, text: n.body, created: n.created_at,
    attachments: attachments(n.body, input.project),
  }))
}

if (input.do === "issue") {
  const is = call("GET", issuePath(input.project, input.iid))
  return {
    iid: is.iid, title: is.title, description: is.description || "", state: is.state, labels: is.labels || [],
    milestone: is.milestone ? is.milestone.title : null, assignees: (is.assignees || []).map((a) => a.username),
    author: is.author.username, created: is.created_at, updated: is.updated_at, url: is.web_url,
    attachments: attachments(is.description || "", input.project),
  }
}

if (input.do === "upload") {
  // The file is uploaded to the project, and with an issue a comment there is
  // the link GitLab gives for it.
  const file = call("POST", projectPath(input.project) + "/uploads", { upload: { file: { data: input.file, name: input.name } }, timeout: 120 })
  const out = { markdown: file.markdown, url: file.url }
  if (input.iid) out.note = call("POST", issuePath(input.project, input.iid) + "/notes", { json: { body: unmarked(file.markdown) } }).id
  return out
}

if (input.do === "file") {
  const ref = input.ref || "HEAD"
  const text = call("GET", projectPath(input.project) + "/repository/files/" + encodeURIComponent(input.path) + "/raw", { query: { ref: ref }, raw: true })
  return { project: input.project, path: input.path, ref: ref, text: text }
}

if (input.do === "commit") {
  const body = { branch: input.branch, commit_message: input.message, actions: input.actions }
  if (input.start_branch) body.start_branch = input.start_branch
  const c = call("POST", projectPath(input.project) + "/repository/commits", { json: body })
  return { id: c.id, short_id: c.short_id, title: c.title, url: c.web_url || null }
}

if (input.do === "pipeline") {
  const variables = Object.entries(input.variables || {}).map(([key, value]) => ({ key: key, value: String(value) }))
  const p = call("POST", projectPath(input.project) + "/pipeline", { json: { ref: input.ref, variables: variables } })
  return { id: p.id, status: p.status, ref: p.ref, url: p.web_url }
}

if (input.do === "attachments") {
  // Each file's contents, downloaded through the API with the token. A file over
  // input.most bytes, by HEAD's Content-Length or by what GET gave, has no file.
  // GitLab answers every file as application/octet-stream, so each keeps the
  // type its name gave it.
  const site = input.site
  const options = { headers: { "PRIVATE-TOKEN": secrets.get("GITLAB_TOKEN") }, timeout: 30 }
  return input.attachments.map((a) => {
    const address = "https://" + site + "/api/v4" + a.id
    const head = http.request(address, Object.assign({ method: "HEAD" }, options))
    if (head.status !== 200) throw new Error("GitLab answered the size of " + a.name + " with " + head.status)
    const length = Number(head.headers["content-length"])
    if (length > input.most) return Object.assign({}, a, { size: length })
    const reply = http.get(address, Object.assign({}, options, { as: "bytes", timeout: 120 }))
    if (reply.status !== 200) throw new Error("GitLab answered the download of " + a.name + " with " + reply.status)
    const size = reply.body.length
    if (size > input.most) return Object.assign({}, a, { size: size })
    return Object.assign({}, a, { size: size, file: blobs.make(reply.body, { type: a.type }) })
  })
}

throw new Error("gitlab.js has no do: " + input.do)

// seconds reads a length of time: a number with s, m or h.
function seconds(text) {
  const m = /^(\d+)(s|m|h)$/.exec(String(text))
  if (!m) throw new Error("settle is a number with s, m or h, as 2m, and not " + text)
  return Number(m[1]) * { s: 1, m: 60, h: 3600 }[m[2]]
}

// newest keeps the n entries of a map with the latest at.
function newest(map, n) {
  return Object.fromEntries(Object.entries(map).sort((a, b) => b[1].at - a[1].at).slice(0, n))
}

// hear asks GitLab what changed on the issues of input.projects and
// input.group since its last ask, and emits message and answer for each, in
// the order they happened. The cursor holds where it stands:
//
//   since      GitLab's time, by its Date header, when the last ask started
//   start      two minutes before the first ask started; nothing before it is read
//   iids       the highest issue IID seen, by project: a higher one is opened;
//              a project not here starts from GitLab's highest made before
//              the ask's window
//   seen       the notes and events already heard that a next ask reads again
//   issues     each issue's labels, milestone and state when last read
//   people     each person's email, by user ID, kept a day
//   waiting    the opened issues not heard until nobody has changed them for settle
//   questions  the question waiting on each issue, from the bot's own comments
//
// A first ask reads as any other and emits nothing, so that what it read is
// not heard again. Each ask reads the issues changed since two minutes before
// the last one started, since GitLab moves an issue's updated_at at most once a
// minute for a comment, then their notes made since then, leaving out those in
// seen. Changes are read from the resource events of a field that differs
// from the issue as last read, made since it was read; a field changed and
// changed back between two asks is not heard. Every list in the cursor is
// bounded, so that it stays far below the 256 KiB a job's output may hold.
function hear() {
  const before = input.cursor
  try {
    const me = call("GET", "/user", { whole: true })
    const now = Date.parse(me.headers["date"])
    if (!now) throw new Error("GitLab gave no Date header to tell its time by")
    if (!before) return poll({ since: now, start: now - overlap }, me.body, now, true)
    return poll(before, me.body, now, false)
  } catch (e) {
    // GitLab asked to wait: the next ask comes once it has, from the same cursor.
    if (!e.retry) throw e
    log(e.message + "; asking again in " + e.retry + "s")
    return { cursor: before, rest: Math.min(Math.max(e.retry, 30), 3600) + "s" }
  }
}

// poll asks once. A quiet one emits nothing.
function poll(before, bot, now, quiet) {
  const from = Math.max(before.since - overlap, before.start)
  const next = Math.max(now - overlap, before.start)
  const settle = seconds(input.settle) * 1000
  const seen = new Set(before.seen || [])
  const seenNext = []
  const iids = Object.assign({}, before.iids)
  const issues = Object.assign({}, before.issues)
  const people = Object.fromEntries(Object.entries(before.people || {}).filter(([, p]) => now - p.at < 86400000))
  const questions = Object.assign({}, before.questions)
  const waiting = (before.waiting || []).slice()
  const heard = []
  const mention = new RegExp("(^|[^\\w@])@" + bot.username.replace(/[.\-]/g, "\\$&") + "(?![\\w-]|\\.[\\w-]) ?", "g")
  const names = (text) => String(text).search(mention) >= 0
  const unnamed = (text) => String(text).replace(mention, "$1").trim()

  // fresh says whether a note or an event is new to this ask, by its key, and
  // keeps the key for the next ask when that one reads it again.
  const fresh = (key, at) => {
    if (at >= next) seenNext.push(key)
    return !seen.has(key)
  }

  // person gives what a message holds of who wrote it: the email is GitLab's
  // public one, or any one with an administrator's token, kept a day.
  const person = (author) => {
    const id = String(author.id)
    if (!people[id]) {
      const u = call("GET", "/users/" + author.id, { maybe: true }) || {}
      people[id] = { email: u.email || u.public_email || "", at: now }
    }
    const out = { user: id, name: author.name || author.username, email: people[id].email }
    if (author.id === bot.id) out.own = true
    return out
  }

  // The issues changed since from, newest first, each once; a list GitLab
  // pages as it changes can give one twice, never miss one.
  const changed = new Map()
  const sources = (input.projects || []).map((p) => ({ path: projectPath(p), project: p }))
  if (input.group) sources.push({ path: "/groups/" + encodeURIComponent(input.group) })
  if (!sources.length) throw new Error("the GitLab listener reads nothing: give it projects:, a list of projects' paths, or group:, a group's path")
  for (const source of sources) {
    for (let page = 1; ; page++) {
      const query = { updated_after: new Date(from).toISOString(), order_by: "updated_at", sort: "desc", state: "all", per_page: "100", page: String(page) }
      const got = call("GET", source.path + "/issues", { query: query })
      const inside = got.filter((is) => Date.parse(is.updated_at) >= from)
      for (const is of inside) {
        const project = source.project || is.references.full.slice(0, is.references.full.lastIndexOf("#"))
        changed.set(project + "#" + is.iid, Object.assign({ project: project }, is))
      }
      if (got.length < 100 || inside.length < got.length) break
    }
  }

  // An issue's notes are read newest first, a page at a time, only as far as
  // needed.
  const readers = new Map()
  const notesOf = (project, iid) => {
    const chat = project + "#" + iid
    if (!readers.has(chat)) {
      const r = { list: [], page: 0, end: false }
      r.more = () => {
        r.page++
        const got = call("GET", issuePath(project, iid) + "/notes", { query: { sort: "desc", order_by: "created_at", per_page: "100", page: String(r.page) } })
        r.list.push(...got)
        if (got.length < 100) r.end = true
      }
      readers.set(chat, r)
    }
    return readers.get(chat)
  }

  // thread gives the history of a message and whether it is complete: the
  // issue, for a comment, and the public comments before it, as a blob of a
  // list of { line, attachments }, oldest first, its latest 20 entries and 8000
  // characters at most. The bot's own are said by "you".
  const thread = (issue, note) => {
    const r = notesOf(issue.project, issue.iid)
    const earlier = (n) => !note || Date.parse(n.created_at) < Date.parse(note.created_at) || (n.created_at === note.created_at && n.id < note.id)
    while (!r.end && r.page < 5 && r.list.filter((n) => isPublic(n) && earlier(n)).length < 21) r.more()
    const name = (author) => (author.id === bot.id ? "you" : author.username)
    const entry = (author, text) => ({ line: name(author || {}) + ": " + text, attachments: attachments(text, issue.project) })
    const entries = note ? [entry(issue.author, issue.title + "\n\n" + (issue.description || ""))] : []
    for (const n of r.list.filter((n) => isPublic(n) && earlier(n)).reverse()) entries.push(entry(n.author, n.body))
    let kept = 0
    let size = 0
    while (kept < Math.min(entries.length, 20) && size + entries[entries.length - 1 - kept].line.length <= 8000) {
      size += entries[entries.length - 1 - kept].line.length
      kept++
    }
    return { history: blobs.make(entries.slice(entries.length - kept)), complete: r.end && kept === entries.length }
  }

  // say keeps a message or an answer, to be emitted in the order things
  // happened.
  const say = (at, event, data) => heard.push({ at: at, event: event, data: Object.assign({ service: "gitlab" }, data) })

  // comment keeps a message for a comment, or for an opened issue with no
  // note: one that names the bot, the bot's own, and with overhear any other,
  // marked unaddressed.
  const comment = (issue, note) => {
    if (quiet) return
    const author = note ? note.author : issue.author
    const said = note ? note.body.replace(askedLine, "").replace(closedLine, "") : issue.title + "\n\n" + (issue.description || "")
    const who = person(author)
    if (!who.own && !names(said) && !input.overhear) return
    const text = unnamed(said)
    const m = Object.assign({ place: issue.project, chat: issue.project + "#" + issue.iid }, who, { text: text }, thread(issue, note))
    const files = attachments(text, issue.project)
    if (files.length) m.attachments = files
    if (!who.own && !names(said)) m.unaddressed = true
    say(Date.parse(note ? note.created_at : issue.created_at), "message", m)
  }

  // change keeps a message for a change to an issue.
  const change = (issue, at, author, kind, text) => {
    if (quiet) return
    say(at, "message", Object.assign({ place: issue.project, chat: issue.project + "#" + issue.iid }, person(author), { text: text, change: kind }))
  }

  // events gives an issue's resource events of a kind made since from, and
  // since the issue was last read. GitLab gives them oldest first, so the last
  // pages are read.
  const events = (issue, kind, was) => {
    const since = Math.max(from, was ? was.at : 0)
    const path = issuePath(issue.project, issue.iid) + "/resource_" + kind + "_events"
    const get = (page) => call("GET", path, { query: { per_page: "100", page: String(page) }, whole: true })
    const first = get(1)
    let page = Number(first.headers["x-total-pages"]) || 1
    let found = page === 1 ? first.body : get(page).body
    while (page > 1 && found.length && Date.parse(found[0].created_at) >= since) {
      page--
      found = (page === 1 ? first.body : get(page).body).concat(found)
    }
    return found.filter((e) => Date.parse(e.created_at) >= since && fresh(kind + e.id, Date.parse(e.created_at)))
  }

  // highest gives the highest IID of a project's issues made before from, or 0
  // for none. Any issue made since is listed in this ask or was in an earlier
  // one, so this is the mark of a project not seen before.
  const highest = (project) => {
    const query = { created_before: new Date(from).toISOString(), order_by: "created_at", sort: "desc", state: "all", per_page: "1" }
    const top = call("GET", projectPath(project) + "/issues", { query: query })
    return top.length ? top[0].iid : 0
  }

  for (const issue of [...changed.values()].reverse()) {
    const chat = issue.project + "#" + issue.iid
    const known = iids[issue.project] !== undefined ? iids[issue.project] : highest(issue.project)
    const opened = issue.iid > known
    delete iids[issue.project]
    iids[issue.project] = Math.max(known, issue.iid)
    if (opened && !quiet) waiting.push({ chat: chat })
    const wait = waiting.find((w) => w.chat === chat)

    // The comments made since from, oldest first.
    const r = notesOf(issue.project, issue.iid)
    while (!r.end && (!r.list.length || Date.parse(r.list[r.list.length - 1].created_at) >= from)) r.more()
    const notes = r.list.filter((n) => Date.parse(n.created_at) >= from && fresh("note" + n.id, Date.parse(n.created_at))).reverse()
    let assigned = null
    for (const n of notes) {
      // GitLab gives no event for a change of assignees, only a system note.
      if (n.system && /^(assigned to|unassigned|removed assignee)/.test(n.body)) assigned = n
      if (!isPublic(n)) continue
      if (n.author.id === bot.id) {
        const q = askedLine.exec(n.body)
        if (q) questions[chat] = { run: q[1], choices: q[2].trim().split(" ").map(decodeURIComponent), at: now }
        const c = closedLine.exec(n.body)
        if (c && questions[chat] && questions[chat].run === c[1]) delete questions[chat]
      }
      // A comment that is only a choice, in any case, while a question waits
      // on the issue, is an answer to it.
      const q = questions[chat]
      const said = unnamed(n.body).replace(/[.!]+$/, "").toLowerCase()
      const choice = q && q.choices.find((c) => c.toLowerCase() === said)
      if (choice !== undefined && n.author.id !== bot.id) {
        if (quiet) continue
        say(Date.parse(n.created_at), "answer", Object.assign({ run: q.run }, person(n.author), { choice: choice }))
        continue
      }
      // A comment on an issue still waiting is read with it, as its history,
      // and the latest that names the bot is kept for when the issue is heard.
      if (wait) {
        if (names(n.body) && n.author.id !== bot.id) wait.note = n.id
        continue
      }
      comment(issue, n)
    }

    // Changes, from the resource events of each field that differs from the
    // issue as last read, or of every field of an issue not read before. A
    // burst of changes to one field is one message.
    //
    // UNVERIFIED: that adding or removing a label, or setting a milestone,
    // moves the issue's updated_at, so that the issue is listed at all. GitLab's
    // documentation does not say, and it is not yet checked on a GitLab.
    const was = issues[chat]
    const is = { labels: (issue.labels || []).slice().sort(), milestone: issue.milestone ? issue.milestone.id : null, state: issue.state }
    issues[chat] = Object.assign({ at: now }, is)
    if (opened) continue
    if (!was || JSON.stringify(was.labels) !== JSON.stringify(is.labels)) {
      const e = events(issue, "label", was)
      if (e.length) change(issue, Date.parse(e[e.length - 1].created_at), e[e.length - 1].user, "labels", "labels " + e.map((x) => (x.action === "add" ? "+" : "-") + (x.label ? x.label.name : "?")).join(" "))
    }
    if (!was || was.milestone !== is.milestone) {
      const e = events(issue, "milestone", was)
      const last = e[e.length - 1]
      if (last) change(issue, Date.parse(last.created_at), last.user, "milestone", "milestone " + (last.action === "add" ? "+" : "-") + (last.milestone ? last.milestone.title : "?"))
    }
    if (!was || was.state !== is.state) {
      const e = events(issue, "state", was).filter((x) => x.state === "closed" || x.state === "reopened")
      const last = e[e.length - 1]
      if (last) change(issue, Date.parse(last.created_at), last.user, last.state, last.state)
    }
    if (assigned) {
      const assignees = (issue.assignees || []).map((a) => "@" + a.username)
      change(issue, Date.parse(assigned.created_at), assigned.author, "assignees", "assignees " + (assignees.length ? assignees.join(" ") : "none"))
    }
  }

  // An opened issue is heard once nobody has changed it for settle, as it is
  // then. When it does not name the bot and a comment made while it waited
  // does, that comment is heard in its place, with the issue as its history.
  const still = []
  for (const w of waiting) {
    const { project, iid } = chatOf(w.chat)
    const listed = changed.get(w.chat)
    const issue = listed || Object.assign({ project: project }, call("GET", issuePath(project, iid), { maybe: true }))
    if (!issue.iid) continue
    // GitLab's Date is to the second, so an issue changed in that second has
    // waited none.
    if (now + 1000 - Date.parse(issue.updated_at) <= settle) {
      still.push(w)
      continue
    }
    const r = notesOf(project, iid)
    while (w.note && !r.end && !r.list.some((n) => n.id === w.note)) r.more()
    const note = r.list.find((n) => n.id === w.note)
    if (note && !names(issue.title + "\n\n" + (issue.description || ""))) comment(issue, note)
    else comment(issue, null)
  }

  // GitLab says how many more asks it takes before it refuses: when few are
  // left, the next ask waits until it takes more.
  let rest = 30
  const l = call.limit
  if (l.remaining !== undefined && l.remaining < Math.max(10, l.limit / 10)) {
    rest = Math.min(Math.max(Math.ceil(l.reset - now / 1000), 30), 3600)
    log("GitLab takes " + l.remaining + " more asks until " + new Date(l.reset * 1000).toISOString() + "; asking again in " + rest + "s")
  }

  // Said only now, when nothing is left to fail: a job that fails keeps the old
  // cursor, and its next ask hears the same again.
  heard.sort((a, b) => a.at - b.at)
  for (const h of heard) emit(h.event, h.data)
  const cursor = {
    since: now, start: before.start, iids: Object.fromEntries(Object.entries(iids).slice(-500)), seen: seenNext,
    issues: newest(issues, 300), people: newest(people, 200), waiting: still.slice(-100), questions: newest(questions, 100),
  }
  return { cursor: cursor, rest: rest + "s" }
}
