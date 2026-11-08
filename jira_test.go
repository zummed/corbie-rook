package rook_test

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zummed/corbie/client"
)

// jiraWorld makes a project with a bot on a stand-in Jira, with the files
// given and the agent at agents/assistant standing in, and the token and
// values set. The project is not yet submitted.
func jiraWorld(t *testing.T, j *jira, files map[string]string) (*world, string) {
	t.Helper()
	w := newWorld(t)
	w.secret("JIRA_TOKEN", "jira-token")
	dir := pointed(t, "jira", j.Server)
	jiraAt(w, j)
	write(t, dir, files)
	return w, dir
}

// jiraHeard gives the message that started each conversation, in order, with
// its history read from its blob.
func jiraHeard(t *testing.T, w *world) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, run := range stubs(t, w, "conversation") {
		in, _ := w.show(run.ID).Input.(map[string]any)
		r, file, err := w.Fetch(context.Background(), run.ID, "input/history")
		if err != nil {
			t.Fatal(err)
		}
		var entries []map[string]any
		if err := json.NewDecoder(r).Decode(&entries); err != nil || file.Type != "application/json" {
			t.Fatalf("the history is a %s that gives %v", file.Type, err)
		}
		in["history"] = entries
		out = append(out, in)
	}
	return out
}

// lines gives the line of each entry of a history.
func lines(history any) []string {
	var out []string
	for _, e := range history.([]map[string]any) {
		out = append(out, e["line"].(string))
	}
	return out
}

// names gives the name of each file in a list of them.
func fileNames(files any) []string {
	var out []string
	list, _ := files.([]any)
	for _, f := range list {
		out = append(out, f.(map[string]any)["name"].(string))
	}
	return out
}

// ended waits for a run to end well, and gives its output.
func ended(w *world, id string) any {
	w.t.Helper()
	var run *client.Run
	w.until("the run to end", func() bool {
		run = w.show(id)
		return run.Status == "done" || run.Status == "failed"
	})
	if run.Status != "done" {
		w.t.Fatalf("the run %s: %s%s", run.Status, run.Error, run.Refused)
	}
	return run.Output
}

// A comment naming the bot on an issue is answered there, once, whoever wrote
// it; the bot's own words and a comment that names nobody are left alone.
func TestJiraIssuesAreAnswered(t *testing.T) {
	j := newJira(t)
	w, dir := jiraWorld(t, j, nil)
	w.sync(dir)
	w.kept(router("jira"))
	// The first ask marks the time, and the next is the first to read.
	tick(w, j.asked)
	at := time.Now().Add(time.Second)
	j.comment("1", "acc-1", "no name at all", at)
	j.comment("2", "acc-bot", "[~accountid:acc-bot] my own words", at)
	j.comment("3", "acc-1", "[~accountid:acc-bot] the build fails", at)
	j.comment("4", "acc-9", "[~accountid:acc-bot] hello", at.Add(time.Millisecond))
	tick(w, j.asked)
	w.until("two comments", func() bool { return len(j.saying()) >= 2 })
	tick(w, j.asked)
	tick(w, j.asked)
	if got := j.saying(); len(got) != 2 || !slices.Contains(got, "the build fails") || !slices.Contains(got, "hello") {
		t.Fatalf("the bot said %q", got)
	}
}

// With overhear, a comment that does not name the bot is sent to the router
// as any message is, with the person and the history, and marked unaddressed;
// the router starts no conversation for it.
func TestJiraOverheardCommentIsMarkedAndDropped(t *testing.T) {
	j := newJira(t)
	w, dir := jiraWorld(t, j, nil)
	setSite(t, dir, router("jira"), "input: { projects: [OPS] }", "input: { projects: [OPS], overhear: true }")
	w.sync(dir)
	routing := w.kept(router("jira"))
	tick(w, j.asked)
	at := time.Now().Add(time.Second)
	j.comment("1", "acc-1", "just noting", at)
	j.comment("2", "acc-1", "[~accountid:acc-bot] please look", at.Add(time.Millisecond))
	tick(w, j.asked)
	var heard []map[string]any
	w.until("both messages at the router", func() bool {
		heard = nil
		for _, e := range w.record(routing) {
			for _, ev := range e.Events {
				if m, ok := ev.Data.(map[string]any); ok && e.ID == routing && ev.Name == "message" {
					heard = append(heard, m)
				}
			}
		}
		return len(heard) >= 2
	})
	over, named := heard[0], heard[1]
	if over["unaddressed"] != true || over["text"] != "just noting" || over["name"] != "Al" || over["email"] != "al@example.com" || over["history"] == nil {
		t.Errorf("the overheard message is %v", over)
	}
	if named["unaddressed"] != nil || named["text"] != "please look" {
		t.Errorf("the named message is %v", named)
	}
	w.until("the answer", func() bool { return len(j.saying()) >= 1 })
	tick(w, j.asked)
	time.Sleep(10 * interval)
	if got := jiraHeard(t, w); len(got) != 1 || got[0]["text"] != "please look" {
		t.Errorf("the conversations started with %v", got)
	}
	if got := j.saying(); len(got) != 1 || got[0] != "please look" {
		t.Errorf("the bot said %q", got)
	}
}

