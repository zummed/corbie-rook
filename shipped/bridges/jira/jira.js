// Jira's script: input.do is hear, for listen.yaml, say, for say.yaml, or history,
// for history.yaml. It
// speaks the REST API of the Jira Cloud at input.site as input.email, with the
// API token JIRA_TOKEN, which it holds only as a handle.
const api = "https://" + input.site + "/rest/api/2"
const auth = secrets.text`Basic ${secrets.encoded(secrets.text`${input.email}:${secrets.get("JIRA_TOKEN")}`, { as: "base64" })}`

// call calls Jira and gives its answer; a refusal is thrown with Jira's status,
// so the job fails and says why. No error holds the address called.
function call(method, path, options) {
  const reply = http.request(api + path, Object.assign({ as: "text", timeout: 30 }, options, { method: method, headers: { Authorization: auth, Accept: "application/json" } }))
  if (reply.status < 200 || reply.status >= 300) throw new Error("Jira answered " + method + " " + path.split("?")[0] + " with " + reply.status)
  return reply.body ? JSON.parse(reply.body) : {}
}

// open says everyone who can read the issue can read a comment: one with a
// visibility is for a role or a group, and jsdPublic false is Service Management's
// internal comment.
const open = (c) => !c.visibility && c.jsdPublic !== false

// when reads one of Jira's times, as 2026-10-05T09:00:00.000+0000.
const when = (text) => Date.parse(String(text).replace(/([+-]\d\d)(\d\d)$/, "$1:$2"))

if (input.do === "hear") {
  const now = Date.now()
  if (!input.cursor) return { cursor: { since: new Date(now).toISOString(), seen: [] } }
  const since = Date.parse(input.cursor.since)
  const seen = input.cursor.seen
  const me = call("GET", "/myself").accountId
  const mention = "[~accountid:" + me + "]"
  const minutes = Math.ceil((now - since) / 60000) + 2
  const jql = "project in (" + input.projects.map((p) => JSON.stringify(p)).join(", ") + ") AND updated >= -" + minutes + "m ORDER BY updated ASC"
  // What came at the cursor's own moment is heard unless the job before heard it.
  const things = []
  const add = (at, id, issue, author, text) => {
    if (author && author.accountId !== me && (at > since || (at === since && !seen.includes(id)))) things.push({ at: at, id: id, issue: issue, from: author.accountId, text: text })
  }
  let token
  for (let page = 0; page < 10; page++) {
    const query = { jql: jql, fields: "summary,description,reporter,created,comment", maxResults: "50" }
    if (token) query.nextPageToken = token
    const found = call("GET", "/search/jql", { query: query })
    for (const issue of found.issues || []) {
      const f = issue.fields || {}
      add(when(f.created), "issue:" + issue.id, issue.key, f.reporter, (f.summary || "") + "\n\n" + (f.description || ""))
      // An answer is a public comment, so what only some can read is not heard.
      for (const c of ((f.comment && f.comment.comments) || []).filter(open)) add(when(c.created), "comment:" + c.id, issue.key, c.author, c.body || "")
    }
    token = found.nextPageToken
    if (!token || found.isLast) break
  }
  if (!things.length) return { cursor: input.cursor }
  things.sort((a, b) => a.at - b.at)
  for (const t of things) {
    if (!t.text.includes(mention)) continue
    emit("message", { bridge: "jira", text: t.text.replaceAll(mention, "").trim(), chat: t.issue, at: t.id, from: t.from, place: t.issue.slice(0, t.issue.lastIndexOf("-")), settings: { site: input.site, email: input.email } })
  }
  const last = things[things.length - 1].at
  const at = things.filter((t) => t.at === last).map((t) => t.id)
  return { cursor: { since: new Date(last).toISOString(), seen: last === since ? seen.concat(at) : at } }
}

if (input.do === "history") {
  // The issue and its comments before this one. A message that is the opening of
  // an issue has none, and holds the issue itself.
  const at = String(input.at || "")
  if (!at.startsWith("comment:")) return null
  const id = Number(at.slice("comment:".length))
  const issue = "/issue/" + encodeURIComponent(String(input.to))
  const me = call("GET", "/myself").accountId
  const name = (author) => (author.accountId === me ? "you" : author.displayName || author.accountId)
  const f = call("GET", issue, { query: { fields: "summary,description,reporter" } }).fields || {}
  const lines = [name(f.reporter || {}) + ": " + (f.summary || "") + "\n\n" + (f.description || "")]
  const comments = call("GET", issue + "/comment", { query: { orderBy: "-created", maxResults: "30" } }).comments || []
  // A restricted comment is left out: the token may read it, and whoever reads
  // the answer may not.
  for (const c of comments.reverse().filter(open)) {
    if (Number(c.id) < id) lines.push(name(c.author || {}) + ": " + c.body)
  }
  return require("../../blocks/thread.js").text(lines)
}

// say: a comment on the issue, in parts Jira takes.
if (!input.text) throw new Error("there is nothing to say")
const longest = 32767
const issue = "/issue/" + encodeURIComponent(String(input.to))
let text = String(input.text)
let sent
while (text.length) {
  sent = call("POST", issue + "/comment", { json: { body: text.slice(0, longest) } })
  text = text.slice(longest)
}
return { sent: "comment:" + sent.id }
