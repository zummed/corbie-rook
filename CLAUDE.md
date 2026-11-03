# Rook

Rook is ready-made Corbie machines: bridges for Telegram, Slack, GitLab and Jira, and the blocks that answer what people ask on them. The program `corbie-rook` writes them into a project. `corbie run submit` brings a project's runs into line with its files, and `corbie ui` shows them.

## Where to start

- `DESIGN.md`: the design. It and `README.md` are pages of the manual the program carries.
- `rook.txt`: the page a model is told when it connects to `corbie rook mcp`.
- `shipped/`: what is written into a project's `.rook/`. `starter/`: a project's first files. `bridges/`: the file that starts each bridge.
- `rook.go`: what writes them.

## What holds

- **Two ideas**: machine and block. `people.yaml` and `rules.yaml` are data that machines read, and `.rook/agents/base.yaml` and the files in `parts/` are fields an agent includes. Add no other idea a person must learn.
- **Five slots**: a project fills bridges, people, attend, rules and parts. Design a piece as one way of filling a slot, and never around one project. Rook's ready-made way to fill a slot is in `shipped/<slot>/`.
- **A message is one mapping**, the same from every bridge, given in `DESIGN.md`. A block takes it or a part of it, and gives data back. A bridge only translates between its service and that mapping.
- **A project's files hold choices; `.rook/` holds what Rook does.** Rook never updates a project's file, so anything Rook may change goes in `.rook/`. `.rook/starter/` lets a project's files be compared with this version's.
- **Nothing personal lives here.** No real name, address, ID or host goes in this repository. A person's own machines live in their own directory.
- **The program does one thing**: writes machines into a project. Everything else is written in machines, which the person can change.
- **A person changes nothing in `.rook/`.** Rook rewrites it, and changes no other file.
- **Rook holds no secret.** Every service is reached by a bridge's machines, whose secrets Corbie keeps.
- Corbie stays simple and generic, and gains nothing that serves Rook alone.

## What the code is for

In this order:

1. Simple. Drop anything that can be dropped.
2. One path to do each thing. Flag anything that adds a second.
3. Obvious logic. Prefer the plain way to the clever one.
4. Simple error handling. The layer that notices passes the error up untouched; one outermost place per message, run or command reports everything that went wrong together. Nothing in between catches, retries or rewords.
5. Data flowing through functions, where that is sensible.

And these hold everywhere:

- Nothing important lives in memory. Everything is in Corbie's store or the directory.
- Security rests on a few rules that can be said in a sentence. Do not add a check where there is no real boundary, and do not add fiddly mechanism for a special case.
- If a normal person would need a special config, package or key to set something up, the design is wrong.

## Writing

These rules hold for everything a reader sees: the README, the design, what Rook says and prints, and reports to the author.

Words:

- Use the precise technical word. The developer's word is the plain word: process, file, directory, S3 bucket, network connection, permission, environment variable, default, timeout, poll, encrypted, signed.
- Name the real thing: the field (`allow:`, `job:`), the command, the status (`denied`), the path.
- No metaphor and no soft verbs. Not "decides", "reaches", "touches", "meets", "holds on to", "is owed", "stands", "says so". Not "unless it says": write "by default".
- One word for one thing, the same in the docs, the code and what is printed. A word with two meanings is a fault, and so is a second word for the same thing. A word is changed everywhere or not at all.
- Corbie's words are Rook's words. These are settled:
  - **machine**: a YAML definition of type flow, statechart or agent. Never a computer.
  - **job**: the `job:` block of a stage, a state, a tool or an ask. A job is a script, which a runner executes, or another machine, which the controller starts as a child run.
  - **run**: one execution of a machine, which a client starts, lists and shows.
  - **directory**, never "folder". **computer**, never "machine".
  - **controller**, **runner**, **client**: the three kinds of process. **store**: the directory or bucket they share.
- A page does not use words that only the code needs: pass, wake, mark, head, receipt, party. It says what happens instead.

Sentences:

- Few words. Every sentence states a fact the reader needs. If a sentence can be deleted with nothing lost, delete it. Say a thing once.
- Cut every word that carries no fact: "also", "that is", "which is", "in order to", a subject said again, a reason the reader does not need. After writing a paragraph, cut it by a third and check that no fact went.
- A paragraph is at most three sentences. Facts of the same kind go in a list, not in a paragraph.
- Choose the list by what its items are. A term and a few words about it (a field, a flag, a command, a path) is a row of two columns. Steps in order are a numbered list. Anything else is a dash list, and each item of it is a whole sentence with a subject and a verb: "When the script sends the handle to another host, Corbie refuses", never "Sent to another host: Corbie refuses". Do not set sentences in two columns.
- For example, this paragraph:

  "The bucket calls a function when a file is written to that function's inbox directory: the controller's function for the controller's inbox, and a runner's function for that runner's inbox. The function processes everything that is waiting and returns. A schedule also calls the controller's function once a minute."

  is written as:

  ```
    a file written to the controller's inbox   the controller's function
    a file written to a runner's inbox         that runner's function
    a schedule, once a minute                  the controller's function, for timers

  A function handles everything that is waiting, then exits.
  ```
- Say who does what: "Rook reads the directory, then starts the run."
- Whole sentences. No slogans, no two-part titles, no comma fragments. A title is a short name.
- A row in a list is one line. A fact added to a row is a few words in that line, not another sentence.
- Never claim more than is true. Check a statement against the code, or run it.
- No diagram drawn in text: no boxes, lines or arrows made of characters. They confuse the author every time. Say the same thing as a short table or a numbered list of one-line steps, and do not let the replacement grow longer than the diagram was.

## Working on the code

- Run Go through mise, which pins the version: `mise exec -- go …`. `mise run check` vets and tests.
- Pure Go, no cgo. Tests need no network, no container, no key and no model: they run a whole Corbie in the test's process with a stand-in model.
- One package per area, named as the area is.
- Until version 1, nothing is kept backwards compatible.

## Docs

Docs say how things are now. No history, no "previously", no alternatives. Something removed is simply gone.

## Commits

- The message starts with the area.
- Never push straight to `main`: push a branch.
