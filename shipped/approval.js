// What the approvers are shown of the jobs an approval holds: for each job
// what the agent said as it asked and the job's arguments, then who asked and
// what was asked. What the agent said, what was asked and an argument of more
// than one line are quoted, each line after "> ", so that no line of them
// reads as Rook's own. The job's name is in the question's first line, and is
// given again here only when there are several.
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
const jobs = input.jobs || []
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
return { context: blocks.join("\n\n"), names: jobs.map((j) => j.name) }
