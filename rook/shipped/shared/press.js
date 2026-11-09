// Whether a press counts, given the press and who the lookup says the presser
// is. An email, and the choice, are compared ignoring case: a choice written in
// a comment, as on Jira, is the choice it matches.
const answer = input.answer, who = input.who || {}
const matched = input.choices.find((c) => String(c).toLowerCase() === String(answer.choice).toLowerCase())
const button = matched !== undefined
const own = input.asker !== "" && String(who.email || "").toLowerCase() === input.asker.toLowerCase()
const scoped = (who.scopes || []).includes(input.approvers)
let tell = ""
if (button && own) tell = "You asked for this, so someone else must answer it."
else if (button && !scoped) tell = "Only someone with the scope " + input.approvers + " can answer this."
return { answer: answer, choice: button ? matched : answer.choice, by: who.name || answer.name || answer.user, counts: button && !own && scoped, tell: tell }
