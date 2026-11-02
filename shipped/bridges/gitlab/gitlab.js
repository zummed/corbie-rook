// GitLab's script: input.do is hear, for listen.yaml, say, for say.yaml, or
// history, for history.yaml. It
// speaks the REST API of the GitLab at input.site with the token GITLAB_TOKEN,
// which it holds only as a handle.
const api = "https://" + input.site + "/api/v4"
const token = secrets.get("GITLAB_TOKEN")

// call calls GitLab and gives its answer; a refusal is thrown with GitLab's
// status, so the job fails and says why. No error holds the address called. A
// thing gone or out of the token's sight is null where options.maybe says so.
function call(method, path, options) {
  const { maybe, ...rest } = options || {}
  const reply = http.request(api + path, Object.assign({ method: method, headers: { "PRIVATE-TOKEN": token }, as: "text", timeout: 30 }, rest))
  if (maybe && (reply.status === 403 || reply.status === 404)) return null
  if (reply.status < 200 || reply.status >= 300) throw new Error("GitLab answered " + method + " " + path.split("?")[0] + " with " + reply.status)
  return JSON.parse(reply.body)
}

if (input.do === "hear") {
  const cursor = Object.assign({}, input.cursor || {})
  const heard = []
  const me = call("GET", "/user")
  const named = new RegExp("(^|[^\\w@])@" + me.username.replace(/[.\-]/g, "\\$&") + "(?![\\w.-])")
  for (const project of input.projects) {
    const id = "/projects/" + encodeURIComponent(project)
    if (cursor[project] === undefined) {
      const latest = call("GET", id + "/events", { query: { per_page: "1" } })
      cursor[project] = latest.length ? latest[0].id : 0
      continue
    }
    // Events come newest first: read back to the cursor, then take them in the
    // order they happened.
    const events = []
    for (let page = 1; page <= 10; page++) {
      const some = call("GET", id + "/events", { query: { per_page: "100", page: String(page) } })
      const newer = some.filter((e) => e.id > cursor[project])
      events.push(...newer)
      if (newer.length < some.length || some.length < 100) break
    }
    events.reverse()
    for (const e of events) {
      cursor[project] = Math.max(cursor[project], e.id)
      if (e.author_id === me.id) continue
      let iid, text, at
      // An answer is a public note, so what only members can read is not heard.
      if (e.note && e.note.noteable_type === "Issue" && !e.note.system && !e.note.internal && !e.note.confidential) {
        iid = e.note.noteable_iid
        text = e.note.body || ""
        at = e.note.id
      } else if (!e.note && e.target_type === "Issue" && e.action_name === "opened") {
        const issue = call("GET", id + "/issues/" + e.target_iid, { maybe: true }) || {}
        iid = e.target_iid
        text = (issue.title || "") + "\n\n" + (issue.description || "")
      }
      if (iid === undefined || !e.author_username || !named.test(text)) continue
      heard.push({ bridge: "gitlab", text: text.trim(), chat: project + "#" + iid, from: String(e.author_username), place: project, at: at, settings: { site: input.site } })
    }
  }
  // Said only now, when nothing is left to fail: a job that fails keeps the old
  // cursor, and its next ask hears the same again.
  for (const message of heard) emit("message", message)
  return { cursor: cursor }
}

if (input.do === "history") {
  // The issue and its comments before this one. A message that is the opening of
  // an issue has none, and holds the issue itself.
  if (!input.at) return null
  const to = String(input.to)
  const cut = to.lastIndexOf("#")
  const issue = "/projects/" + encodeURIComponent(to.slice(0, cut)) + "/issues/" + to.slice(cut + 1)
  const me = call("GET", "/user").username
  const name = (author) => (author.username === me ? "you" : author.username)
  const opened = call("GET", issue, { maybe: true })
  const notes = call("GET", issue + "/notes", { query: { sort: "desc", per_page: "30" }, maybe: true }) || []
  const lines = opened ? [name(opened.author || {}) + ": " + opened.title + "\n\n" + (opened.description || "")] : []
  // An internal note is left out: the token may read it, and whoever reads the
  // answer may not.
  for (const note of notes.reverse()) {
    if (note.system || note.internal || note.confidential || note.id >= input.at) continue
    lines.push(name(note.author || {}) + ": " + note.body)
  }
  return require("../../blocks/thread.js").text(lines)
}

// say: a comment on the issue.
const to = String(input.to)
const cut = to.lastIndexOf("#")
if (cut < 0) throw new Error("GitLab has no issue " + to)
if (!input.text) throw new Error("there is nothing to say")
const note = call("POST", "/projects/" + encodeURIComponent(to.slice(0, cut)) + "/issues/" + to.slice(cut + 1) + "/notes", { json: { body: String(input.text) } })
return { sent: "note:" + note.id }
