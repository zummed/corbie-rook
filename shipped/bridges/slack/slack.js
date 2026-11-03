// Slack's script: input.do is hear, for listen.yaml, say, for say.yaml, ask, for
// ask.yaml, history, for history.yaml, or status, for status.yaml. It speaks
// Slack's Web API with the bot token SLACK_TOKEN and listens with the app-level
// token SLACK_APP_TOKEN, holding each as a handle.
const api = "https://slack.com/api"
const listenFor = 4 * 60 * 1000
const longest = 12000

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

if (input.do === "hear") {
  const me = call("auth.test").user_id
  const mention = "<@" + me + ">"

  // pressed gives the answer a press of a button is. A button's value holds the
  // question and the choice, and the buttons go once one is pressed.
  const pressed = (body) => {
    const action = (body.actions || [])[0] || {}
    const value = String(action.value || "")
    const at = value.indexOf(" ")
    if (at < 0) return
    emit("answer", { bridge: "slack", question: value.slice(0, at), from: body.user.id, choice: value.slice(at + 1) })
    const shown = body.message && body.message.text
    if (body.channel && body.message) {
      try {
        call("chat.update", { channel: body.channel.id, ts: body.message.ts, text: shown + "\n\nYou said " + value.slice(at + 1) + ".", blocks: [] })
      } catch (e) {
        log(e.message)
      }
    }
  }

  // spoke holds the threads the bot has said something in, as channel/thread:
  // what it was seen to say, and what a read of the thread found.
  const spoke = new Map()
  const spoken = (channel, thread) => {
    const at = channel + "/" + thread
    if (!spoke.has(at)) {
      const replies = call("conversations.replies", null, { channel: channel, ts: thread, limit: "200" }).messages || []
      spoke.set(at, replies.some((m) => m.user === me))
    }
    return spoke.get(at)
  }

  // said emits a message said to the bot: in a direct message, or naming it in a
  // channel. With input.overhear, a reply in a thread the bot has said something
  // in is emitted too, marked unaddressed. What a bot says is left out, the bot's
  // own included.
  const said = (e) => {
    if (e.type !== "message" || (e.subtype && e.subtype !== "file_share" && e.subtype !== "thread_broadcast")) return
    if (e.user === me) spoke.set(e.channel + "/" + (e.thread_ts || e.ts), true)
    if (e.bot_id || !e.user || e.user === me) return
    const text = e.text || ""
    const direct = e.channel_type === "im"
    if (!text) return
    let unaddressed = false
    if (!direct && !text.includes(mention)) {
      if (!input.overhear || !e.thread_ts) return
      try {
        if (!spoken(e.channel, e.thread_ts)) return
      } catch (err) {
        log(err.message)
        return
      }
      unaddressed = true
    }
    const place = direct ? "dm" : "#" + call("conversations.info", null, { channel: e.channel }).channel.name
    const chat = e.channel + (direct && !e.thread_ts ? "" : "/" + (e.thread_ts || e.ts))
    const message = { bridge: "slack", text: text.replaceAll(mention, "").trim(), chat: chat, from: e.user, place: place, at: e.ts }
    if (unaddressed) message.unaddressed = true
    emit("message", message)
  }

  // Slack gives the app-level token the address of one connection, and sends
  // each event in an envelope, again if it is not told the envelope was received.
  const link = call("apps.connections.open", {}, null, "SLACK_APP_TOKEN")
  const socket = websocket.connect(link.url, { timeout: 30 })
  const until = Date.now() + listenFor
  for (let left = listenFor; left > 0; left = until - Date.now()) {
    let message
    try {
      message = socket.receive({ timeout: Math.ceil(left / 1000) })
    } catch (e) {
      if (e.code === "timeout") break
      throw e
    }
    if (message === null) break
    const envelope = JSON.parse(message)
    if (envelope.type === "disconnect") break
    if (!envelope.envelope_id) continue
    const body = envelope.payload || {}
    if (envelope.type === "interactive" && body.type === "block_actions") pressed(body)
    if (envelope.type === "events_api" && body.event) said(body.event)
    socket.send(JSON.stringify({ envelope_id: envelope.envelope_id }))
  }
  socket.close()
  return {}
}

