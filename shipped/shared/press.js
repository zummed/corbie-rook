// Whether a press on a question's button counts, as { counts, refused, choice,
// by }. It counts when input.choice is one of input.choices, input.scopes, the
// presser's, hold input.approvers, and input.email, the presser's, is not
// input.asker. refused says why it does not count, for the presser.
const email = String(input.email || "").toLowerCase()
let refused = ""
if (!input.choices.includes(input.choice)) refused = "That is not one of the choices."
else if (input.asker && email === String(input.asker).toLowerCase()) refused = "You asked for this, so someone else must answer it."
else if (!(input.scopes || []).includes(input.approvers)) refused = "Only someone with the scope " + input.approvers + " can answer this."
if (refused) log(input.by + " pressed " + String(input.choice).slice(0, 100) + ", which does not count: " + refused)
return { counts: !refused, refused: refused, choice: input.choice, by: input.by }