// A message holds who wrote it, as Jira gives them, and the issue before it as
// history: the issue with its reporter, then each public comment before it,
// the bot's as you, and none after it. The person is read once.
func TestJiraMessageHoldsThePersonAndTheIssue(t *testing.T) {
	j := newJira(t)
	w, dir := jiraWorld(t, j, nil)
	w.sync(dir)
	w.kept(router("jira"))
	tick(w, j.asked)
	at := time.Now().Add(time.Second)
	j.comment("1", "acc-1", "an earlier word", at)
	j.comment("2", "acc-bot", "my earlier reply", at)
	j.comment("3", "acc-1", "a restricted word", at)
	j.mu.Lock()
	j.comments[2]["visibility"] = map[string]any{"type": "role", "value": "Administrators"}
	j.mu.Unlock()
	j.comment("4", "acc-1", "[~accountid:acc-bot] go on", at.Add(time.Second))
	j.comment("5", "acc-bot", "written later", at)
	tick(w, j.asked)
	w.until("the bot to answer", func() bool { return len(j.saying()) >= 1 })
	j.comment("6", "acc-1", "[~accountid:acc-bot] and then", at.Add(2*time.Second))
	j.comment("7", "acc-9", "[~accountid:acc-bot] who am I?", at.Add(2*time.Second))
	tick(w, j.asked)
	w.until("acc-9's conversation", func() bool { return len(stubs(t, w, "conversation")) >= 2 })
	got := jiraHeard(t, w)
	m := got[0]
	for field, want := range map[string]any{"service": "jira", "place": "OPS", "chat": "OPS-12", "user": "acc-1", "name": "Al", "email": "al@example.com", "text": "go on", "complete": true} {
		if m[field] != want {
			t.Errorf("the message's %s is %v, not %v", field, m[field], want)
		}
	}
	if want := []string{"Al: Broken\n\nIt fails.", "acc-1: an earlier word", "you: my earlier reply"}; !slices.Equal(lines(m["history"]), want) {
		t.Errorf("the history is %q", lines(m["history"]))
	}
	if stranger := got[len(got)-1]; stranger["name"] != "Stranger" || stranger["email"] != "" || stranger["text"] != "who am I?" {
		t.Errorf("the stranger's message is %v", stranger)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if !slices.Equal(j.lookups, []string{"acc-1", "acc-9"}) {
		t.Errorf("Jira was asked who %q are", j.lookups)
	}
	w.titled(map[string][]string{"jira-listen": {"Jira listener"}})
}

// An issue with more comments than the listener reads gives the latest 50 and
// complete false, and still the issue's files.
func TestJiraLongIssueIsNotComplete(t *testing.T) {
	j := newJira(t)
	at := time.Now().Add(time.Second)
	j.files = []map[string]any{attachment("1", "shot.png", "image/png", 7, "acc-1", at.Add(-time.Hour))}
	w, dir := jiraWorld(t, j, nil)
	w.sync(dir)
	w.kept(router("jira"))
	tick(w, j.asked)
	for n := 2; n < 62; n++ {
		j.comment(strconv.Itoa(n), "acc-1", "comment "+strconv.Itoa(n), at.Add(-time.Minute))
	}
	j.comment("70", "acc-1", "[~accountid:acc-bot] what is wrong?", at)
	tick(w, j.asked)
	w.until("the answer", func() bool { return len(j.saying()) >= 1 })
	m := jiraHeard(t, w)[0]
	got := lines(m["history"])
	if m["complete"] != false || len(got) != 50 || got[1] != "acc-1: comment 13" || got[49] != "acc-1: comment 61" {
		t.Errorf("complete is %v, and the history holds %d lines: %q", m["complete"], len(got), got)
	}
	if files := m["history"].([]map[string]any)[0]["attachments"]; !slices.Equal(fileNames(files), []string{"shot.png"}) {
		t.Errorf("the issue's files are %v", files)
	}
	if got := j.downloaded(); len(got) != 0 {
		t.Errorf("Jira gave the files %v", got)
	}
}

// An opened issue that does not name the bot, with a comment that does made
// while it waits, is answered once it settles: the comment is heard.
func TestJiraHearsACommentMadeWhileItsIssueWaits(t *testing.T) {
	j := newJira(t)
	w, dir := jiraWorld(t, j, nil)
	setSite(t, dir, router("jira"), "projects: [OPS] }", "projects: [OPS], settle: 2m }")
	w.sync(dir)
	tick(w, j.asked)
	at := time.Now().Add(time.Second)
	j.mu.Lock()
	j.fields = map[string]any{"created": jiraTime(at), "updated": jiraTime(time.Now()), "description": "It fails."}
	j.mu.Unlock()
	j.comment("1", "acc-1", "[~accountid:acc-bot] can you look?", at.Add(time.Second))
	tick(w, j.asked)
	tick(w, j.asked)
	time.Sleep(10 * interval)
	if got := j.saying(); len(got) != 0 {
		t.Fatalf("the bot said %v before the issue settled", got)
	}
	j.mu.Lock()
	j.fields["updated"] = jiraTime(time.Now().Add(-10 * time.Minute))
	j.mu.Unlock()
	tick(w, j.asked)
	w.until("the answer", func() bool { return len(j.saying()) >= 1 })
	tick(w, j.asked)
	time.Sleep(10 * interval)
	if got := j.saying(); len(got) != 1 || !strings.Contains(got[0], "can you look?") {
		t.Fatalf("the bot said %v", got)
	}
}

// A comment made at the same moment as the last one heard is heard, and the one
// before it is not heard twice.
func TestJiraHearsTwoCommentsAtOneMoment(t *testing.T) {
	j := newJira(t)
	w, dir := jiraWorld(t, j, nil)
	w.sync(dir)
	tick(w, j.asked)
	at := time.Now().Add(time.Second)
	j.comment("1", "acc-1", "[~accountid:acc-bot] first", at)
	tick(w, j.asked)
	w.until("the first comment", func() bool { return len(j.saying()) >= 1 })
	j.comment("2", "acc-1", "[~accountid:acc-bot] second", at)
	tick(w, j.asked)
	w.until("the second comment", func() bool { return len(j.saying()) >= 2 })
	tick(w, j.asked)
	time.Sleep(20 * interval)
	if got := j.saying(); len(got) != 2 || !strings.Contains(got[0], "first") || !strings.Contains(got[1], "second") {
		t.Fatalf("the bot said %v", got)
	}
}

// A restricted comment on Jira is not heard.
func TestRestrictedJiraCommentIsNotHeard(t *testing.T) {
	j := newJira(t)
	w, dir := jiraWorld(t, j, nil)
	w.sync(dir)
	w.kept(router("jira"))
	tick(w, j.asked)
	at := time.Now().Add(time.Second)
	j.comment("1", "acc-1", "[~accountid:acc-bot] restricted", at)
	j.comment("2", "acc-1", "[~accountid:acc-bot] internal", at)
	j.comment("3", "acc-1", "[~accountid:acc-bot] open to all", at.Add(time.Second))
	j.mu.Lock()
	j.comments[0]["visibility"] = map[string]any{"type": "role", "value": "Administrators"}
	j.comments[1]["jsdPublic"] = false
	j.mu.Unlock()
	tick(w, j.asked)
	w.until("the bot to answer", func() bool { return len(j.saying()) >= 1 })
	time.Sleep(20 * interval)
	if got := j.saying(); len(got) != 1 || !strings.Contains(got[0], "open to all") {
		t.Fatalf("the bot said %v", got)
	}
}

// Two Jira projects are asked about in one search, and a comment's place is
// its project's key.
func TestJiraTwoProjectsAreAsked(t *testing.T) {
	j := newJira(t)
	var mu sync.Mutex
	var jqls []string
	override(j.Server, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/rest/api/2/search/jql" {
			mu.Lock()
			jqls = append(jqls, r.URL.Query().Get("jql"))
			mu.Unlock()
		}
		return false
	})
	w, dir := jiraWorld(t, j, nil)
	setSite(t, dir, router("jira"), "projects: [OPS]", "projects: [OPS, DEV]")
	w.sync(dir)
	tick(w, j.asked)
	tick(w, j.asked)
	mu.Lock()
	defer mu.Unlock()
	if len(jqls) == 0 || !strings.Contains(jqls[len(jqls)-1], `project in ("OPS", "DEV")`) {
		t.Fatalf("Jira was asked %q", jqls)
	}
}

