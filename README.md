# Rook

Rook is ready-made [Corbie](https://github.com/zummed/corbie) machines for a bot that answers people on Telegram, Slack, GitLab issues and Jira issues, with agents, approvals and a list of who gets what. `corbie rook init` writes them into a project, Corbie keeps them running, and the machines are yours to read and change.

## Start

Install [Corbie](https://github.com/zummed/corbie) and [corbie-ui](https://github.com/zummed/corbie-ui), then Rook:

```sh
go install github.com/zummed/corbie-rook/cmd/corbie-rook@latest
```

Then:

```sh
corbie init --ceiling all
corbie rook init my-bot --bridges slack,jira,gitlab
```

`rook init` lists the secrets to set. Then change `people.yaml` and `rules.yaml`, and run:

```sh
cd my-bot
corbie start controller runner ui &
corbie run submit bridges
```

Run `corbie run submit bridges` again after any change. `corbie ui` shows what is running and what waits on a person. Its Parts page shows each part, what opens it, who that is for and who approves it.

## With a model

Corbie and Rook carry the manual, and serve it to a model. In the project's directory, run:

```sh
claude mcp add corbie -- corbie mcp --allow test
claude mcp add rook -- corbie rook mcp --allow init
```

A model connected to `rook` is told the page `corbie rook manual rook`, and reads the rest from there. Add `--allow "run submit"` to the first line for the model to check and start what it writes, and without it the model gives you the commands. A submitted machine may do anything the ceiling allows.

## What a project holds

| Slot | Your files | What they say |
|---|---|---|
| Bridges | `bridges/slack.yaml`, `bridges/jira.yaml`, … | a bridge runs while its file is there |
| People | `lookup.yaml`, `people.yaml` | who a bridge's id belongs to, and how to reach someone |
| Attend | `attend.yaml` | whether a message that does not name the bot is meant for it |
| Rules | `rules.yaml`, `answer.yaml` | which group gets which agent, and who approves what |
| Parts | `agents/`, `parts/` | the agents, and the tools each can open |

`.rook/` holds Rook's machines. Change nothing there: `corbie rook init` rewrites it, so run that after updating Rook. It changes no other file.

## Common changes

- **Add a bridge:** run `corbie rook init <project> --bridges <name>`, then set its secrets. Delete its file and submit again to stop it.
- **Give Slack its history:** the bot's token needs `channels:history`, `groups:history`, `im:history` and `mpim:history`. Without them a new conversation starts with nothing, and is still answered.
- **Give an older project history:** add `history` to `input:` and the prompt in `agents/base.yaml`, as `starter/agents/base.yaml` has them. Without it an agent is given the thread and does not show it to the model.
- **Add a tool:** add a part under `parts/`, and list it under `open:` in an agent's file. Mark a tool that changes something `approve: true`, and name who approves it in `rules.yaml`.
- **Add an agent:** add its file to `agents/`, add it to `rules.yaml`, and add a stage for it in `answer.yaml`.
- **Answer with your own machine:** add it as an agent is added. It gets the message and gives a `reply`, as the design says under Your machines.
- **Tell someone from a machine:** run `.rook/blocks/tell.yaml` as a job, with `person` and `text`. `say.yaml` says at an address, and `ask.yaml` asks a question.
- **Run something at a time:** add a statechart with a timer under `schedules/`, and run `corbie run submit bridges schedules`. See `corbie manual schedules`.
- **Take people from elsewhere:** change the job in `lookup.yaml`.
- **Answer in a thread without being named:** add `overhear: true` to the input in `bridges/slack.yaml`. Each reply in a thread the bot has said something in goes to `attend.yaml`, which by default asks a small model. On GitLab and Jira, `overhear: true` sends every issue and comment there.
- **Decide another way what is meant for the bot:** change the job in `attend.yaml`. It gives `{ answer: true }` or `{ answer: false }`.
- **Wait for an issue to be finished:** add `settle: 2m` to the input in `bridges/gitlab.yaml` or `bridges/jira.yaml`. An opened issue is heard once nobody has changed it for two minutes.
- **Show a chart:** an agent writes a fenced `chart` block, as the starter's `agents/base.yaml` tells it. Slack draws it, and the other bridges show a table.
- **Call GitLab or Jira from a part:** load `.rook/bridges/gitlab/api.js` or `.rook/bridges/jira/api.js` in the part's script.
- **Show a status on Slack:** declare the Slack app an agent, which gives it `assistant:write`. Slack shows a status only in a thread.

A GitLab project or a Jira project not listed under `places:` in `rules.yaml` counts as read by anyone, so only an agent `for: [anyone]` answers there.

`DESIGN.md` gives the design, and `corbie rook manual rook-design` prints it.

## License

Copyright 2026 Paul Young. Licensed under the Apache License, Version 2.0: see `LICENSE` and `NOTICE`.

Rook was written with Claude Code, designed and directed by Paul Young.
