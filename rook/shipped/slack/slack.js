// Slack's script: input.do names what it does, for the machine that runs it:
//
//   hear      listen.yaml
//   post, cut chat/post.yaml
//   ask       chat/show.yaml, which posts the question
//   tell      chat/show.yaml, which tells one person why their press does not count
//   close     chat/show.yaml, which shows the answer in place of the question
//   status    assistant/status.yaml
//   download  files/get.yaml
//   upload    files/upload.yaml
//   replies   conversations/replies.yaml
//
// It speaks Slack's Web API with the bot token SLACK_TOKEN and listens with the
// app-level token SLACK_APP_TOKEN, holding each as a handle. Where it posts,
// `in` is a conversation's ID, a channel's #name or a person's member ID, each
// with /thread for a thread.
const api = "https://slack.com/api"
const listenFor = 4 * 60 * 1000
const longest = 12000
// question is the form of a question's run ID, which a button holds.
const question = /^[\w.-]+\/\d{8}T\d{6}\.\d{3}Z-[0-9a-f]{10}$/
// A message's history holds at most 20 entries and 8000 characters.
const mostLines = 20
const mostChars = 8000
// A person's name and email are kept for a day, for at most 500 people.
const day = 24 * 60 * 60 * 1000
const mostPeople = 500

// call calls a method and gives Slack's answer. A refusal is thrown with Slack's
// word, which the error carries as slack, so the job fails and says why.
function call(method, body, query, token) {
  const options = { headers: { Authorization: secrets.text`Bearer ${secrets.get(token || "SLACK_TOKEN")}` }, as: "text", timeout: 30 }
  if (body) options.json = body
  if (query) options.query = query
  const reply = body ? http.post(api + "/" + method, options) : http.get(api + "/" + method, options)
  if (reply.status === 429) throw new Error("Slack answered " + method + " with 429, too many calls")
  let answer
  try {
    answer = JSON.parse(reply.body)
  } catch (e) {
    throw new Error("Slack answered " + method + " with status " + reply.status + " and no answer it could read")
  }
  if (!answer.ok) {
    const e = new Error("Slack refused " + method + ": " + answer.error)
    e.slack = answer.error
    throw e
  }
  return answer
}

// named gives the ID of the conversation `to` names: the channel a #name names,
// of those the bot can see; the bot's direct message with the person a member
// ID names; and a conversation's ID as it is.
function named(to) {
  if (/^[UW][A-Z0-9]+$/.test(to)) return call("conversations.open", { users: to }).channel.id
  if (!to.startsWith("#")) return to
  let cursor
  for (let page = 0; page < 20; page++) {
    const part = call("conversations.list", null, Object.assign({ types: "public_channel,private_channel", exclude_archived: "true", limit: "200" }, cursor ? { cursor: cursor } : {}))
    const found = (part.channels || []).find((c) => c.name === to.slice(1))
    if (found) return found.id
    cursor = (part.response_metadata || {}).next_cursor
    if (!cursor) break
  }
  throw new Error("Slack has no channel " + to + " the bot can see")
}

// place gives the conversation and thread `in` names, as { channel, thread }.
function place(where) {
  const [to, thread] = String(where).split("/")
  return { channel: named(to), thread: thread }
}

// described gives the files of a message Slack keeps, as a message's
// attachments give them. A hidden, deleted or external file has no address
// there, and is left out.
const described = (files) => (files || []).filter((f) => String(f.url_private_download || "").startsWith("https://files.slack.com/"))
  .map((f) => ({ name: f.name || f.title || f.id, type: f.mimetype || "application/octet-stream", size: f.size, id: f.url_private_download }))