// An issue opened naming the bot is heard as it is, and answered on the issue.
// Its history holds no entry: the message is the issue.
func TestJiraOpenedIssueNamingTheBotIsAnswered(t *testing.T) {
	j := newJira(t)
	w, dir := jiraWorld(t, j, nil)
	w.sync(dir)
	tick(w, j.asked)
	at := time.Now().Add(time.Second)
	j.mu.Lock()
	j.fields = map[string]any{"created": jiraTime(at), "updated": jiraTime(time.Now().Add(-time.Hour)), "description": "[~accountid:acc-bot] the disk is full"}
	j.mu.Unlock()
	tick(w, j.asked)
	w.until("the answer", func() bool {
		w.set(w.now().Add(31 * time.Second))
		time.Sleep(5 * interval)
		return len(j.saying()) >= 1
	})
	tick(w, j.asked)
	time.Sleep(10 * interval)
	if got := j.saying(); len(got) != 1 || !strings.Contains(got[0], "the disk is full") || strings.Contains(got[0], "accountid") {
		t.Fatalf("the bot said %q", got)
	}
	if m := jiraHeard(t, w)[0]; len(m["history"].([]map[string]any)) != 0 || m["complete"] != true || m["user"] != "acc-1" {
		t.Errorf("the message is %v", m)
	}
}

