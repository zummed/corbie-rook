// The People slot, read from people.yaml. who gives the person a bridge's id
// belongs to, and their groups. reach gives, for each person or group named,
// the person and their ways: each bridge their entry gives that can reach a
// person directly, in the entry's order, with their id on it.
const file = require("../../people.yaml") || {}
const people = file.people || {}, groups = file.groups || {}
const direct = ["telegram", "slack"]
if (input.do === "who") {
  const person = Object.keys(people).find((p) => people[p][input.bridge] !== undefined && String(people[p][input.bridge]) === String(input.id)) || null
  return { person: person, groups: Object.keys(groups).filter((g) => groups[g].includes(person)) }
}
const names = input.names.flatMap((name) => groups[name] || [name])
return [...new Set(names)].map((person) => {
  if (!people[person]) throw new Error("people.yaml has no one called " + person)
  const ways = Object.keys(people[person]).filter((b) => direct.includes(b)).map((b) => ({ bridge: b, id: String(people[person][b]) }))
  if (!ways.length) throw new Error("people.yaml gives no way to reach " + person + " directly")
  return { person: person, ways: ways }
})
