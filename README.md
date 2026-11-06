# Rook

Rook is ready-made [Corbie](https://github.com/zummed/corbie) machines for a bot that answers people on Telegram, Slack, GitLab issues and Jira issues, with agents, approvals and a list of who gets what. `corbie rook init` writes them into a project's `.rook/`, Corbie runs them, and the project's own files say who gets which agent and what each agent can do.

## Start

Install [Corbie](https://github.com/zummed/corbie) and [corbie-ui](https://github.com/zummed/corbie-ui), then Rook:

```sh
go install github.com/zummed/corbie-rook/cmd/corbie-rook@latest
```

Make a project, and read its page:

```sh
corbie init --ceiling all
corbie rook init my-bot --bridges slack
corbie rook manual rook-project
```

The manual has three pages of Rook's:

| Page | What it gives |
|---|---|
| `rook-project` | making a project, what to run after a change, and the common changes |
| `rook-design` | the runs that answer a message, what each block takes and gives, and who may do what |
| `rook-starter` | the files this version starts a project with |

`corbie run submit .` in the project records it in Corbie as one tree, `/my-bot`, and starts its bridges. A message goes from the bridge to a chat for its thread, then to a conversation for that thread and agent, which calls the agent. Each run names the run whose send or call started it, so a message can be followed to its agent, tools and approvals.

A new project has no agent and no part, and Rook needs no model: an agent names its own, or is a machine with none. To have a model add them, start Claude Code or another model in the project and say what the bot should do. The project's `.mcp.json` gives it Corbie's manual and `corbie test`, and its `AGENTS.md` tells any model how the project is changed.

## License

Copyright 2026 Paul Young. Licensed under the Apache License, Version 2.0: see `LICENSE` and `NOTICE`.

Rook was written with Claude Code, designed and directed by Paul Young.
