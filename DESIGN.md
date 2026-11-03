# Rook

Rook is ready-made Corbie machines. A message on Telegram or Slack, or a GitLab or Jira issue that names the bot, reaches a machine that is already running, and what happens next is written in machines the person can change.

| Piece | What it does |
|---|---|
| Corbie | runs machines; `corbie run submit` brings a directory's runs into line with its files; `corbie ui` shows them |
| Rook | `corbie rook init` writes its ready-made machines into a project |
| A project | the person's own directory: people, rules, agents and parts |


## Five slots

A project fills five slots. Each is one or a few files, and nothing else depends on how one is filled.

| Slot | Question | What fills it |
|---|---|---|
| Bridges | where does work come in and go out? | a file for each in `bridges/`: `telegram.yaml`, `slack.yaml`, `gitlab.yaml`, `jira.yaml` |
| People | who is this, what groups are they in, and how are they reached? | `lookup.yaml`, by default from `people.yaml` |
| Attend | is a message that does not name the bot meant for it? | `attend.yaml`, by default no |
| Rules | which group gets which agent, and who approves what? | `rules.yaml`, `answer.yaml` |
| Parts | what can the agents do? | `agents/`, `parts/` |

A slot's file names a machine as a job. Rook's ready-made machine for a slot is in `.rook/<slot>/`, and a project's own machine can take its place.

## A project

`corbie rook init` writes `.rook/`, and the first time the starting files. It rewrites `.rook/` every time, writes any file `.rook/` reads by name that the project lacks, and changes no other file.

A project's own files hold its choices, and `.rook/` holds what Rook does. When `init` updates a project, it names the project's files that are not as this version starts a project; the manual page `rook-starter` shows this version's starting files and every bridge's file.

| In `.rook/` | What it does |
|---|---|
| `bridges/<name>/listen.yaml` | hears a service, and sends each message and answer by key |
| `bridges/<name>/say.yaml`, `ask.yaml`, `close.yaml`, `history.yaml`, `status.yaml` | says a text, asks a question with buttons, shows its answer, reads a thread, and shows a status |
| `bridges/gitlab/api.js`, `bridges/jira/api.js` | the service's REST API, for the bridge and a part's script |
| `blocks/chat.yaml` | one conversation on any bridge, a message at a time: `who`, `attend`, then `answer` and `say` |
| `blocks/who.yaml` | the person an event is from, and the agent they get where it came from |
| `blocks/say.yaml`, `tell.yaml`, `ask.yaml`, `status.yaml` | say at an address, tell a person, ask people, show a status |
| `blocks/history.yaml`, `thread.js` | what a service's thread held before a message |
| `blocks/rich.js` | the charts in a text, drawn by Slack and shown as tables by the other bridges |
| `blocks/format.js` | how to write for each bridge, given to an agent as `format` |
| `agents/base.yaml` | what every agent answering a message takes from Rook: its input, prompt, conversation and the tool `progress` |
| `blocks/question.yaml`, `close.yaml` | the run behind `ask.yaml`, which a tap goes to, and what shows its answer |
| `blocks/approval.yaml` | a part's ask: the reviewer, then the part's approvers |
| `people/file.js` | the People slot, read from `people.yaml` |
| `attend/never.yaml`, `attend/model.yaml` | the Attend slot: no, or asking a small model |

A machine in `.rook/` reads a project's files by these names: `rules.yaml`, `lookup.yaml`, `attend.yaml`, `answer.yaml` and `agents/reviewer.yaml`. `.rook/people/file.js` reads `people.yaml`.

## A bridge

A bridge's file starts its listener, and the bridge runs while the file is there. It holds what the bridge needs to know about the project.

```yaml
# bridges/gitlab.yaml
flow: gitlab-bridge
limits: { depth: 16 }   # every conversation counts its levels from here
stages:
  listen:
    job:
      machine: .rook/bridges/gitlab/listen.yaml
      restart: { when: always }
      input: { dir: "${run.context}", site: gitlab.example.com, projects: [group/project] }
```

A conversation starts below the bridge, so a bridge's file sets `limits: { depth: }` for all of them. Corbie's default of 8 is too few for an approval.

