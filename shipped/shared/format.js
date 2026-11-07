// How to write for the bridge a message came from, which an agent's system text
// holds as ${input.format}. Charts and layouts are as rich.js reads them.
const chart = [
  "To show numbers as a chart, write a fenced block marked chart, holding one JSON object:",
  "",
  "```chart",
  '{"type": "bar", "title": "Tickets", "labels": ["Jan", "Feb"], "series": [{"name": "Email", "values": [140, 165]}], "x": "Month", "y": "Tickets"}',
  "```",
  "",
  "type is bar, line, area or pie, and a pie takes the first series. Keep the title, labels and names short.",
].join("\n")
const layout = [
  "For a layout Markdown cannot give, such as a header, fields side by side, a context line, an image or a button that opens a link, write a fenced block marked blocks, holding a JSON list of Slack Block Kit blocks:",
  "",
  "```blocks",
  '[{"type": "header", "text": {"type": "plain_text", "text": "Release 4.2"}}, {"type": "section", "fields": [{"type": "mrkdwn", "text": "*Status*\\nShipped"}, {"type": "mrkdwn", "text": "*Owner*\\nAl"}]}]',
  "```",
  "",
  "A button in a layout only opens a link: give it a url. Use plain Markdown where it does.",
].join("\n")
const ways = {
  slack: "Your reply is shown on Slack, in Markdown.\n\n" + chart + " Slack draws at most two charts in a message, and shows any other as a table.\n\n" + layout,
  telegram: "Your reply is shown on Telegram as plain text: write no Markdown, and no tables.",
  gitlab: "Your reply is a comment on a GitLab issue, in GitLab Flavored Markdown. GitLab draws a fenced block marked mermaid as a diagram.\n\n" + chart + " GitLab shows a chart as a table.",
  jira: "Your reply is a comment on a Jira issue, in Markdown, which Rook writes as Jira's wiki markup.\n\n" + chart + " Jira shows a chart as a table.",
}
// Only an agent's last text is its reply, so text written beside a tool call is lost.
const last = "Only what you write after your last tool call is shown, so write the whole reply there."
return ways[input.bridge] ? ways[input.bridge] + "\n\n" + last : ""