// A project with no JIRA_EMAIL: the listener fails naming it, and Jira is not
// asked.
func TestJiraWithNoEmailNamesTheValue(t *testing.T) {
	j := newJira(t)
	w := newWorld(t)
	w.secret("JIRA_TOKEN", "jira-token")
	dir := pointed(t, "jira", j.Server)
	w.value("JIRA_SITE", strings.TrimPrefix(j.URL, "http://"))
	w.sync(dir)
	w.kept(router("jira"))
	w.until("the listener fails naming JIRA_EMAIL", func() bool {
		w.set(w.now().Add(31 * time.Second))
		time.Sleep(5 * interval)
		return strings.Contains(strings.Join(failures(w), "\n"), "JIRA_EMAIL")
	})
	if j.asked() != 0 {
		t.Errorf("Jira was asked %d times", j.asked())
	}
}

// attachment is one of the issue's files, made by author at a time.
func attachment(id, name, kind string, size int, author string, at time.Time) map[string]any {
	return map[string]any{"id": id, "filename": name, "mimeType": kind, "size": size, "created": jiraTime(at), "author": map[string]any{"accountId": author}}
}

// A comment's message has the files it names, and of two with one name the
// newest. An external image names no file. The issue's files are in the
// history, with the issue. attachment/get downloads each, except one over
// 32 MiB, which it gives without its contents.
func TestJiraCommentHasTheFilesItNames(t *testing.T) {
	at := time.Now().Add(time.Second)
	j := newJira(t)
	j.files = []map[string]any{
		attachment("1", "cat.png", "image/png", 7, "acc-1", at.Add(-2*time.Second)),
		attachment("2", "cat.png", "image/png", 7, "acc-1", at.Add(-time.Second)),
		attachment("4", "dog.png", "image/png", 3, "acc-1", at.Add(time.Second)),
		attachment("7", "bird.png", "image/png", 4, "acc-1", at),
		attachment("5", "x.png", "image/png", 1, "acc-1", at),
		attachment("6", "film.mp4", "video/mp4", 64<<20, "acc-1", at),
	}
	j.contents = map[string]string{"1": "old cat", "2": "new cat", "4": "dog", "5": "x", "7": "bird"}
	w, dir := jiraWorld(t, j, nil)
	w.sync(dir)
	tick(w, j.asked)
	j.comment("1", "acc-1", "[~accountid:acc-bot] wow! !cat.png! and !dog.png|thumbnail! and !https://example.com/x.png! [^film.mp4]", at)
	tick(w, j.asked)
	w.until("the answer", func() bool { return len(j.saying()) >= 1 })
	m := jiraHeard(t, w)[0]
	if got := fileNames(m["attachments"]); !slices.Equal(got, []string{"cat.png", "film.mp4", "dog.png"}) {
		t.Fatalf("the message has the files %v", got)
	}
	if got := m["history"].([]map[string]any)[0]["attachments"]; !slices.Contains(fileNames(got), "bird.png") {
		t.Errorf("the issue's files are %v", got)
	}
	// The conversation downloaded them before it answered.
	before := len(j.downloaded())
	id := w.start(bot+"/jira/attachment/get.yaml", map[string]any{"attachments": m["attachments"]})
	got := ended(w, id).([]any)
	if downloaded := j.downloaded()[before:]; !slices.Equal(slices.Sorted(slices.Values(downloaded)), []string{"2", "4"}) {
		t.Errorf("Jira gave the files %v", downloaded)
	}
	film := got[1].(map[string]any)
	if film["file"] != nil || film["size"] != float64(64<<20) || got[0].(map[string]any)["file"] == nil {
		t.Errorf("attachment/get gave %v", got)
	}
}

