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

`rook init` lists the secrets to set. For Slack, make the app first, as [The Slack app](#the-slack-app) says. Then change `people.yaml` and `rules.yaml`, and run:

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

`.rook/` holds Rook's machines. Change nothing there: `corbie rook init` rewrites it, so run that after updating Rook. It changes no other file, and writes any file `.rook/` needs that the project lacks.

Your files are yours, and Rook never updates them. After updating Rook, `corbie rook init` names your files that are not as this version starts a project, and `corbie rook manual rook-starter` shows how it starts them. Take what you want of them, and run `corbie run submit . --check`.

## Common changes

- **Add a bridge:** run `corbie rook init <project> --bridges <name>`, then set its secrets. Delete its file and submit again to stop it.
- **Add a tool:** add a part under `parts/`, and list it under `open:` in an agent's file. Mark a tool that changes something `approve: true`, and name who approves it in `rules.yaml`: people, asked each in a direct message, or `{ people: [team], in: "#approvals" }`, asked in a Slack channel where anyone in `team` may answer.
- **Add an agent:** add its file to `agents/`, add it to `rules.yaml`, and add a stage for it in `answer.yaml`.
- **Answer with your own machine:** add it as an agent is added. It gets the message and gives a `reply`, as the design says under Your machines.
- **Tell someone from a machine:** run `.rook/blocks/tell.yaml` as a job, with `person` and `text`. `say.yaml` says at an address, and `ask.yaml` asks a question.
- **Run something at a time:** add a statechart with a timer under `schedules/`, and run `corbie run submit bridges schedules`. See `corbie manual schedules`.
- **Take people from elsewhere:** change the job in `lookup.yaml`.
- **Answer in a thread without being named:** add `overhear: true` to the input in `bridges/slack.yaml`. Each reply in a thread the bot has said something in goes to `attend.yaml`, which says no until you change it. On GitLab and Jira, `overhear: true` sends every issue and comment there.
- **Decide what is meant for the bot:** in `attend.yaml`, name `.rook/attend/model.yaml` to ask a small model, or your own machine. It gives `{ answer: true }` or `{ answer: false }`.
- **Wait for an issue to be finished:** add `settle: 2m` to the input in `bridges/gitlab.yaml` or `bridges/jira.yaml`. An opened issue is heard once nobody has changed it for two minutes.
- **Show a chart:** an agent writes a fenced `chart` block, as `${input.format}` in its system text tells it. Slack draws it, and GitLab and Jira show a table.
- **Call GitLab or Jira from a part:** load `.rook/bridges/gitlab/api.js` or `.rook/bridges/jira/api.js` in the part's script.

## The Slack app

The Slack bridge needs a Slack app that is an agent and uses Socket Mode, so nothing on the internet connects to this computer.

1. At [api.slack.com/apps](https://api.slack.com/apps), choose Create New App, then From a manifest, and pick the workspace.
2. Choose JSON, and paste this manifest, with the name and description you want. Slack cannot turn an agent back into a plain app.
3. Under Basic Information, App-Level Tokens, make a token with the scope `connections:write`. Set it with `corbie controller secret set SLACK_APP_TOKEN`.
4. Under Install App, install the app. Set its Bot User OAuth Token with `corbie controller secret set SLACK_TOKEN`.
5. In `people.yaml`, give each person `slack:` with their member ID, from their profile's menu, Copy member ID.
6. In each channel the bot should hear, run `/invite @Rook`.

```json
{
  "display_information": {
    "name": "Rook"
  },
  "features": {
    "bot_user": {
      "display_name": "Rook",
      "always_online": true
    },
    "app_home": {
      "messages_tab_enabled": true,
      "messages_tab_read_only_enabled": false
    },
    "agent_view": {
      "agent_description": "Answers questions and does tasks for the people it belongs to."
    }
  },
  "oauth_config": {
    "scopes": {
      "bot": [
        "assistant:write",
        "chat:write",
        "im:write",
        "channels:read",
        "groups:read",
        "channels:history",
        "groups:history",
        "im:history"
      ]
    }
  },
  "settings": {
    "event_subscriptions": {
      "bot_events": [
        "message.channels",
        "message.groups",
        "message.im"
      ]
    },
    "interactivity": {
      "is_enabled": true
    },
    "socket_mode_enabled": true
  }
}
```

| Scope | What the bridge does with it |
|---|---|
| `assistant:write` | shows a status in a thread |
| `chat:write` | posts, and changes a question once it is answered |
| `im:write` | opens a direct message to ask a question |
| `channels:read`, `groups:read` | reads a channel's name, as `rules.yaml` names places |
| `channels:history`, `groups:history`, `im:history` | hears messages, and reads a thread's history |

The bot hears a direct message, and a message naming it in a channel it was invited to.

A GitLab project or a Jira project not listed under `places:` in `rules.yaml` counts as read by anyone, so only an agent `for: [anyone]` answers there.

`DESIGN.md` gives the design, and `corbie rook manual rook-design` prints it.

## License

Copyright 2026 Paul Young. Licensed under the Apache License, Version 2.0: see `LICENSE` and `NOTICE`.

Rook was written with Claude Code, designed and directed by Paul Young.