// said gives what was said in a conversation before the message at `before`,
// oldest first, and whether that is all of it: in a thread, its replies, up
// to a thousand, and in a direct message outside a thread, its latest
// `latest`. Slack gives a thread oldest first, in pages, so the latest of a
// long one are on its last page.
function said(channel, thread, before, latest) {
  const asked = { channel: channel, latest: String(before), inclusive: "false", limit: thread ? "200" : String(latest) }
  let found = []
  let cursor
  for (let page = 0; page < 5; page++) {
    const part = thread
      ? call("conversations.replies", null, Object.assign({ ts: thread }, asked, cursor ? { cursor: cursor } : {}))
      : call("conversations.history", null, asked)
    found = found.concat(part.messages || [])
    cursor = (part.response_metadata || {}).next_cursor || (part.has_more ? "more" : "")
    if (!cursor || !thread) break
  }
  const earlier = found.filter((m) => (m.text || described(m.files).length) && Number(m.ts) < Number(before))
  earlier.sort((a, b) => Number(a.ts) - Number(b.ts))
  return { messages: earlier, complete: !cursor }
}

// lines gives what said gives as the entries thread.js takes: each person by
// the name `who` gives, the bot as "you", and another bot by its ID.
function lines(messages, me, who) {
  const you = new RegExp("<@" + me + ">", "g")
  const name = (m) => (m.user === me ? "you" : m.user && !m.bot_id ? who(m.user) : "<@" + (m.bot_id || m.user || "someone") + ">")
  return messages.map((m) => ({ line: name(m) + ": " + String(m.text || "").replace(you, "you"), attachments: described(m.files) }))
}

