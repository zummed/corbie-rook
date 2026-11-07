// What the approvers are shown of the jobs an approval holds: who asked, by
// their email, what was asked, then for each job the tool, its description,
// its arguments and what the agent said as it asked. What was asked and what
// the agent said are quoted, each line after "> ", so that no line of them
// reads as Rook's own.
const quoted = (text) => String(text).split("\n").map((line) => "> " + line).join("\n")
const jobs = input.jobs || []
const lines = [input.asker ? "Asked by " + input.asker + "." : "The asker's email is not given.", ""]
if (input.request) lines.push("Request, the latest message last:", quoted(input.request), "")
for (const j of jobs) {
  const asked = j.asked || {}
  lines.push("Tool: " + j.name + (asked.description ? " (" + asked.description + ")" : ""))
  lines.push("Arguments: " + JSON.stringify(asked.args || j.input || {}, null, 2))
  if (asked.said) lines.push("The agent said:", quoted(asked.said))
  lines.push("")
}
return { context: lines.join("\n").trim(), names: jobs.map((j) => j.name) }
