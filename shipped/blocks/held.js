// What the reviewer and the approvers are shown of the jobs a part's approval
// holds: who asked and what, then for each job the tool, what it does, its
// arguments and what the agent said as it asked; and whether reviewer's yes is
// the last word, which it is only when every job held is one left to reviewer
// alone (input.alone).
const jobs = input.jobs || []
const lines = []
if (input.request) lines.push("Request, from " + (input.asker || "nobody named") + ": " + input.request, "")
for (const j of jobs) {
  const asked = j.asked || {}
  lines.push("Tool: " + j.name + (asked.does ? " (" + asked.does + ")" : ""))
  lines.push("Arguments: " + JSON.stringify(asked.args || j.input || {}, null, 2))
  if (asked.said) lines.push("The agent said: " + asked.said)
  lines.push("")
}
// A job's name carries which call of its tool it is, as tool#2 or tool.1.
const alone = jobs.length > 0 && jobs.every((j) => (input.alone || []).includes(j.name.split(/[.#]/)[0]))
lines.push(alone
  ? "Your yes ends it: the call runs at once."
  : "Your yes is passed on as a recommendation, and a person answers.")
return { context: lines.join("\n"), names: jobs.map((j) => j.name), alone: alone }