if (input.do === "hear") {
  const me = call("auth.test").user_id
  const mention = "<@" + me + ">"

  // people holds each person's name and email by member ID, as users.info gave
  // them, with when it did, from this job and the ones before it. A person
  // Slack refuses to give, such as one from another organisation, has no
  // email and their member ID as their name, and is asked again next time.
  const people = new Map(Object.entries((input.cursor || {}).people || {}).filter(([, p]) => p.at > Date.now() - day))
  const person = (id) => {
    if (!people.has(id)) {
      let u
      try {
        u = call("users.info", null, { user: id }).user
      } catch (e) {
        if (!e.slack) throw e
        log("Slack gives no name for " + id + ": " + e.slack)
        return { name: id, email: "" }
      }
      const profile = u.profile || {}
      people.set(id, { name: profile.real_name || u.real_name || profile.display_name || u.name || id, email: profile.email || "", at: Date.now() })
    }
    const p = people.get(id)
    return { name: p.name, email: p.email }
  }

  // before gives the history and complete of a message: what its thread held
  // before the time `ts`, as a blob of a list of { line, attachments }, oldest
  // first, its latest 20 entries and 8000 characters at most, and whether that
  // is the whole of it. A message outside a thread in a channel starts its
  // thread, so nothing came before it.
  const before = (channel, threadTs, ts, direct) => {
    if (!threadTs && !direct) return { history: blobs.make([]), complete: true }
    const found = said(channel, threadTs, ts, 30)
    const entries = lines(found.messages, me, (id) => person(id).name)
    let kept = 0
    let size = 0
    while (kept < Math.min(entries.length, mostLines) && size + entries[entries.length - 1 - kept].line.length <= mostChars) {
      size += entries[entries.length - 1 - kept].line.length
      kept++
    }
    return { history: blobs.make(entries.slice(entries.length - kept)), complete: found.complete && kept === entries.length }
  }

  // placeOf gives a message's place: dm, or the channel's #name.
  const placeOf = (channel, direct) => (direct ? "dm" : "#" + call("conversations.info", null, { channel: channel }).channel.name)

  // pressed gives the answer a press of a question's button is. A button's value
  // holds the question and the choice. The question's run takes the buttons
  // away once a press counts. A press of another button, such as one of a
  // layout an agent posted, starts nothing, and one whose value names no
  // question is logged.
  const pressed = (body) => {
    const action = (body.actions || [])[0] || {}
    if (!String(action.action_id || "").startsWith("choice-")) return
    const value = String(action.value || "")
    const at = value.indexOf(" ")
    if (at < 0 || !question.test(value.slice(0, at))) return log("a press names no question: " + value.slice(0, 100))
    if (!body.user || !body.user.id) return log("a press names no person")
    emit("answer", Object.assign({ service: "slack", run: value.slice(0, at), user: body.user.id, choice: value.slice(at + 1) }, person(body.user.id)))
  }

  // heard holds the messages emitted in the last ten minutes, as channel/ts,
  // from this job and the ones before it: Slack sends an envelope again when
  // its receipt is late or missing.
  const heard = ((input.cursor || {}).heard || []).slice()

  // spoke holds the threads the bot has said something in, as channel/thread:
  // what it was seen to say, and what a read of the thread found.
  const spoke = new Map()
  const spoken = (channel, threadTs) => {
    const at = channel + "/" + threadTs
    if (!spoke.has(at)) {
      const replies = call("conversations.replies", null, { channel: channel, ts: threadTs, limit: "200" }).messages || []
      spoke.set(at, replies.some((m) => m.user === me))
    }
    return spoke.get(at)
  }

  // message emits a message said to the bot: in a direct message, or naming it
  // in a channel. With input.overhear, a channel's message outside a thread,
  // and a reply in a thread the bot has said something in, are emitted too,
  // marked unaddressed. What a bot says is left out, the bot's own included.
  const message = (e) => {
    if (e.type !== "message" || (e.subtype && e.subtype !== "file_share" && e.subtype !== "thread_broadcast")) return
    if (!e.channel || !e.ts) return log("a message names no channel or no time")
    if (heard.includes(e.channel + "/" + e.ts)) return
    if (e.user === me) spoke.set(e.channel + "/" + (e.thread_ts || e.ts), true)
    if (e.bot_id || !e.user || e.user === me) return
    const text = e.text || ""
    const direct = e.channel_type === "im"
    const files = described(e.files)
    if (!text && !files.length) return
    let unaddressed = false
    if (!direct && !text.includes(mention)) {
      if (!input.overhear || e.thread_ts && !spoken(e.channel, e.thread_ts)) return
      unaddressed = true
    }
    const chat = e.channel + (direct && !e.thread_ts ? "" : "/" + (e.thread_ts || e.ts))
    const m = Object.assign({ service: "slack", place: placeOf(e.channel, direct), chat: chat, user: e.user }, person(e.user),
      { text: text.replaceAll(mention, "").trim(), at: e.ts }, before(e.channel, e.thread_ts, e.ts, direct))
    if (files.length) m.attachments = files
    if (unaddressed) m.unaddressed = true
    heard.push(e.channel + "/" + e.ts)
    emit("message", m)
  }

  // reacted emits an emoji a person added to a message, as a message with
  // change: reaction in the chat of the message reacted to, whose text is the
  // emoji, such as :eyes:, and whose at is the message reacted to. Its history
  // is what that chat held before the reaction, the message reacted to
  // included.
  const reacted = (e) => {
    if (e.type !== "reaction_added") return
    const item = e.item || {}
    if (item.type !== "message" || !item.channel || !item.ts || !e.user || !e.reaction || !e.event_ts) return log("a reaction names no message, person or emoji")
    if (e.user === me || heard.includes(item.channel + "/" + e.event_ts)) return
    const info = call("conversations.info", null, { channel: item.channel }).channel
    // The message reacted to names its thread in thread_ts: a reply its parent,
    // and a parent itself. A message with no replies names none, and is outside
    // a thread, which in a channel it starts.
    const target = (call("conversations.replies", null, { channel: item.channel, ts: item.ts, limit: "1" }).messages || [])[0] || {}
    const threadTs = target.thread_ts && target.thread_ts !== item.ts ? target.thread_ts : info.is_im ? undefined : item.ts
    heard.push(item.channel + "/" + e.event_ts)
    emit("message", Object.assign({ service: "slack", place: info.is_im ? "dm" : "#" + info.name, chat: item.channel + (threadTs ? "/" + threadTs : ""), user: e.user }, person(e.user),
      { text: ":" + e.reaction + ":", at: item.ts, change: "reaction" }, before(item.channel, threadTs, e.event_ts, info.is_im)))
  }

  // stopped emits a press of the stop button Slack shows under a thread while
  // the bot works there, as stop to that thread's chat. Slack sends
  // agent_session_stopped to an app that subscribes to it.
  const stopped = (e) => {
    if (e.type !== "agent_session_stopped") return
    if (!e.channel || !e.thread_ts || !e.user || !e.event_ts) return log("a stop names no channel, thread, person or time")
    if (heard.includes(e.channel + "/" + e.event_ts)) return
    const channel = call("conversations.info", null, { channel: e.channel }).channel
    heard.push(e.channel + "/" + e.event_ts)
    emit("stop", Object.assign({ service: "slack", place: channel.is_im ? "dm" : "#" + channel.name, chat: e.channel + "/" + e.thread_ts, user: e.user }, person(e.user)))
  }

  // Slack gives the app-level token the address of one connection, and sends
  // each event in an envelope, again if it is not told the envelope was received.
  const link = call("apps.connections.open", {}, null, "SLACK_APP_TOKEN")
  const socket = websocket.connect(link.url, { timeout: 30 })
  const until = Date.now() + listenFor
  for (let left = listenFor; left > 0; left = until - Date.now()) {
    let received
    try {
      received = socket.receive({ timeout: Math.ceil(left / 1000) })
    } catch (e) {
      if (e.code === "timeout") break
      throw e
    }
    if (received === null) break
    let envelope
    try {
      envelope = JSON.parse(received)
    } catch (e) {
      log("Slack sent what is not JSON: " + received.slice(0, 100))
      continue
    }
    if (envelope.type === "disconnect") break
    if (!envelope.envelope_id) continue
    // An envelope the listener handled or skipped is acknowledged. Any failure,
    // such as a network error or Slack refusing a call, is logged and leaves
    // the envelope unacknowledged, so Slack sends it again, a few times at most.
    const body = envelope.payload || {}
    try {
      if (envelope.type === "interactive" && body.type === "block_actions") pressed(body)
      if (envelope.type === "events_api" && body.event) {
        message(body.event)
        reacted(body.event)
        stopped(body.event)
      }
    } catch (e) {
      log(e.message)
      continue
    }
    socket.send(JSON.stringify({ envelope_id: envelope.envelope_id }))
  }
  socket.close()
  const ts = (h) => Number(h.slice(h.indexOf("/") + 1))
  const latest = Math.max(0, ...heard.map(ts))
  const kept = [...people.entries()].sort((a, b) => b[1].at - a[1].at).slice(0, mostPeople)
  return { cursor: { heard: heard.filter((h) => ts(h) > latest - 600), people: Object.fromEntries(kept) } }
}

