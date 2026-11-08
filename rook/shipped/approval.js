// Who is asked about the jobs an approval holds, and what they are shown.
//
// The jobs go in groups: a job whose tool or, failing that, toolkit names an
// entry of `by` goes with that entry's group, and every other job with the
// approval's own. An entry gives any of ask, approvers and within, in place of
// the approval's. Each group is asked in each of its places, with the ballot
// <ballot>/<group's number>.
//
// For each job a group is shown what the agent said as it asked and the job's
// arguments, then who asked and what was asked. What the agent said, what was
// asked and an argument of more than one line are quoted, each line after
// "> ", so that no line of them reads as Rook's own. The job's name is in the
// question's first line, and is given again here only when there are several.
const quoted = (text) => String(text).split("\n").map((line) => "> " + line).join("\n")
// A value of one line longer than this, such as a link or a file's content,
// is shown as its first and last 150 characters, with its length: whole, it
// can make the question longer than a service takes. Its end is shown, since
// what a command does can be at its end. A value of several lines, such as a
// diff, is shown whole.
const LONG = 300
const cut = (text, at) => text.slice(0, /[\uDC00-\uDFFF]/.test(text[at] || "") ? at - 1 : at)
const short = (value) => value.length > LONG && !value.includes("\n")
  ? cut(value, 150) + " … (" + value.length + " characters, the middle left out) … " + value.slice(cut(value, value.length - 150).length)
  : value
// flat gives each argument as [path, value], a nested key joined by dots.
const flat = (value, path) =>
  value && typeof value === "object" && !Array.isArray(value)
    ? Object.entries(value).flatMap(([key, inner]) => flat(inner, path ? path + "." + key : key))
    : [[path, short(typeof value === "string" ? value : JSON.stringify(value))]]
const shown = (args) => {
  const pairs = flat(args, "").map(([path, value]) => [path ? path + ":" : "", value])
  if (pairs.every(([, value]) => !value.includes("\n"))) return pairs.map((pair) => pair.join(" ").trim()).join(" · ")
  return pairs.map(([label, value]) => value.includes("\n") ? (label + "\n" + quoted(value)).trim() : (label + " " + value).trim()).join("\n")
}
const context = (jobs) => {
  const blocks = jobs.map((j) => {
    const asked = j.asked || {}
    const lines = jobs.length > 1 ? [j.name + ":"] : []
    if (asked.said) lines.push(quoted(asked.said))
    const args = shown(asked.args || j.input || {})
    if (args) lines.push(args)
    return lines.join("\n")
  })
  const request = []
  if (input.asker) request.push("Asked by " + input.asker + ".")
  if (input.request) request.push(quoted(input.request))
  if (request.length) blocks.push(request.join("\n"))
  return blocks.join("\n\n")
}

const by = input.by || {}
const has = (name) => typeof name === "string" && Object.prototype.hasOwnProperty.call(by, name)
for (const [name, entry] of Object.entries(by)) {
  for (const key of Object.keys(entry || {})) {
    if (!["ask", "approvers", "within"].includes(key)) throw new Error("by." + name + " gives " + key + ": an entry of by gives ask, approvers or within")
  }
}
const routeOf = (j) => { const asked = j.asked || {}; return has(asked.tool) ? asked.tool : has(asked.toolkit) ? asked.toolkit : "" }
const routes = [...new Set((input.jobs || []).map(routeOf))]
const groups = routes.map((route, n) => {
  const jobs = input.jobs.filter((j) => routeOf(j) === route)
  const names = jobs.map((j) => j.name)
  const group = Object.assign({ ask: input.ask, approvers: input.approvers, within: input.within }, route ? by[route] : {})
  if (!group.ask || !group.ask.length || !group.approvers) throw new Error("No one is asked about " + names.join(", ") + ": give the approval's ask and approvers" + (route ? ", or by." + route + "'s" : ", or an entry of by for its tool or toolkit"))
  return { names: names, context: context(jobs), approvers: group.approvers, within: group.within || "7d", ask: group.ask, ballot: input.ballot + "/" + n }
})
return { groups: groups, places: groups.flatMap((g, n) => g.ask.map((place) => ({ group: n, at: place.at, in: place.in }))) }