// A comment that only names the bot and links a file is answered, and its
// message has the file.
func TestJiraFileAloneIsAnswered(t *testing.T) {
	at := time.Now().Add(time.Second)
	j := newJira(t)
	j.files = []map[string]any{attachment("10001", "log.txt", "text/plain", 9, "acc-1", at)}
	w, dir := jiraWorld(t, j, nil)
	w.sync(dir)
	tick(w, j.asked)
	j.comment("1", "acc-1", "[~accountid:acc-bot] [^log.txt]", at)
	tick(w, j.asked)
	w.until("the answer", func() bool { return len(j.saying()) >= 1 })
	if m := jiraHeard(t, w)[0]; m["text"] != "[^log.txt]" || !slices.Equal(fileNames(m["attachments"]), []string{"log.txt"}) {
		t.Fatalf("the message is %v", m)
	}
}

// An opened issue's message has its files, whoever attached them, except each
// one a restricted comment names, even where the description names it too.
func TestJiraIssueHasItsFiles(t *testing.T) {
	at := time.Now().Add(time.Second)
	j := newJira(t)
	j.files = []map[string]any{
		attachment("1", "mine.png", "image/png", 4, "acc-1", at),
		attachment("2", "theirs.png", "image/png", 6, "acc-2", at),
		attachment("3", "secret.png", "image/png", 6, "acc-1", at),
		attachment("4", "both.png", "image/png", 4, "acc-1", at),
	}
	w, dir := jiraWorld(t, j, nil)
	w.sync(dir)
	tick(w, j.asked)
	j.mu.Lock()
	j.fields = map[string]any{"created": jiraTime(at), "description": "[~accountid:acc-bot] it fails !both.png!", "comment": map[string]any{"comments": []any{
		map[string]any{"id": "5", "body": "!secret.png! [^both.png]", "created": jiraTime(at.Add(time.Second)), "author": map[string]any{"accountId": "acc-1"}, "visibility": map[string]any{"type": "role", "value": "Administrators"}},
	}}}
	j.mu.Unlock()
	tick(w, j.asked)
	w.until("the answer", func() bool { return len(j.saying()) >= 1 })
	if got := fileNames(jiraHeard(t, w)[0]["attachments"]); !slices.Equal(got, []string{"mine.png", "theirs.png"}) {
		t.Fatalf("the message has the files %v", got)
	}
}

// The Jira token goes only to JIRA_SITE: a file's download that Jira would
// redirect elsewhere is asked without a redirect, and nothing is asked of the
// other host.
func TestJiraTokenGoesOnlyToTheSite(t *testing.T) {
	j := newJira(t)
	seen := &headers{}
	seen.wrap(j.Server)
	j.contents = map[string]string{"100": "hello"}
	w, dir := jiraWorld(t, j, nil)
	w.sync(dir)
	id := w.start(bot+"/jira/attachment/get.yaml", map[string]any{"attachments": []any{map[string]any{"name": "log.txt", "type": "text/plain", "size": 5, "id": "100"}}})
	ended(w, id)
	for _, line := range seen.all() {
		if strings.Contains(line, "/attachment/content/") && !strings.Contains(line, "auth=Basic") {
			t.Errorf("a download was asked without the token: %s", line)
		}
	}
	if got := j.downloaded(); len(got) != 1 {
		t.Errorf("downloaded %v", got)
	}
}