if (input.do === "download") {
  // Each file's contents, downloaded from its address with the bot token, which
  // needs the scope files:read. A file over input.most bytes is left as it is.
  return input.attachments.map((a) => {
    if (a.size > input.most) return a
    const reply = http.get(a.id, { headers: { Authorization: secrets.text`Bearer ${secrets.get("SLACK_TOKEN")}` }, as: "bytes", timeout: 120 })
    // Slack answers a token that may not read the file with its sign-in page.
    const type = reply.headers["content-type"] || ""
    if (reply.status !== 200 || (/^text\/html/.test(type) && !/^text\/html/.test(a.type))) throw new Error("Slack answered the download of " + a.name + " with status " + reply.status + " and " + (type || "no type"))
    return Object.assign({}, a, { file: blobs.make(reply.body, { type: a.type }) })
  })
}

if (input.do === "upload") {
  // A file in a conversation or its thread, which needs the scope files:write.
  // Slack gives an address on files.slack.com that takes the file's bytes with
  // no token, then shares the file where it is told.
  const where = place(input.in)
  const at = call("files.getUploadURLExternal", null, { filename: input.name, length: String(input.file.size) })
  const reply = http.post(at.upload_url, { body: input.file, timeout: 240 })
  if (reply.status !== 200) throw new Error("Slack answered the upload of " + input.name + " with status " + reply.status)
  call("files.completeUploadExternal", Object.assign({ files: [{ id: at.file_id, title: input.name }], channel_id: where.channel }, where.thread ? { thread_ts: where.thread } : {}))
  return { sent: where.channel + ":" + at.file_id }
}

