// Fails with input.error as its error. Corbie adds the line an error is thrown
// at, so the line input.error already ends with is cut, and the error names one.
throw new Error(String(input.error).replace(/ at line \d+, column \d+$/, ""))
