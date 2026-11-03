// What rules.yaml says under approve for a part: a list of people and groups, or
// { people, in }, where in is a Slack channel to ask in.
const { approve } = require("../../rules.yaml")
const rule = (approve || {})[input.part]
if (!rule) throw new Error("rules.yaml names no one to approve the part " + input.part)
return Array.isArray(rule) ? { people: rule, in: "" } : { people: rule.people || [], in: rule.in || "" }