if (input.do === "ask") {
  // A question, with a button for each choice, in input.in: a channel's #name
  // or a conversation, either with /thread. A button's value holds the
  // question and the choice, which a press names.
  const label = (s) => ({ type: "plain_text", text: String(s).slice(0, 75) })
  const buttons = input.choices.map((c, n) => ({ type: "button", action_id: "choice-" + n, text: label(c[0].toUpperCase() + c.slice(1)), value: input.question + " " + c }))
  const blocks = [{ type: "section", text: { type: "mrkdwn", text: input.text } }, { type: "actions", elements: buttons }]
  const where = place(input.in)
  const posted = call("chat.postMessage", Object.assign({ channel: where.channel, text: input.text, blocks: blocks }, where.thread ? { thread_ts: where.thread } : {}))
  return { sent: [Object.assign({ channel: where.channel, ts: posted.ts }, where.thread ? { thread: where.thread } : {})], where: "Slack" }
}

if (input.do === "tell") {
  // input.text, shown only to the person who pressed a button of a question,
  // input.answer.user, under each message the question posted.
  for (const m of input.sent) call("chat.postEphemeral", Object.assign({ channel: m.channel, user: input.answer.user, text: input.text }, m.thread ? { thread_ts: m.thread } : {}))
  return {}
}

if (input.do === "close") {
  // Each message a question posted shows its text, input.text, and then
  // input.said, with no buttons.
  const text = input.text + "\n\n" + input.said
  for (const m of input.sent) call("chat.update", { channel: m.channel, ts: m.ts, text: text, blocks: [] })
  return {}
}

if (input.do === "replies") {
  // What a thread held before a message, or a conversation's latest before
  // it, as a blob of text, each message with the files it names.
  const me = call("auth.test").user_id
  const where = place(input.in)
  const found = said(where.channel, where.thread, input.at || String(Date.now() / 1000), 200)
  const names = new Map()
  const who = (id) => {
    if (!names.has(id)) {
      try {
        const u = call("users.info", null, { user: id }).user
        names.set(id, (u.profile || {}).real_name || u.real_name || u.name || id)
      } catch (e) {
        if (!e.slack) throw e
        names.set(id, "<@" + id + ">")
      }
    }
    return names.get(id)
  }
  const text = require("../shared/thread.js").text(lines(found.messages, me, who))
  return Object.assign(text ? { history: blobs.make(text) } : {}, { complete: found.complete })
}

if (input.do === "status") {
  // Any text sets the thread's agent session to processing, under which Slack
  // shows "Working..." and its stop button; an empty text sets it to active.
  // Slack keeps a session only in a thread.
  const [channel, threadTs] = String(input.in).split("/")
  if (!threadTs) return {}
  call("agents.sessions.setStatus", { channel_id: channel, thread_ts: threadTs, status: input.text ? "processing" : "active" })
  return {}
}

// inert gives a layout's blocks with no action_id at any depth, and no value
// in a button, url buttons included. Slack gives each element an action_id of
// its own, which never starts with choice-, so no press of a button a post
// holds is a question's answer.
const inert = (v) => Array.isArray(v) ? v.map(inert)
  : v && typeof v === "object"
    ? Object.fromEntries(Object.entries(v).filter(([k]) => k !== "action_id" && !(k === "value" && v.type === "button")).map(([k, x]) => [k, inert(x)]))
    : v

// post: one message of those cut gives, in input.in, its layout made inert. A
// message with charts or a layout that Slack refuses is posted again as its
// plain markdown.
if (input.do === "post") {
  const where = place(input.in)
  const channel = where.channel
  const m = input.message
  const post = Object.assign({ channel: channel }, where.thread ? { thread_ts: where.thread } : {})
  if (!m.blocks) return { sent: channel + ":" + call("chat.postMessage", Object.assign(post, { markdown_text: m.plain })).ts }
  try {
    return { sent: channel + ":" + call("chat.postMessage", Object.assign({}, post, { text: m.plain.slice(0, 3000), blocks: inert(m.blocks) })).ts }
  } catch (e) {
    if (!e.slack) throw e
    log(e.message)
    return { sent: channel + ":" + call("chat.postMessage", Object.assign(post, { markdown_text: m.plain })).ts }
  }
}