`restart: { when: always }` brings a listener back if it ever ends. A listener handles a failed hear itself, in its `resting` state, so a drawing shows the wait.

Every bridge ships in `.rook/`, because a block names each bridge it may use, and Corbie reads every file a machine names. One a project does not run is never reached, and needs no secret.

A listener sends two events by key: `message`, to `blocks/chat.yaml` under `<bridge>:<listener>:<chat>`, where `<listener>` ends the listener's run ID, and `answer`, a tap on a question, to the run with the key `question:<question>`.

A message is one mapping, the same from every bridge, and every block takes it or a part of it.

| Field | What it holds |
|---|---|
| `bridge` | `telegram`, `slack`, `gitlab` or `jira` |
| `chat` | the conversation it belongs to |
| `from` | the sender's id on the bridge |
| `text` | what was said, without the bot's name |
| `place` | where it was said, as `rules.yaml` names places |
| `at` | the message's id on the service, where a bridge reads a thread |
| `answers` | the message a reply answers, on Telegram |
| `settings` | the service's host, and Jira's account, which a reply needs |
| `unaddressed` | `true` for a message that does not name the bot, and absent otherwise |

An answer holds `bridge`, `question`, `from` and `choice`.

| Bridge | Hears | `chat` | `place` |
|---|---|---|---|
| Telegram | a direct message | the chat | `dm` |
| Slack | a direct message, or a message naming the bot, over Socket Mode | the conversation, and thread in a channel | `dm`, or `#channel` |
| GitLab | an issue opened or commented on, naming the bot, every 30 seconds | `project#iid` | the project |
| Jira | an issue opened or commented on, naming the bot, every 30 seconds | the issue's key | the project's key |

A bridge's file sets these in its listener's input.

| Setting | Bridges | What it does |
|---|---|---|
| `overhear: true` | Slack | hears a reply in a thread the bot has said something in, marked `unaddressed` |
| `overhear: true` | GitLab, Jira | hears every issue opened and every public comment, marked `unaddressed` where they do not name the bot |
| `settle: 2m` | GitLab, Jira | hears an opened issue once nobody has changed it for that long, as it is then; `0s` by default |

A comment on an issue that is still waiting is not heard on its own, and is in the issue's history. What a bridge hears about who sent something is the service's word. Telegram and Slack can reach a person directly, so they ask questions; GitLab and Jira only comment on an issue.

## A conversation

A conversation is a run. Its messages are answered one at a time, each by a new agent given the conversation so far.

- A conversation quiet for a day ends, and the next message starts afresh.
- After `corbie run submit` starts a bridge again, its next message starts a new conversation, with the machines as they are then. A conversation already under way finishes with the machines it started with.
- A new conversation on Slack, GitLab or Jira reads what the thread or issue held before the message: the latest 20 entries, at most 8000 characters, from the first 1000 of a Slack thread. A machine that gives no `conversation` is given it for each message. Telegram keeps nothing a bot can read, so one there starts with nothing.
- History holds what was said, and no tool calls or results. A read that fails gives no history, and the message is still answered.
- Rook reads nothing for a person with no agent.
- A message marked `unaddressed` from someone with an agent goes to `attend.yaml`, with the thread before it, and is answered only when it gives `answer: true`. A failed `attend.yaml` answers nothing. No one is told anything for an unaddressed message.
- While an agent works, `blocks/status.yaml` shows `Working on it.`, and a reply clears it. An answer that says nothing clears it.
- A conversation waits while its message waits, approvals included, up to seven days.
- An answer that fails is told so, and the conversation starts afresh.
- A person with no agent who names the bot is told the bot is not theirs.

## Your machines

Your machines use Rook's blocks as jobs. A job runs from a flow, a statechart or an agent's tool, and reports `done` or `failed`. Keys are internal to Rook: they carry a message to its conversation and a tap to its question, and your machines name none.

`answer.yaml` runs the agent `who` chose as a job. Any other machine can take its place, with the same input and output.

| Input | What it holds |
|---|---|
| `said` | the text of the message |
| `asker` | the person's name, from `lookup.yaml` |
| `home` | the project's directory |
| `address` | where the message came from |
| `history` | what the service's thread held before this message, for a new conversation on Slack, GitLab or Jira, or nothing |
| `conversation` | what the last answer gave, or nothing |
| `format` | how to write for the message's bridge: Markdown or not, and how to write a chart |

