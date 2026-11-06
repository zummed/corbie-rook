// The entry of rules.yaml's agents a person gets in a place: the first whose
// `for` holds them and everyone who reads there, or null. A place rules.yaml
// does not list is read by anyone. A field rules.yaml does not take fails,
// naming it, so a mistyped one is not passed over.
const rules = require("../../rules.yaml") || {}
const unknown = (where, item, what, fields) => {
  const extra = Object.keys(item || {}).filter((k) => !fields.includes(k))
  if (extra.length) throw new Error("rules.yaml: " + where + " has " + extra.join(" and ") + ", and " + what + " takes only " + fields.join(" and "))
}
unknown("the file", rules, "the file", ["places", "agents"])
const { places, agents } = rules
for (const [name, place] of Object.entries(places || {})) unknown("the place " + name, place, "a place", ["readers"])
for (const item of agents || []) {
  unknown("the agent " + item.name, item, "an agent", ["name", "for"])
  if (!Array.isArray(item.for)) throw new Error("rules.yaml: the agent " + item.name + " has no for: list")
}
const asker = [input.person, ...input.groups]
const readers = ((places || {})[input.place] || { readers: ["anyone"] }).readers
const holds = (item, names) => item.for.includes("anyone") || names.some((n) => item.for.includes(n))
const allowed = (item) => holds(item, asker) && readers.every((r) => r === "asker" || holds(item, [r]))
return (agents || []).find(allowed) || null
