// Telegram's script: input.do is hear, for listen.yaml, post, for
// chat/post.yaml, ask, answer and close, for chat/ask.yaml, status, for
// chat/status.yaml, get, for files/get.yaml, or send, for files/send.yaml. It
// speaks Telegram's Bot API with the token of the secret input.token names,
// which it holds only as a handle.
const api = "https://api.telegram.org"
const token = secrets.get(input.token)

// The history the listener keeps of each chat: at most 20 entries and 8000
// characters a chat, and at most 250,000 characters in all, so the listener's
// output stays under a quarter of the 4 MiB a job may give. A chat with no
// message for a day is dropped.
const mostEntries = 20
const mostChars = 8000
const mostKept = 250000
const quiet = 24 * 60 * 60

// call calls a method of the Bot API and gives its result, sending body as
// JSON, or with upload as a form that holds a file, which may take four minutes. Telegram's refusal is
// thrown with its words, so the job fails and says why. No error holds the
// address called, since it holds the token.
function call(method, body, upload) {
  const reply = http.post(secrets.text`${api}/bot${token}/${method}`, upload ? { upload: body, as: "text", timeout: 240 } : { json: body, as: "text", timeout: 70 })
  let answer
  try {
    answer = JSON.parse(reply.body)
  } catch (e) {
    throw new Error("Telegram answered " + method + " with status " + reply.status + " and no answer it could read")
  }
  if (!answer.ok) throw new Error("Telegram refused " + method + ": " + answer.error_code + " " + answer.description)
  return answer.result
}

// The file a message carries, as a message's attachments give it, or null. A
// photo comes in several sizes, and the largest is taken. A live photo also
// sets photo, and an animation also sets document, so each is taken once.
function attachment(m) {
  if (m.photo) {
    const p = m.photo.reduce((a, b) => (b.width * b.height > a.width * a.height ? b : a))
    return { name: "photo.jpg", type: "image/jpeg", size: p.file_size, id: p.file_id }
  }
  for (const kind of ["animation", "document", "audio", "voice", "video", "video_note"]) {
    const f = m[kind]
    if (f) return { name: f.file_name || kind, type: f.mime_type || "application/octet-stream", size: f.file_size, id: f.file_id }
  }
  return null
}

