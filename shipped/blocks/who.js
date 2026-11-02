// The agent a person gets in a place: the first of agents whose `for` holds them
// and everyone who reads there. A place rules.yaml does not list is read by anyone.
const { places, agents } = require("../../rules.yaml")
const asker = [input.person, ...input.groups]
const readers = ((places || {})[input.place] || { readers: ["anyone"] }).readers
const holds = (item, names) => item.for.includes("anyone") || names.some((n) => item.for.includes(n))
const allowed = (item) => holds(item, asker) && readers.every((r) => r === "asker" || holds(item, [r]))
return (agents.find(allowed) || {}).name || null
