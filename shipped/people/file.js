// The People slot, read from people.yaml. who gives the person a bridge's id
// belongs to, and their groups. reach gives, for each person or group named,
// where each person is reached: the first bridge their entry gives that can
// reach a person directly and that the project runs. corbie rook init writes
// the bridges the project runs as running.
const file = require("../../people.yaml") || {}
const people = file.people || {}, groups = file.groups || {}
const direct = ["telegram", "slack"]
const running = []
if (input.do === "who") {
  const person = Object.keys(people).find((p) => people[p][input.bridge] !== undefined && String(people[p][input.bridge]) === String(input.id)) || null
  return { person: person, groups: Object.keys(groups).filter((g) => groups[g].includes(person)) }
}
const names = input.names.flatMap((name) => groups[name] || [name])
return [...new Set(names)].map((person) => {
  if (!people[person]) throw new Error("people.yaml has no one called " + person)
  const bridges = Object.keys(people[person]).filter((b) => direct.includes(b))
  if (!bridges.length) throw new Error("people.yaml gives no way to reach " + person + " directly")
  const bridge = bridges.find((b) => running.includes(b))
  if (!bridge) throw new Error("people.yaml reaches " + person + " on " + bridges.join(" and ") + ", which this project does not run")
  return { bridge: bridge, id: String(people[person][bridge]) }
})
