// How to write for the bridge a message came from, which an agent's system text
// holds as ${input.format}. Charts are as rich.js reads them.
const chart = [
  "To show numbers as a chart, write a fenced block marked chart, holding one JSON object:",
  "",
  "```chart",
  '{"type": "bar", "title": "Tickets", "labels": ["Jan", "Feb"], "series": [{"name": "Email", "values": [140, 165]}], "x": "Month", "y": "Tickets"}',
  "```",
  "",
  "type is bar, line, area or pie, and a pie takes the first series. Keep the title, labels and names short.",
].join("\n")
const ways = {
  slack: "Your reply is shown on Slack, in Markdown.\n\n" + chart + " Slack draws at most two charts in a message, and shows any other as a table.",
  telegram: "Your reply is shown on Telegram as plain text: write no Markdown, and no tables.",
  gitlab: "Your reply is a comment on a GitLab issue, in Markdown.\n\n" + chart + " GitLab shows a chart as a table.",
  jira: "Your reply is a comment on a Jira issue, in Jira's wiki markup, not Markdown.\n\n" + chart + " Jira shows a chart as a table.",
}
return ways[input.bridge] || ""
