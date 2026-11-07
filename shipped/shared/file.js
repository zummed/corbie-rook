// The file send.yaml sends: the blob given, or else the text, made a blob of the
// type its name gives, by default text/plain.
if (input.file) return input.file
if (typeof input.text !== "string") throw new Error("send takes a blob or a text, and was given neither")
return blobs.make(input.text, { type: require("./types.js").of(input.name, "text/plain") })
