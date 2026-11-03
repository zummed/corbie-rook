// Jira's REST API, for the bridge and for a part's script. api gives call for the
// Jira Cloud at site, signed in as email with the API token JIRA_TOKEN, which it
// holds only as a handle. A job that loads it lists .rook/bridges/jira/api.js
// under files and allows net:<site> and secret:JIRA_TOKEN@net:<site>.
//
// call(method, path, options) calls Jira and gives its answer; a refusal is thrown
// with Jira's status, so the job fails and says why. No error holds the address
// called. A thing gone or out of the token's sight is null where options.maybe
// says so.
exports.api = (site, email) => {
  const base = "https://" + site + "/rest/api/2"
  const auth = secrets.text`Basic ${secrets.encoded(secrets.text`${email}:${secrets.get("JIRA_TOKEN")}`, { as: "base64" })}`
  return (method, path, options) => {
    const { maybe, ...rest } = options || {}
    const reply = http.request(base + path, Object.assign({ as: "text", timeout: 30 }, rest, { method: method, headers: { Authorization: auth, Accept: "application/json" } }))
    if (maybe && (reply.status === 403 || reply.status === 404)) return null
    if (reply.status < 200 || reply.status >= 300) throw new Error("Jira answered " + method + " " + path.split("?")[0] + " with " + reply.status)
    return reply.body ? JSON.parse(reply.body) : {}
  }
}
