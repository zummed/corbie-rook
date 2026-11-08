// The thread before a turn's messages as text, for the agent's prompt: the
// entries of the history blob that a message holds, as thread.js writes them.
const entries = input.history ? blobs.read(input.history, { as: "json" }) : []
return require("./thread.js").text(entries)
