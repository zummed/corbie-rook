// GitLab's REST API, for the bridge and for a part's script. api gives call for
// the GitLab at site, with the token GITLAB_TOKEN, which it holds only as a
// handle. A job that loads it lists .rook/bridges/gitlab/api.js under files and
// allows net:<site> and secret:GITLAB_TOKEN@net:<site>.
//
// call(method, path, options) calls GitLab and gives its answer; a refusal is
// thrown with GitLab's status, so the job fails and says why. No error holds the
// address called. A thing gone or out of the token's sight is null where
// options.maybe says so. request gives the same call's whole reply, as
// http.request does, and throws nothing for a refusal.
exports.request = (site) => {
  const base = "https://" + site + "/api/v4"
  const token = secrets.get("GITLAB_TOKEN")
  return (method, path, options) => http.request(base + path, Object.assign({ method: method, headers: { "PRIVATE-TOKEN": token }, as: "text", timeout: 30 }, options))
}

exports.api = (site) => {
  const request = exports.request(site)
  return (method, path, options) => {
    const { maybe, ...rest } = options || {}
    const reply = request(method, path, rest)
    if (maybe && (reply.status === 403 || reply.status === 404)) return null
    if (reply.status < 200 || reply.status >= 300) throw new Error("GitLab answered " + method + " " + path.split("?")[0] + " with " + reply.status)
    return reply.body ? JSON.parse(reply.body) : null
  }
}
