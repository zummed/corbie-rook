// Slack's script: input.do is hear, for listen.yaml, say, for say.yaml, ask, for
// ask.yaml, or history, for history.yaml. It speaks Slack's Web API with the bot token SLACK_TOKEN and
// listens with the app-level token SLACK_APP_TOKEN, holding each as a handle.
const api = "https://slack.com/api"
const listenFor = 4 * 60 * 1000
const longest = 12000

// call calls a method and gives Slack's answer; a refusal is thrown with Slack's
// word, so the job fails and says why.
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
  if (!answer.ok) throw new Error("Slack refused " + method + ": " + answer.error)
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

  // said emits a message, if it is said to the bot: in a direct message, or
  // naming it in a channel. What a bot says is left out, the bot's own included.
  const said = (e) => {
    if (e.type !== "message" || (e.subtype && e.subtype !== "file_share" && e.subtype !== "thread_broadcast")) return
    if (e.bot_id || !e.user || e.user === me) return
    const text = e.text || ""
    const direct = e.channel_type === "im"
    if (!text || (!direct && !text.includes(mention))) return
    const place = direct ? "dm" : "#" + call("conversations.info", null, { channel: e.channel }).channel.name
    const chat = e.channel + (direct && !e.thread_ts ? "" : "/" + (e.thread_ts || e.ts))
    emit("message", { bridge: "slack", text: text.replaceAll(mention, "").trim(), chat: chat, from: e.user, place: place, at: e.ts })
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
  return require("../../blocks/thread.js").text(said.map((m) => name(m) + ": " + m.text))
}

// say: the text in parts Slack takes, cut at a line's end where one is near.
const [channel, thread] = String(input.to).split("/")
let text = String(input.text || "")
if (!text) throw new Error("there is nothing to say")
const sent = []
while (text.length) {
  let at = text.length <= longest ? text.length : text.lastIndexOf("\n", longest - 1) + 1
  if (at < longest / 2) at = Math.min(text.length, longest)
  const message = { channel: channel, markdown_text: text.slice(0, at) }
  if (thread) message.thread_ts = thread
  sent.push(channel + ":" + call("chat.postMessage", message).ts)
  text = text.slice(at)
}
return { sent: sent }
