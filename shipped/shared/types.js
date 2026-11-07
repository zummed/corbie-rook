// The media type a file's name gives by its extension, for a service that gives
// none, or `otherwise`.
const types = {
  png: "image/png", jpg: "image/jpeg", jpeg: "image/jpeg", gif: "image/gif", webp: "image/webp", svg: "image/svg+xml",
  pdf: "application/pdf", json: "application/json", xml: "application/xml",
  txt: "text/plain", log: "text/plain", md: "text/markdown", csv: "text/csv", html: "text/html", yaml: "text/yaml", yml: "text/yaml",
}

exports.of = (name, otherwise) => types[((/\.(\w+)$/.exec(name) || [])[1] || "").toLowerCase()] || otherwise