// issue/attachments/create attaches a file and writes a comment that shows it:
// a PNG, JPEG or GIF as a picture, and any other file as a link. A name is sent
// with each [, ], |, ! and ^ as _, and the comment names the file as Jira
// keeps it.
func TestJiraAttachedFileIsShownInAComment(t *testing.T) {
	for _, c := range []struct{ name, stored, contents, sent, said string }{
		{"report.csv", "", "a,b\n1,2\n", "report.csv", "[^report.csv]"},
		{"cat.png", "", "\x89PNG picture", "cat.png", "!cat.png!"},
		{"logo.svg", "", "<svg/>", "logo.svg", "[^logo.svg]"},
		{"report [1].csv", "report (1).csv", "a,b\n", "report _1_.csv", "[^report (1).csv]"},
	} {
		t.Run(c.name, func(t *testing.T) {
			j := newJira(t)
			j.stored = c.stored
			w, dir := jiraWorld(t, j, map[string]string{bot + "/jira/issue/attachments/create.yaml": "include: .rook/jira/issue/attachments/create.yaml"})
			w.sync(dir)
			id := w.start(bot+"/jira/issue/attachments/create.yaml", map[string]any{"issue": "OPS-12", "name": c.name, "file": client.InputFile{Name: c.name, Contents: strings.NewReader(c.contents)}})
			if out := ended(w, id); out.(map[string]any)["sent"] != "comment:77" {
				t.Errorf("it gave %v", out)
			}
			if got := j.attached(); len(got) != 1 || got[0].Name != c.sent || got[0].Body != c.contents || got[0].To != "OPS-12" {
				t.Errorf("the bot attached %+v", got)
			}
			if got := j.saying(); !slices.Equal(got, []string{c.said}) {
				t.Errorf("the bot said %q", got)
			}
		})
	}
}

// A file Jira refuses fails the call with Jira's status, and is named in no
// comment.
func TestJiraRefusedFileIsNotNamed(t *testing.T) {
	j := newJira(t)
	j.largest = 4
	w, dir := jiraWorld(t, j, map[string]string{bot + "/jira/issue/attachments/create.yaml": "include: .rook/jira/issue/attachments/create.yaml"})
	w.sync(dir)
	id := w.start(bot+"/jira/issue/attachments/create.yaml", map[string]any{"issue": "OPS-12", "name": "report.csv", "file": client.InputFile{Name: "report.csv", Contents: strings.NewReader("a,b\n1,2\n")}})
	w.until("the call to fail", func() bool { return w.show(id).Status == "failed" })
	if got := w.show(id).Error; !strings.Contains(got, "Jira answered POST /issue/OPS-12/attachments with 413") {
		t.Errorf("the call failed with %s", got)
	}
	if got := j.saying(); len(got) != 0 {
		t.Errorf("the bot said %q", got)
	}
}

// issue/comment/list gives the issue and its public comments as text, and
// whether every comment was read.
func TestJiraCommentListReadsTheIssue(t *testing.T) {
	j := newJira(t)
	at := time.Now()
	j.comment("1", "acc-1", "an earlier word", at)
	j.comment("2", "acc-bot", "my reply", at)
	j.comment("3", "acc-1", "a restricted word", at)
	j.comments[2]["visibility"] = map[string]any{"type": "role", "value": "Administrators"}
	w, dir := jiraWorld(t, j, map[string]string{bot + "/jira/issue/comment/list.yaml": "include: .rook/jira/issue/comment/list.yaml"})
	w.sync(dir)
	out := ended(w, w.start(bot+"/jira/issue/comment/list.yaml", map[string]any{"issue": "OPS-12"})).(map[string]any)
	if want := "Al: Broken\n\nIt fails.\n\nacc-1: an earlier word\n\nyou: my reply"; out["text"] != want || out["complete"] != true {
		t.Errorf("it gave %v", out)
	}
}

