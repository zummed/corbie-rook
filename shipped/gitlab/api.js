// GitLab's REST API, for the bridge and for a part's script. api gives call for
// the GitLab at site, with the token GITLAB_TOKEN, which it holds only as a
// handle. A job that loads it lists .rook/gitlab/api.js under files and
// allows net:<site> and secret:GITLAB_TOKEN@net:<site>.
//
// call(method, path, options) calls GitLab and gives its answer; a refusal is
// thrown with GitLab's status, so the job fails and says why. No error holds the
// address called. A thing gone or out of the token's sight is null where
// options.maybe says so.
exports.api = (site) => {
  if (String(site).includes("/")) throw new Error("GITLAB_SITE is a host name, such as gitlab.example.com, with no https:// and no path: it is " + site)
  const base = "https://" + site + "/api/v4"
  const token = secrets.get("GITLAB_TOKEN")
  return (method, path, options) => {
    const { maybe, ...rest } = options || {}
    const reply = http.request(base + path, Object.assign({ method: method, headers: { "PRIVATE-TOKEN": token }, as: "text", timeout: 30 }, rest))
    if (maybe && (reply.status === 403 || reply.status === 404)) return null
    if (reply.status < 200 || reply.status >= 300) throw new Error("GitLab answered " + method + " " + path.split("?")[0] + " with " + reply.status)
    return reply.body ? JSON.parse(reply.body) : null
  }
}
