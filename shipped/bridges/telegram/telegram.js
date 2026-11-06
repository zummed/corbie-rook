// The bot's script: input.do is hear, for listen.yaml, say, for say.yaml,
// ask, for ask.yaml, close, for close.yaml, status, for status.yaml,
// attachments, for attachments.yaml, or send, for send.yaml. It speaks
// Telegram's Bot API with the token of the secret input.token names, which it
// holds only as a handle.
const api = "https://api.telegram.org"
const token = secrets.get(input.token)

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
  // a person's own chat with the bot is emitted as message; a tap on a
  // button of a question is emitted as answer.
  const ask = { timeout: 50, limit: 1, allowed_updates: ["message", "callback_query"] }
  if (input.cursor !== null && input.cursor !== undefined) ask.offset = input.cursor
  let cursor = ask.offset === undefined ? null : ask.offset
  for (const u of call("getUpdates", ask)) {
    cursor = u.update_id + 1
    const q = u.callback_query
    if (q && q.message && q.data) {
      const at = q.data.indexOf(" ")
      const question = q.data.slice(0, at), choice = q.data.slice(at + 1)
      // Telegram passes on whatever data a client sends, so a tap that names
      // no question is logged and starts nothing.
      if (at > 0 && /^\d{8}T\d{6}\.\d{3}Z-[0-9a-f]{10}$/.test(question)) emit("answer", { bridge: "telegram", question: question, from: String(q.from.id), choice: choice })
      else log("a tap names no question: " + q.data.slice(0, 100))
      // This only shows the tap. Telegram refuses it for an old tap, and a
      // refusal must not stop the cursor passing this update.
      try {
        call("answerCallbackQuery", { callback_query_id: q.id, text: choice })
      } catch (e) {
        log(e.message)
      }
      continue
    }
    const m = u.message
    if (!m || !m.from || m.from.is_bot || m.chat.type !== "private") continue
    const text = m.text || m.caption || ""
    const file = attachment(m)
    if (!text && !file) continue
    // Each item of an album comes as its own update, so it is a message of
    // its own, with its own caption.
    const message = { bridge: "telegram", text: text, chat: String(m.chat.id), from: String(m.from.id), place: "dm",
      answers: m.chat.id + ":" + m.message_id }
    if (file) message.attachments = [file]
    emit("message", message)
  }
  return { cursor: cursor }
}

if (input.do === "attachments") {
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
  const m = call("sendDocument", { chat_id: String(input.to), document: { data: input.file, name: input.name } }, true)
  return { sent: input.to + ":" + m.message_id }
}

if (input.do === "ask") {
  // A question with a button for each choice. A button holds the question
  // and the choice, which a tap names; Telegram takes at most 64 bytes.
  const buttons = input.choices.map((c) => ({ text: c[0].toUpperCase() + c.slice(1), callback_data: input.question + " " + c }))
  const m = call("sendMessage", { chat_id: input.to, text: input.text, reply_markup: { inline_keyboard: [buttons] } })
  return { sent: [{ chat: input.to, id: m.message_id }] }
}

if (input.do === "close") {
  // Each message a question posted shows input.text, with no buttons.
  for (const m of input.sent) call("editMessageText", { chat_id: m.chat, message_id: m.id, text: input.text })
  return {}
}

if (input.do === "status") {
  // Telegram shows "typing" for five seconds, or until the bot says something.
  // It has no text of its own, and nothing clears it.
  if (input.text) call("sendChatAction", { chat_id: input.to, action: "typing" })
  return {}
}

// say: the text in parts Telegram takes, of at most 4096 characters, cut
// at a line's end where one is near, the first in reply to what it answers.
// Each chart is shown as its table.
if (!input.text) throw new Error("there is nothing to say")
let text = require("../../blocks/rich.js").plain(String(input.text))
const sent = []
while (text.length) {
  let at = text.length <= 4096 ? text.length : text.lastIndexOf("\n", 4095) + 1
  if (at < 2048) at = Math.min(text.length, 4096)
  const message = { chat_id: input.to, text: text.slice(0, at) }
  if (!sent.length && input.answers) {
    const [chat, id] = String(input.answers).split(":")
    if (chat === String(input.to) && id) message.reply_parameters = { message_id: Number(id), allow_sending_without_reply: true }
  }
  const result = call("sendMessage", message)
  sent.push(input.to + ":" + result.message_id)
  text = text.slice(at)
}
return { sent: sent }