// chat/post writes Markdown as Jira's wiki markup, and a chart as its table.
func TestJiraPostWritesWikiMarkup(t *testing.T) {
	j := newJira(t)
	w, dir := jiraWorld(t, j, nil)
	w.sync(dir)
	markdown := strings.Join([]string{
		"# Release",
		"Some **bold**, *italic*, _also italic_, ~~gone~~ and `a {b}` here.",
		"See [the docs](https://example.com/docs) or <https://example.com>, ![logo](https://example.com/l.png), [~accountid:acc-1] and [^log.txt].",
		"A [note] and a {macro}.",
		"",
		"- one",
		"  - one and a half",
		"- two",
		"1. first",
		"2. second",
		"",
		"> quoted **words**",
		"",
		"---",
		"",
		"| Name | Count |",
		"| --- | ---: |",
		"| a | 1 |",
		"",
		"```go",
		"x := *p ** 2",
		"```",
		"",
		"```chart",
		`{"type": "bar", "title": "Tickets", "labels": ["Jan"], "series": [{"name": "Email", "values": [140]}]}`,
		"```",
	}, "\n")
	ended(w, w.start(bot+"/jira/chat/post.yaml", map[string]any{"in": "OPS-12", "text": markdown}))
	want := strings.Join([]string{
		"h1. Release",
		"Some *bold*, _italic_, _also italic_, -gone- and {{a \\{b\\}}} here.",
		"See [the docs|https://example.com/docs] or [https://example.com], !https://example.com/l.png!, [~accountid:acc-1] and [^log.txt].",
		"A \\[note] and a \\{macro}.",
		"",
		"* one",
		"** one and a half",
		"* two",
		"# first",
		"# second",
		"",
		"bq. quoted *words*",
		"",
		"----",
		"",
		"||Name||Count||",
		"|a|1|",
		"",
		"{noformat}",
		"x := *p ** 2",
		"{noformat}",
		"",
		"*Tickets*",
		"",
		"||||Email||",
		"|Jan|140|",
	}, "\n")
	if got := j.saying(); len(got) != 1 || got[0] != want {
		t.Errorf("the bot said\n%s\nnot\n%s", strings.Join(got, "\n---\n"), want)
	}
}

// questionIn gives the run ID a question's comment shows, once the bot has
// written it.
func questionIn(w *world, j *jira) string {
	w.t.Helper()
	var id string
	w.until("the question", func() bool {
		for _, said := range j.saying() {
			if m := regexp.MustCompile(`\{\{yes (\S+)\}\}`).FindStringSubmatch(said); m != nil {
				id = m[1]
			}
		}
		return id != ""
	})
	return id
}

// approvingOnJira makes a project whose agents/approve is Rook's approval,
// starts it for a held call, asking on the issue in, and gives its run.
// Jira's people include alice, who may approve, and bob, who asked.
func approvingOnJira(t *testing.T, j *jira, in, within string) (*world, string) {
	t.Helper()
	j.mu.Lock()
	j.people["acc-a"] = map[string]any{"accountId": "acc-a", "displayName": "Alice A.", "emailAddress": "Alice@Example.com"}
	j.people["acc-b"] = map[string]any{"accountId": "acc-b", "displayName": "Bob B.", "emailAddress": "bob@example.com"}
	j.mu.Unlock()
	w, dir := jiraWorld(t, j, map[string]string{"agents/approve.yaml": "include: .rook/approval.yaml"})
	w.sync(dir)
	w.kept(router("jira"))
	tick(w, j.asked)
	input := map[string]any{
		"asking": map[string]any{}, "jobs": []any{map[string]any{"name": "restart", "asked": map[string]any{"description": "Restarts a VM.", "args": map[string]any{"vm": "web-1"}, "said": "Restarting it."}}},
		"ask": []any{map[string]any{"at": bot + "/jira/chat/ask", "in": in}}, "approvers": "messages:approve", "asker": "bob@example.com", "request": "please restart web-1",
	}
	if within != "" {
		input["within"] = within
	}
	return w, w.start("agents/approve.yaml", input)
}

// A question on Jira is a comment with a line for each choice. A comment that
// holds only such a line answers it: one by the asker, by someone without the
// approvers' scope is refused in a comment naming them, one with no such choice
// is told nothing, and the first that counts ends it, whatever case it is
// written in. No answer starts a conversation.
func TestJiraQuestionIsAnsweredByAComment(t *testing.T) {
	j := newJira(t)
	w, approval := approvingOnJira(t, j, "OPS-12", "")
	question := questionIn(w, j)
	if asked := j.saying()[0]; !strings.Contains(asked, "A machine wants to run restart.") || !strings.Contains(asked, "Asked by bob@example.com.") || !strings.Contains(asked, "bq. please restart web-1") ||
		!strings.Contains(asked, "* {{no "+question+"}}") || strings.Count(asked, question) != 2 {
		t.Errorf("the question is %q", asked)
	}
	at := time.Now().Add(time.Second)
	answer := func(id, who, text, want string) {
		t.Helper()
		j.comment(id, who, text, at)
		at = at.Add(time.Second)
		before := len(j.saying())
		tick(w, j.asked)
		w.until(want, func() bool { return len(j.saying()) > before })
		if got := j.saying()[before]; got != want {
			t.Fatalf("the bot said %q, not %q", got, want)
		}
	}
	answer("11", "acc-b", "yes "+question, "[~accountid:acc-b], you asked for this, so someone else must answer it.")
	answer("12", "acc-1", "[~accountid:acc-bot] yes "+question, "[~accountid:acc-1], only someone with the scope messages:approve can answer this.")
	j.comment("13", "acc-a", "maybe "+question, at)
	at = at.Add(time.Second)
	said := len(j.saying())
	tick(w, j.asked)
	tick(w, j.asked)
	time.Sleep(10 * interval)
	if got := j.saying(); len(got) != said {
		t.Errorf("a choice that is none of the question's was told %q", got[said:])
	}
	answer("14", "acc-a", " {{Yes "+question+"}} ", "alice answered yes.")
	out := ended(w, approval).(map[string]any)
	if !slices.Equal(out["approved"].([]any), []any{"restart"}) || out["note"] != "alice said yes." {
		t.Errorf("the approval gave %v", out)
	}
	before := len(j.saying())
	j.comment("15", "acc-a", "no "+question, at)
	tick(w, j.asked)
	tick(w, j.asked)
	time.Sleep(10 * interval)
	if got := j.saying(); len(got) != before {
		t.Errorf("after the question ended the bot said %q", got[before:])
	}
	if got := stubs(t, w, "conversation"); len(got) != 0 {
		t.Errorf("an answer started %d conversations", len(got))
	}
}

