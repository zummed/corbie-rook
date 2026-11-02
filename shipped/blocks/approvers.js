// The people and groups rules.yaml names under approve for a part.
const { approve } = require("../../rules.yaml")
if (!(approve || {})[input.part]) throw new Error("rules.yaml names no one to approve the part " + input.part)
return approve[input.part]