if (input.do === "hear") {
  // One update at a time, waiting up to fifty seconds for it. Asking from
  // the cursor tells Telegram every update before it was heard; with no
  // cursor, Telegram gives the oldest it has not been told of. A message in
  // a person's own chat with the bot is emitted as message, except /start and
  // /stop. /stop, or /stop@<bot>, with any words after it, is emitted as stop;
  // a tap on a button of a question is emitted as answer. A message in a group
  // is logged.
  //
  // An update that lacks what the listener reads is logged and passed over.
  // An update the listener fails on for any other reason is given again, by
  // asking from its own update_id, and after it has failed three times running
  // it is logged and passed over. input.failing names that update and how many
  // times it has failed. A failure of getUpdates fails the job, which asks
  // again from the same cursor. limit: 1 makes each job hear one update: a job
  // that ends with no output, such as at its timeout, has emitted at most the
  // update Telegram gives again, where a longer list would be given again whole.
  //
  // input.chats is the history of each chat, by its ID: { at, complete,
  // entries }, at being the time of its latest message in seconds, and each
  // entry { line, attachments } as shared/thread.js reads it. The Bot API
  // keeps no history a bot can read, so the listener keeps what it heard, and
  // a run that starts with no output has none. A chat's history is complete
  // when the listener heard the /start that began the chat and dropped no entry
  // since.
  const ask = { timeout: 50, limit: 1, allowed_updates: ["message", "callback_query"] }
  if (input.cursor !== null && input.cursor !== undefined) ask.offset = input.cursor
  let cursor = ask.offset === undefined ? null : ask.offset
  const chats = Object.assign({}, input.chats || {})
  // heard emits what one update holds, and gives the chat's history as it is
  // after the update, or null where the update changes none. Its emit is the
  // last thing that can fail, so an update it fails on was not emitted, and
  // giving it again emits it once.
  const heard = (u) => {
    const q = u.callback_query
    if (q) {
      if (!q.id || !q.from || !q.data) {
        log("a tap with no id, sender or data is passed over")
        return null
      }
      const at = q.data.indexOf(" ")
      const question = q.data.slice(0, at), choice = q.data.slice(at + 1)
      // Telegram passes on whatever data a client sends, so a tap that names
      // no question is logged, answered here so the client stops waiting, and
      // starts nothing. The question answers a tap that names it.
      if (at > 0 && /^\d{8}T\d{6}\.\d{3}Z-[0-9a-f]{10}$/.test(question)) {
        emit("answer", { service: "telegram", run: question, user: String(q.from.id), name: named(q.from), choice: choice, callback: q.id })
        return null
      }
      log("a tap names no question: " + q.data.slice(0, 100))
      // Telegram refuses this for an old tap, and a refusal must not stop the
      // cursor passing this update.
      try {
        call("answerCallbackQuery", { callback_query_id: q.id })
      } catch (e) {
        log(e.message)
      }
      return null
    }
    const m = u.message
    if (!m || (m.from && m.from.is_bot)) return null
    if (!m.from || !m.chat) {
      log("a message with no sender or chat is passed over")
      return null
    }
    if (m.chat.type !== "private") {
      log("a message in a " + m.chat.type + " is not heard: the Telegram listener hears only direct messages")
      return null
    }
    const chat = String(m.chat.id)
    const text = m.text || m.caption || ""
    const kept = chats[chat] && chats[chat].at > m.date - quiet ? chats[chat] : null
    // Telegram sends /start when a person opens the bot, before anything else
    // in the chat.
    if (text === "/start" || text.startsWith("/start ")) {
      log("a /start is not passed on")
      return kept ? null : { at: m.date, complete: true, entries: [] }
    }
    const person = { service: "telegram", chat: chat, user: String(m.from.id), name: named(m.from), place: "dm", answers: chat + ":" + m.message_id }
    if (/^\/stop(@\S+)?(\s|$)/.test(text)) {
      emit("stop", person)
      return null
    }
    const file = attachment(m)
    if (!text && !file) return null
    // Each item of an album comes as its own update, so it is a message of
    // its own, with its own caption.
    const before = kept || { at: m.date, complete: false, entries: [] }
    const message = Object.assign({ text: text, history: blobs.make(before.entries), complete: before.complete }, person)
    if (file) message.attachments = [file]
    emit("message", message)
    return added(before, { line: person.name + ": " + (text || "(" + file.name + ")"), attachments: file ? [file] : [] }, m.date)
  }
  // record keeps a chat's history after an update, dropping each chat quiet
  // for a day before it and, past the bound in all, the chats quiet longest.
  const record = (chat, history, now) => {
    if (!history) return
    for (const id of Object.keys(chats)) if (chats[id].at <= now - quiet) delete chats[id]
    chats[chat] = history
    while (JSON.stringify(chats).length > mostKept) {
      const oldest = Object.keys(chats).reduce((a, b) => (chats[b].at < chats[a].at ? b : a))
      delete chats[oldest]
    }
  }
  for (const u of call("getUpdates", ask)) {
    try {
      const history = heard(u)
      if (u.message && u.message.chat) record(String(u.message.chat.id), history, u.message.date)
    } catch (e) {
      const tries = input.failing && input.failing.update === u.update_id ? input.failing.tries + 1 : 1
      if (tries < 3) {
        log("an update failed, and is given again: " + e.message)
        return { cursor: u.update_id, failing: { update: u.update_id, tries: tries }, chats: chats }
      }
      log("an update failed three times and is passed over: " + e.message)
    }
    cursor = u.update_id + 1
  }
  return { cursor: cursor, chats: chats }
}