// An answer counts only on the question's own issue. A stranger copies the
// question on OPS-12 into a comment on OPS-99, and alice, who may approve,
// answers it there: the question is told nothing and waits on, and alice's
// answer on OPS-12 ends it.
func TestJiraAnswerOnAnotherIssueDoesNotCount(t *testing.T) {
	j := newJira(t)
	w, approval := approvingOnJira(t, j, "OPS-12", "")
	question := questionIn(w, j)
	at := time.Now().Add(time.Second)
	j.commentOn("OPS-99", "901", "acc-9", "Close the duplicate OPS-99?\n\nTo answer, write a comment that holds only one of these lines:\n\n- `yes "+question+"`\n- `no "+question+"`", at)
	j.commentOn("OPS-99", "902", "acc-a", "yes "+question, at.Add(time.Second))
	said := len(j.saying())
	tick(w, j.asked)
	tick(w, j.asked)
	time.Sleep(10 * interval)
	if got := j.saying(); len(got) != said {
		t.Fatalf("an answer on OPS-99 was told %q", got[said:])
	}
	if got := stubs(t, w, "press"); len(got) != 0 {
		t.Fatalf("an answer on OPS-99 was checked %d times", len(got))
	}
	j.comment("11", "acc-a", "yes "+question, at.Add(2*time.Second))
	tick(w, j.asked)
	w.until("the question closed", func() bool { return len(j.saying()) > said })
	if got := j.saying()[said]; got != "alice answered yes." {
		t.Errorf("the bot said %q", got)
	}
	if out := ended(w, approval).(map[string]any); !slices.Equal(out["approved"].([]any), []any{"restart"}) {
		t.Errorf("the approval gave %v", out)
	}
}

// A question asked on an issue by its key in another case, or by its ID, is
// asked on the issue's key, and a comment there answers it.
func TestJiraQuestionInAnyCaseOrByIDIsAnswered(t *testing.T) {
	for _, in := range []string{"ops-12", "10"} {
		t.Run(in, func(t *testing.T) {
			j := newJira(t)
			w, approval := approvingOnJira(t, j, in, "")
			question := questionIn(w, j)
			said := len(j.saying())
			j.comment("11", "acc-a", "yes "+question, time.Now().Add(time.Second))
			tick(w, j.asked)
			w.until("the question closed", func() bool { return len(j.saying()) > said })
			if got := j.saying()[said]; got != "alice answered yes." {
				t.Errorf("the bot said %q", got)
			}
			if out := ended(w, approval).(map[string]any); !slices.Equal(out["approved"].([]any), []any{"restart"}) {
				t.Errorf("the approval gave %v", out)
			}
		})
	}
}

// A question no comment answers in time is no, and a last comment says so.
func TestJiraQuestionWithNoAnswerIsNo(t *testing.T) {
	j := newJira(t)
	w, approval := approvingOnJira(t, j, "OPS-12", "1h")
	questionIn(w, j)
	w.set(w.now().Add(61 * time.Minute))
	out := ended(w, approval).(map[string]any)
	if len(out["approved"].([]any)) != 0 || out["note"] != "No one answered in time, so the answer is no." {
		t.Errorf("the approval gave %v", out)
	}
	if got := j.saying(); got[len(got)-1] != "No one answered in time, so the answer is no." {
		t.Errorf("the bot said %q", got)
	}
}
