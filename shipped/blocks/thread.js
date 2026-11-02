// What the bridges share to give a thread's history. text gives lines, oldest
// first, as one text: the latest of them, and no more than a model needs. A
// thread's words cannot close the <thread> tag the prompt puts them in.
const mostLines = 20
const mostChars = 8000

exports.text = (lines) => {
  const text = lines.slice(-mostLines).join("\n\n").replace(/<\/?thread/gi, "(thread")
  return text.length > mostChars ? "…" + text.slice(1 - mostChars) : text
}
