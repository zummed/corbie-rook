// The person whose email is input.email in people/scopes.yaml, as { name, email,
// scopes }, matched ignoring case. An email the table does not hold, or none,
// gives no scopes and the name the service gave. A table that is empty is a
// table with no rows. A row that is not { name, scopes } fails every lookup,
// naming the row, so that a mistake in the table is seen at once.
const table = require("../../people/scopes.yaml") || {}
if (typeof table !== "object" || Array.isArray(table)) throw new Error("people/scopes.yaml is not a mapping of emails to { name, scopes }")
const isRow = (row) => row !== null && typeof row === "object" && !Array.isArray(row) &&
  Object.keys(row).every((k) => k === "name" || k === "scopes") &&
  (row.name === undefined || typeof row.name === "string") &&
  (row.scopes === undefined || Array.isArray(row.scopes) && row.scopes.every((s) => typeof s === "string"))
for (const [email, row] of Object.entries(table)) {
  if (!isRow(row)) throw new Error("people/scopes.yaml: the row of " + email + " is not { name: <text>, scopes: [<scope>, ...] }")
}
const email = input.email.toLowerCase()
const found = email ? Object.keys(table).find((e) => e.toLowerCase() === email) : undefined
if (found === undefined) return { name: input.name, email: input.email, scopes: [] }
const row = table[found]
return { name: row.name || input.name, email: found, scopes: row.scopes || [] }