| Output | What it does |
|---|---|
| `reply` | `chat.yaml` says it at the `address`; an empty reply or no output says nothing |
| `conversation` | goes to the next answer in this chat |

If the machine fails, `chat.yaml` tells the person, and the next message starts a new conversation.

Any machine can run these blocks. A path reads from the project's top directory, wherever the file that writes it sits, and a path starting `./` from that file's own directory.

| Block | Input | What it does |
|---|---|---|
| `.rook/blocks/tell.yaml` | `person`, `text` | says a text to a person named in `lookup.yaml` |
| `.rook/blocks/say.yaml` | `address`, `text` | says a text at an address |
| `.rook/blocks/ask.yaml` | `people`, `in`, `text`, `choices` | asks people, or a Slack channel, and gives `choice` |
| `.rook/blocks/status.yaml` | `address`, `text` | shows a status at an address: under a Slack thread, or "typing" on Telegram; an empty text clears it, and a failure gives nothing |

An address is a mapping: `{ bridge, id }`, with `answers`, `at` and `settings` where a bridge uses them. On Slack, `id` may be a channel's `#name`. `chat.yaml` gives one for a message, and `lookup.yaml`, with `do: reach`, gives one for each person. Pass it whole.

`ask.yaml` takes names, as `lookup.yaml` knows them. A message's address can be a channel or an issue, so it goes to `say.yaml` only.

```yaml
# A machine in schedules/ tells a person.
job: { machine: .rook/blocks/tell.yaml, input: { person: alice, text: The backup failed. } }

# A machine answering a message says something while it works.
job: { machine: .rook/blocks/say.yaml, input: { address: "${input.address}", text: Working on it. } }

# An agent's tool shows a status, and its model chooses only the text.
tools:
  progress:
    does: Show in a few words what you are doing now, while you work.
    args: { text: string }
    job: { machine: .rook/blocks/status.yaml, input: { address: "${input.address}", text: "${args.text}" } }
```

`.rook/agents/base.yaml` holds this tool. It needs no `approve: true`, since its text goes to those who read the answer. A tool whose arguments name a person does. Give a model no address as an argument: `settings.site` is the host a bridge sends its token to.

A person's reply to a text goes to `chat.yaml` as a new message, and the agent there does not know the text. Only a tap on a question goes back to the machine that asked.

## Charts

A text says a chart as a fenced block marked `chart`, holding one JSON object. Slack draws it, two to a message, and every other bridge shows it as a table; so does Slack, for a chart outside its limits.

````
```chart
{"type": "bar", "title": "Tickets", "labels": ["Jan", "Feb"], "series": [{"name": "Email", "values": [140, 165]}], "x": "Month", "y": "Tickets"}
```
````

| Field | What it holds |
|---|---|
| `type` | `bar`, `line`, `area` or `pie`; a pie takes the first series |
| `title` | the chart's title, at most 50 characters for Slack |
| `labels` | the categories, at most 20, each at most 20 characters for Slack |
| `series` | each a `name` and one number in `values` for each label |
| `x`, `y` | the names of the axes |

An agent learns this from `${input.format}`, which tells it on Telegram to write no tables. A block that does not read as a chart is left as it is written.

## Attend

`attend.yaml` takes `said`, `asker`, `address` and `history`, and gives `{ answer: true }` or `{ answer: false }`. Rook ships two machines for it, and any machine with the same input and output can take their place.

| Machine | What it gives |
|---|---|
| `.rook/attend/never.yaml` | no, always; the starting `attend.yaml` runs it |
| `.rook/attend/model.yaml` | what `claude-haiku-5-5` says, with the secret `ANTHROPIC_KEY`, and no when the model is unsure; `about` says what the bot is for |

## Asking a person

A question is a run with a key, named on its buttons, so a press goes to it. `ask.yaml` gives the key.

| Input | What it does |
|---|---|
| `people` | people and groups; each is asked in a direct message on the first bridge `lookup.yaml` gives that reaches them |
| `in` | a Slack channel, as `#name`; the question is posted there in place of direct messages, and the bot must be in it |

