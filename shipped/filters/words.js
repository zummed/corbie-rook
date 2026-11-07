// Whether input.text holds one of input.words as a whole word or phrase,
// ignoring case and how many spaces are between words: a word matches where a
// letter or digit neither comes before it nor follows it, so "deploy" matches
// "Deploy it?" and not "deployed".
const plain = (s) => String(s).toLowerCase().replace(/\s+/g, " ").trim()
const text = plain(input.text)
const letter = /[\p{L}\p{N}]/u
const at = (word) => {
  for (let i = text.indexOf(word); i >= 0; i = text.indexOf(word, i + 1)) {
    const before = text.slice(0, i), after = text.slice(i + word.length)
    if (!letter.test(before.slice(-1)) && !letter.test(after.slice(0, 1))) return true
  }
  return false
}
return input.words.map(plain).some((w) => w !== "" && at(w))
