// The person whose email is input.email in people/scopes.yaml, as { name, email,
// scopes }. A table that is empty is a table with no rows.
const table = require("../../people/scopes.yaml") || {}
const email = input.email.toLowerCase()
const found = email ? Object.keys(table).find((e) => e.toLowerCase() === email) : undefined
if (found === undefined) return { name: input.name, email: input.email, scopes: [] }
const row = table[found] || {}
return { name: String(row.name || input.name), email: found, scopes: (row.scopes || []).map(String) }
