// The conversation's list of messages to answer: `list` with `add` after it.
// It always gives a list, since a job that gives nothing clears its state's
// output.
return input.list.concat(input.add)
