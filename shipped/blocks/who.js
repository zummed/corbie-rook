// The entry of rules.yaml's agents a person gets in a place: the first whose
// `for` holds them and everyone who reads there, or null. A place rules.yaml
// does not list is read by anyone.
const { places, agents } = require("../../rules.yaml")
const asker = [input.person, ...input.groups]
const readers = ((places || {})[input.place] || { readers: ["anyone"] }).readers
for (const item of agents || []) {
  if (!Array.isArray(item.for)) throw new Error("rules.yaml: the agent " + item.name + " has no for: list")
}
const holds = (item, names) => item.for.includes("anyone") || names.some((n) => item.for.includes(n))
const allowed = (item) => holds(item, asker) && readers.every((r) => r === "asker" || holds(item, [r]))
return (agents || []).find(allowed) || null