if (input.do === "ask") {
  // A question in a direct message, with a button for each choice. A button's
  // value holds the question and the choice, which a press names.
  const buttons = input.choices.map((c, n) => ({ type: "button", action_id: "choice-" + n, text: { type: "plain_text", text: c[0].toUpperCase() + c.slice(1) }, value: input.question + " " + c }))
  for (const to of input.to) {
    const channel = call("conversations.open", { users: to }).channel.id
    call("chat.postMessage", { channel: channel, text: input.text, blocks: [{ type: "section", text: { type: "mrkdwn", text: input.text } }, { type: "actions", elements: buttons }] })
  }
  return {}
}

if (input.do === "history") {
  // A thread's replies, or a direct message's latest. Only what came before the
  // message counts: it is what is being answered.
  const me = call("auth.test").user_id
  const [channel, thread] = String(input.to).split("/")
  const asked = { channel: channel, latest: String(input.at), inclusive: "false", limit: thread ? "200" : "30" }
  let found = []
  if (thread) {
    // Slack gives a thread oldest first, so the latest of a long one are on its
    // last page. A thread of more than a thousand replies stops short of them.
    let cursor
    for (let page = 0; page < 5; page++) {
      const part = call("conversations.replies", null, Object.assign({ ts: thread }, asked, cursor ? { cursor: cursor } : {}))
      found = found.concat(part.messages || [])
      cursor = (part.response_metadata || {}).next_cursor
      if (!cursor) break
    }
  } else {
    found = call("conversations.history", null, asked).messages || []
  }
  const said = found.filter((m) => m.text && Number(m.ts) < Number(input.at))
  said.sort((a, b) => Number(a.ts) - Number(b.ts))
  const name = (m) => (m.user === me ? "you" : "<@" + (m.user || m.bot_id || "someone") + ">")
  const you = new RegExp("<@" + me + ">", "g")
  return require("../../blocks/thread.js").text(said.map((m) => name(m) + ": " + m.text.replace(you, "you")))
}

if (input.do === "status") {
  // What the bot is doing, shown under a thread until it replies there or is
  // given an empty status. Slack shows a status only in a thread.
  const [channel, thread] = String(input.to).split("/")
  if (!thread) return {}
  call("assistant.threads.setStatus", { channel_id: channel, thread_ts: thread, status: String(input.text || "") })
  return {}
}

// say: the text in messages Slack takes. Markdown is cut at a line's end where
// one is near, and a message holds at most 12000 characters of it and two charts.
// A chart Slack cannot draw is shown as its table, and a message Slack refuses is
// posted again as plain markdown.
const rich = require("../../blocks/rich.js")
const [channel, thread] = String(input.to).split("/")
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
// Each message is a list of parts: { markdown } or { chart }.
const messages = [[]]
const last = () => messages[messages.length - 1]
const count = (m, kind) => m.reduce((n, p) => n + (kind === "chart" ? (p.chart ? 1 : 0) : (p.markdown || "").length), 0)
for (const p of rich.parts(String(input.text))) {
  if (p.chart && fits(p.chart)) {
    if (count(last(), "chart") === 2) messages.push([])
    last().push(p)
    continue
  }
  for (const piece of cut(p.chart ? rich.table(p.chart) : p.markdown)) {
    if (count(last(), "markdown") + piece.length > longest) messages.push([])
    last().push({ markdown: piece })
  }
}
const sent = []
for (const m of messages.filter((m) => m.length)) {
  const plain = m.map((p) => (p.chart ? rich.table(p.chart) : p.markdown)).join("\n\n")
  const post = { channel: channel }
  if (thread) post.thread_ts = thread
  if (!m.some((p) => p.chart)) {
    post.markdown_text = plain
    sent.push(channel + ":" + call("chat.postMessage", post).ts)
    continue
  }
  post.text = plain.slice(0, 3000)
  post.blocks = m.map((p) => (p.chart ? drawn(p.chart) : { type: "markdown", text: p.markdown }))
  let ts
  try {
    ts = call("chat.postMessage", post).ts
  } catch (e) {
    if (!e.slack) throw e
    log(e.message)
    ts = call("chat.postMessage", Object.assign({ channel: channel, markdown_text: plain }, thread ? { thread_ts: thread } : {})).ts
  }
  sent.push(channel + ":" + ts)
}
return { sent: sent }
