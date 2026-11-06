// What the review and the approvers are shown of the jobs a part's approval
// holds: what was asked, then for each job the tool, what it does, its
// arguments and what the agent said as it asked; and whether the review's yes
// is the last word, which it is only when every job held is one left to the
// review alone (input.alone). What was asked and what the agent said are
// quoted, each line after "> ", so that no line of them reads as Rook's own.
const quoted = (text) => String(text).split("\n").map((line) => "> " + line).join("\n")
const jobs = input.jobs || []
const lines = []
if (input.request) lines.push("Request, the latest message last:", quoted(input.request), "")
for (const j of jobs) {
  const asked = j.asked || {}
  lines.push("Tool: " + j.name + (asked.does ? " (" + asked.does + ")" : ""))
  lines.push("Arguments: " + JSON.stringify(asked.args || j.input || {}, null, 2))
  if (asked.said) lines.push("The agent said:", quoted(asked.said))
  lines.push("")
}
// A job's name carries which call of its tool it is, as tool#2 or tool.1.
const alone = jobs.length > 0 && jobs.every((j) => (input.alone || []).includes(j.name.split(/[.#]/)[0]))
// rule is for the review alone: what its yes does.
const rule = alone ? "Your yes ends it: the call runs at once." : "Your yes is passed on as a recommendation, and a person answers."
return { context: lines.join("\n").trim(), rule: rule, names: jobs.map((j) => j.name), alone: alone }