- A tap counts when `lookup.yaml` says its sender is someone `people` names, or in a group it names, on any bridge. The first tap that counts is the answer, and others change nothing.
- The question's messages then show who answered, and their buttons go. Unanswered after seven days, the choice is `no`.
- A person with no bridge that reaches them directly cannot be asked: the ask fails, and the call does not run.

## Parts

A part is a group of tools an agent's model opens when it needs them. It is written under `open:`, and is usually kept in a file under `parts/`, which the agent includes.

```yaml
# parts/messages/part.yaml
does: Messages to the people this bot knows.
ask: { machine: .rook/blocks/approval.yaml, input: { part: messages, asker: "${input.asker}", request: "${input.said}" } }
tools:
  tell:
    does: Say a text to a person, by their name in people.yaml.
    args: { person: string, text: string }
    job: { machine: .rook/blocks/tell.yaml, input: { person: "${args.person}", text: "${args.text}" }, approve: true }
```

```yaml
# agents/helper.yaml
agent: helper
include: .rook/agents/base.yaml
model: { name: claude-sonnet-5-5, secret: ANTHROPIC_KEY }
system: |
  You help the person who runs this computer. Answer in one or two short sentences.

  ${input.format}
open:
  messages: { include: parts/messages/part.yaml }
```

An agent includes `.rook/agents/base.yaml`, and writes its own model, system text and parts. `${input.format}` in its system text says how to write for the bridge the message came from.

A part's script can call GitLab or Jira through the bridge's `api.js`. Its job lists the file under `files:` and allows the host and the secret, as the file's comment says.

A part's `ask:` gives `approval.yaml` who asked and what, so the reviewer and the approvers judge a call against the request, and not only the agent's words for it.

## Who gets what

`rules.yaml` is read by `who`. A person gets the first agent that they and everyone who reads the answer may each use, so what they get depends on where they ask.

```yaml
# rules.yaml of a team
places:
  "#help": { readers: [anyone] }
  "#platform": { readers: [platform] }
  dm: { readers: [asker] }
agents:
  - { name: team, for: [platform] }
  - { name: support, for: [anyone] }
approve:
  deploys: [platform]
  refunds: { people: [support], in: "#refunds" }
```

| Who asks | Where | Agent |
|---|---|---|
| a customer | `#help` or a direct message | `support` |
| someone in `platform` | `#help` | `support` |
| someone in `platform` | a direct message or `#platform` | `team` |

- The choice of agent keeps information from an answer, and a prompt never does: a tool the agent does not have cannot be called.
- A part goes in an agent's file only if everyone in that agent's `for` may use it. Nothing checks this: the file shows it.
- A place not listed is read by anyone, so a GitLab or Jira project must be listed for a private agent to answer there.
- History comes from the thread the answer goes to. GitLab's internal notes and Jira's restricted comments are left out of it and are not heard, since an answer is public on the issue. Someone who names the bot in one gets no answer.
- `readers:` names groups, `anyone` or `asker`. It is written in the file, and not read from a channel's members.
- `approve:` names who answers each part's held calls: a list of people and groups, each asked in a direct message, or `{ people, in }`, posted in the Slack channel `in` for any of `people` to answer.

## Who may do what

1. Corbie is the floor. A job may do only what its `allow:` says, within the ceilings, and a secret goes only to the host it is bound to.
2. An agent is allowed nothing, and its model can call only the tools of the parts its file names and it has opened.
3. A tool marked `approve: true` waits for its part's ask.

### What Rook does not defend against

- A tool that sends to a destination its arguments choose can send whatever the run read. Mark it `approve: true`.
- Text a tool returns, such as an issue or a web page, and a thread's history, can tell the model to do things. It can call only the tools of the open parts, and every tool that changes something should wait for a yes.
- What an event says about who sent it is the source's word.
- Anyone who can change the directory can change every rule above but the first. Guard it by review in git.

## Open questions

- Webhooks. GitLab, Jira and Slack can push events; polling and Socket Mode need nothing that listens.
- `answer.yaml` names every agent `rules.yaml` lists. An agent added to one must be added to the other.
- Many overlapping teams need many agent files, one for each mix of parts.