// named gives a person's name as Telegram shows it, or their ID with none.
function named(from) {
  return [from.first_name, from.last_name].filter((n) => n).join(" ") || String(from.id)
}

// added gives a chat's history with one entry more, its oldest entries dropped
// past the bound, which leaves it no longer complete.
function added(history, entry, at) {
  const entries = history.entries.concat([entry])
  let complete = history.complete
  while (entries.length > mostEntries || (entries.length > 1 && JSON.stringify(entries).length > mostChars)) {
    entries.shift()
    complete = false
  }
  return { at: at, complete: complete, entries: entries }
}

if (input.do === "get") {
  // Each file's contents: getFile gives its path, downloaded with the token in
  // the address. A file over input.most bytes is left as it is.
  return input.attachments.map((a) => {
    if (a.size > input.most) return a
    const path = call("getFile", { file_id: a.id }).file_path
    const reply = http.get(secrets.text`${api}/file/bot${token}/${path}`, { as: "bytes", timeout: 120 })
    if (reply.status !== 200) throw new Error("Telegram answered the download of " + a.name + " with status " + reply.status)
    return Object.assign({}, a, { file: blobs.make(reply.body, { type: a.type }) })
  })
}

if (input.do === "send") {
  // A file in the chat, as a document, which Telegram shows an image of. A
  // photo is not used: Telegram refuses one past 10 MB or of an odd shape.
  const m = call("sendDocument", { chat_id: String(input.in), document: { data: input.file, name: input.name } }, true)
  return { sent: input.in + ":" + m.message_id }
}

if (input.do === "ask") {
  // A question with a button for each choice. A button holds the question
  // and the choice, which a tap names; Telegram takes at most 64 bytes.
  const buttons = input.choices.map((c) => ({ text: c[0].toUpperCase() + c.slice(1), callback_data: input.question + " " + c }))
  const m = call("sendMessage", { chat_id: input.in, text: input.text, reply_markup: { inline_keyboard: [buttons] } })
  return { sent: [{ chat: input.in, id: m.message_id }] }
}

if (input.do === "answer") {
  // A tap's answer: Telegram shows the presser input.text, with input.alert
  // in a box they must close. Telegram refuses this for an old tap, which
  // changes nothing.
  try {
    call("answerCallbackQuery", { callback_query_id: input.callback, text: input.text, show_alert: input.alert })
  } catch (e) {
    log(e.message)
  }
  return {}
}

if (input.do === "close") {
  // Each message a question posted shows input.text, with no buttons.
  for (const m of input.sent) call("editMessageText", { chat_id: m.chat, message_id: m.id, text: input.text })
  return {}
}

if (input.do === "status") {
  // Telegram shows "typing" for five seconds, or until the bot says something.
  // It has no text of its own, and nothing clears it.
  if (input.text) call("sendChatAction", { chat_id: input.in, action: "typing" })
  return {}
}

// post: the text in parts Telegram takes, of at most 4096 characters, cut
// at a line's end where one is near, the first in reply to what it answers.
// Each chart is shown as its table.
if (!input.text) throw new Error("there is nothing to post")
let text = require("../shared/rich.js").plain(String(input.text))
const sent = []
while (text.length) {
  let at = text.length <= 4096 ? text.length : text.lastIndexOf("\n", 4095) + 1
  if (at < 2048) at = Math.min(text.length, 4096)
  const message = { chat_id: input.in, text: text.slice(0, at) }
  if (!sent.length && input.answers) {
    const [chat, id] = String(input.answers).split(":")
    if (chat === String(input.in) && id) message.reply_parameters = { message_id: Number(id), allow_sending_without_reply: true }
  }
  const result = call("sendMessage", message)
  sent.push(input.in + ":" + result.message_id)
  text = text.slice(at)
}
return { sent: sent }