// cut: the text as the messages Slack takes, each { plain, blocks }, blocks only
// for one with charts or a layout. Markdown is cut at a line's end where one is
// near, and a message holds at most 12000 characters of it, two charts and 50
// blocks. A layout's blocks are kept as they are, and post makes them inert. A
// chart Slack cannot draw is shown as its table, and plain is the markdown with
// each layout as its text.
const rich = require("../shared/rich.js")
if (!input.text) throw new Error("there is nothing to say")
const short = (s, n) => s === undefined || String(s).length <= n
const fits = (c) => {
  const pie = c.type === "pie"
  const series = pie ? c.series.slice(0, 1) : c.series
  return short(c.title, 50) && short(c.x, 50) && short(c.y, 50) && c.labels.length <= (pie ? 12 : 20) &&
    c.labels.every((l) => short(l, 20)) && new Set(c.labels.map(String)).size === c.labels.length &&
    series.length <= 12 && series.every((s) => short(s.name, 20)) && new Set(series.map((s) => s.name)).size === series.length &&
    (!pie || series[0].values.every((v) => v > 0))
}
const drawn = (c) => {
  const labels = c.labels.map(String)
  const chart = c.type === "pie"
    ? { type: "pie", segments: labels.map((l, n) => ({ label: l, value: c.series[0].values[n] })) }
    : {
      type: c.type,
      series: c.series.map((s, n) => ({ name: s.name || (c.series.length > 1 ? "Series " + (n + 1) : c.y || "Value"), data: labels.map((l, i) => ({ label: l, value: s.values[i] })) })),
      axis_config: Object.assign({ categories: labels }, c.x ? { x_label: c.x } : {}, c.y ? { y_label: c.y } : {}),
    }
  return { type: "data_visualization", title: c.title || c.y || "Chart", chart: chart }
}
const cut = (text) => {
  const pieces = []
  while (text.length) {
    let at = text.length <= longest ? text.length : text.lastIndexOf("\n", longest - 1) + 1
    if (at < longest / 2) at = Math.min(text.length, longest)
    pieces.push(text.slice(0, at))
    text = text.slice(at)
  }
  return pieces
}
// Each message is a list of parts: { markdown }, { chart } or { blocks }.
const messages = [[]]
const last = () => messages[messages.length - 1]
// count gives a message's charts, its blocks, or its characters of markdown with
// the blank line between parts.
const count = (m, kind) => m.reduce((n, p) => n + (
  kind === "chart" ? (p.chart ? 1 : 0)
    : kind === "blocks" ? (p.blocks ? p.blocks.length : 1)
      : p.markdown === undefined ? 0 : p.markdown.length + 2), 0)
for (const p of rich.parts(String(input.text))) {
  if (p.blocks) {
    if (count(last(), "blocks") + p.blocks.length > 50) messages.push([])
    last().push({ blocks: p.blocks.slice(0, 50) })
    continue
  }
  if (p.chart && fits(p.chart)) {
    if (count(last(), "chart") === 2 || count(last(), "blocks") === 50) messages.push([])
    last().push(p)
    continue
  }
  for (const piece of cut(p.chart ? rich.table(p.chart) : p.markdown)) {
    if (count(last(), "markdown") + piece.length > longest || count(last(), "blocks") === 50) messages.push([])
    last().push({ markdown: piece })
  }
}
return messages.filter((m) => m.length).map((m) => {
  const plain = m.map((p) => (p.chart ? rich.table(p.chart) : p.blocks ? rich.words(p.blocks) : p.markdown)).filter((s) => s).join("\n\n") || "(A layout with no text.)"
  if (!m.some((p) => p.chart || p.blocks)) return { plain: plain }
  return { plain: plain, blocks: m.flatMap((p) => (p.chart ? [drawn(p.chart)] : p.blocks ? p.blocks : [{ type: "markdown", text: p.markdown }])) }
})
