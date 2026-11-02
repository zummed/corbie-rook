# Rook

Rook is ready-made Corbie machines. A message on Telegram or Slack, or a GitLab or Jira issue that names the bot, reaches a machine that is already running, and what happens next is written in machines the person can change.

| Piece | What it does |
|---|---|
| Corbie | runs machines; `corbie run submit` brings a directory's runs into line with its files; `corbie ui` shows them |
| Rook | `corbie rook init` writes its ready-made machines into a project |
| A project | the person's own directory: people, rules, agents and parts |


## Four slots

A project fills four slots. Each is one or a few files, and nothing else depends on how one is filled.

| Slot | Question | What fills it |
|---|---|---|
| Bridges | where does work come in and go out? | a file for each in `bridges/`: `telegram.yaml`, `slack.yaml`, `gitlab.yaml`, `jira.yaml` |
| People | who is this, what groups are they in, and how are they reached? | `lookup.yaml`, by default from `people.yaml` |
| Rules | which group gets which agent, and who approves what? | `rules.yaml`, `answer.yaml` |
| Parts | what can the agents do? | `agents/`, `parts/` |

## A project

`corbie rook init` writes `.rook/`, and the first time the starting files. It rewrites `.rook/` every time, and changes no other file.

| In `.rook/` | What it does |
|---|---|
| `bridges/<name>/listen.yaml` | hears a service, and sends each message and answer by key |
| `bridges/<name>/say.yaml`, `ask.yaml`, `history.yaml` | says a text, asks a question with buttons, and reads a thread |
| `blocks/chat.yaml` | one conversation on any bridge, a message at a time: `who`, then `answer`, then `say` |
| `blocks/who.yaml` | the person an event is from, and the agent they get where it came from |
| `blocks/say.yaml`, `tell.yaml`, `ask.yaml` | say at an address, tell a person, ask people |
| `blocks/history.yaml`, `thread.js` | what a service's thread held before a message |
| `blocks/question.yaml` | the run behind `ask.yaml`, which a tap goes to |
| `blocks/approval.yaml` | a part's ask: the reviewer, then the part's approvers |

A machine in `.rook/` reads a project's files by these names: `people.yaml`, `rules.yaml`, `lookup.yaml`, `answer.yaml` and `agents/reviewer.yaml`.

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

A listener sends two things by key.

| Event | Fields | Sent to |
|---|---|---|
| `message` | `bridge`, `chat`, `from`, `text`, `place`, and `answers`, `at` and `settings` where a bridge uses them | `blocks/chat.yaml`, under `<bridge>:<chat>` |
| `answer` | `bridge`, `question`, `from`, `choice` | the run with the key `question:<question>` |

| Bridge | Hears | `chat` | `place` |
|---|---|---|---|
| Telegram | a direct message | the chat | `dm` |
| Slack | a direct message, or a message naming the bot, over Socket Mode | the conversation, and thread in a channel | `dm`, or `#channel` |
| GitLab | an issue opened or commented on, naming the bot, every 30 seconds | `project#iid` | the project |
| Jira | an issue opened or commented on, naming the bot, every 30 seconds | the issue's key | the project's key |

What a bridge hears about who sent something is the service's word. Telegram and Slack can reach a person directly, so they ask questions; GitLab and Jira only comment on an issue.

## A conversation

A conversation is a run. Its messages are answered one at a time, each by a new agent given the conversation so far.

- A conversation quiet for a day ends, so a changed file applies to the next, and the next message starts afresh.
- A new conversation on Slack, GitLab or Jira reads what the thread or issue held before the message: the latest 20 entries, at most 8000 characters, from the first 1000 of a Slack thread. A machine that gives no `conversation` is given it for each message. Telegram keeps nothing a bot can read, so one there starts with nothing.
- History holds what was said, and no tool calls or results. A read that fails gives no history, and the message is still answered.
- Rook reads nothing for a person with no agent.
- A conversation waits while its message waits, approvals included, up to seven days.
- An answer that fails is told so, and the conversation starts afresh.
- A person with no agent is told the bot is not theirs.

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
| `.rook/blocks/ask.yaml` | `to`, `text`, `choices` | asks people, and gives `choice` |

An address is a mapping: `{ bridge, id }`, with `answers`, `at` and `settings` where a bridge uses them. `chat.yaml` gives one for a message, and `lookup.yaml`, with `do: reach`, gives one for each person. Pass it whole.

`ask.yaml` takes a list of people's addresses from `lookup.yaml`, since a tap counts by a person's id. A message's address can be a channel or an issue, so it goes to `say.yaml` only.

```yaml
# A machine in schedules/ tells a person.
job: { machine: .rook/blocks/tell.yaml, input: { person: alice, text: The backup failed. } }

# A machine answering a message says something while it works.
job: { machine: .rook/blocks/say.yaml, input: { address: "${input.address}", text: Working on it. } }

# An agent's tool does the same, and its model chooses only the text.
tools:
  progress:
    does: Say how the work is going.
    args: { text: string }
    job: { machine: .rook/blocks/say.yaml, input: { address: "${input.address}", text: "${args.text}" } }
```

The tool above needs no `approve: true`, since its text goes to those who read the answer. A tool whose arguments name a person does. Give a model no address as an argument: `settings.site` is the host a bridge sends its token to.

A person's reply to a text goes to `chat.yaml` as a new message, and the agent there does not know the text. Only a tap on a question goes back to the machine that asked.

## Asking a person

A question is a run with a key, named on its buttons, so a press goes to it. `ask.yaml` gives the key, and `to` lists addresses, so each person is asked on their own bridge.

- Everyone in `to` is asked, and the first answer counts. Unanswered after seven days, the choice is `no`.
- An answer counts only from someone in `to`, by bridge and id.
- `lookup.yaml` gives the first bridge in a person's entry that can reach them directly. A person with none cannot approve: the ask fails, and the call does not run.

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
include: ./base.yaml
open:
  messages: { include: parts/messages/part.yaml }
```

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
- `approve:` names who answers each part's held calls, each a person or a group.

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
