// GitLab's REST API, for the service's machines and for a toolkit's script. api
// gives call for the GitLab at site, with the token GITLAB_TOKEN, which it holds
// only as a handle. A job that loads it lists .rook/gitlab/api.js under files and
// allows net:<site> and secret:GITLAB_TOKEN@net:<site>.
//
// call(method, path, options) calls GitLab and gives its answer, read as JSON,
// or as text with options.raw. With options.whole it gives { headers, body }. A refusal
// is thrown with GitLab's status, so the job fails and says why. No error holds
// the address called. A thing gone or out of the token's sight is null where
// options.maybe says so. A 429 is thrown with retry, the seconds GitLab's
// Retry-After asks to wait. call.limit holds the latest RateLimit-Remaining,
// RateLimit-Limit and RateLimit-Reset GitLab gave.
exports.api = (site) => {
  if (String(site).includes("/")) throw new Error("GITLAB_SITE is a host name, such as gitlab.example.com, with no https:// and no path: it is " + site)
  const base = "https://" + site + "/api/v4"
  const token = secrets.get("GITLAB_TOKEN")
  const limit = {}
  const call = (method, path, options) => {
    const { maybe, whole, raw, ...rest } = options || {}
    const reply = http.request(base + path, Object.assign({ method: method, headers: { "PRIVATE-TOKEN": token }, as: "text", timeout: 30 }, rest))
    const h = reply.headers || {}
    if (h["ratelimit-remaining"] !== undefined) {
      Object.assign(limit, { remaining: Number(h["ratelimit-remaining"]), limit: Number(h["ratelimit-limit"]), reset: Number(h["ratelimit-reset"]) })
    }
    const where = method + " " + path.split("?")[0]
    if (reply.status === 429) {
      const e = new Error("GitLab answered " + where + " with 429, too many requests")
      e.retry = Number(h["retry-after"]) || 60
      throw e
    }
    if (maybe && (reply.status === 403 || reply.status === 404)) return null
    if (reply.status < 200 || reply.status >= 300) throw new Error("GitLab answered " + where + " with " + reply.status)
    const body = raw ? reply.body : reply.body ? JSON.parse(reply.body) : null
    return whole ? { headers: h, body: body } : body
  }
  call.limit = limit
  return call
}
