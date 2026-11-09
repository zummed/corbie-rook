package test

// Rook's lookup, placed at people/find: who someone is, by their email, from
// the table people/scopes.yaml.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// found runs the lookup of a project with this table and input, and gives its
// output, or its error when it failed.
func found(t *testing.T, w *world, table string, input map[string]any) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "project")
	if err := exampleProject(dir, nil); err != nil {
		t.Fatal(err)
	}
	write(t, dir, map[string]string{"people/scopes.yaml": table})
	w.sync(dir)
	run, err := w.Wait(context.Background(), w.start("people/find.yaml", input), interval)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "done" {
		return run.Status + ": " + w.show(run.ID).Error
	}
	return fmt.Sprint(w.show(run.ID).Output)
}

// The lookup finds a person by their email, ignoring case, and gives the
// table's name, email and scopes. An email the table does not hold, or none,
// as Telegram gives, gives no scopes and the name the service gave, or with
// none the person's ID on the service. The service's ID never finds a row.
func TestLookupFindsAPersonByEmail(t *testing.T) {
	table := `
		ana@acme.example: { name: Ana, scopes: [orders:read, orders:write] }
		bo@acme.example:  { name: Bo,  scopes: [infra:read, infra:approve] }
		"12345": { name: Telegram, scopes: [infra:approve] }
		cy@acme.example: { name: Cy }
	`
	for _, c := range []struct {
		input map[string]any
		want  string
	}{
		{map[string]any{"service": "slack", "user": "U1", "name": "Bo B", "email": "Bo@Acme.Example"}, "map[email:bo@acme.example name:Bo scopes:[infra:read infra:approve]]"},
		{map[string]any{"service": "slack", "user": "U2", "name": "Eve", "email": "eve@acme.example"}, "map[email:eve@acme.example name:Eve scopes:[]]"},
		{map[string]any{"service": "telegram", "user": "12345", "name": "Tom"}, "map[email: name:Tom scopes:[]]"},
		{map[string]any{"service": "gitlab", "user": "7"}, "map[email: name:7 scopes:[]]"},
		{map[string]any{"email": "cy@acme.example"}, "map[email:cy@acme.example name:Cy scopes:[]]"},
	} {
		t.Run(fmt.Sprint(c.input["email"], c.input["user"]), func(t *testing.T) {
			w := newWorld(t)
			if got := found(t, w, table, c.input); got != c.want {
				t.Errorf("the lookup gave %s", got)
			}
		})
	}
}

// A table with no rows finds nobody: the starter's {}, and one holding only
// comments.
func TestLookupOfAnEmptyTable(t *testing.T) {
	for _, table := range []string{"{}", "# ana@acme.example: { name: Ana, scopes: [orders:read] }"} {
		w := newWorld(t)
		if got := found(t, w, table, map[string]any{"name": "Ana", "email": "ana@acme.example"}); got != "map[email:ana@acme.example name:Ana scopes:[]]" {
			t.Errorf("with %q the lookup gave %s", table, got)
		}
	}
}

// A table that is not one fails every lookup, saying where: a line YAML cannot
// read by its number, and a row that is not { name, scopes } by its email,
// whoever is looked up.
func TestLookupRefusesAMalformedTable(t *testing.T) {
	for _, c := range []struct{ table, want string }{
		{"ana@acme.example: { name: Ana }\nbo@acme.example: name: Bo\n", "line 2"},
		{"ana@acme.example: { name: Ana }\nbo@acme.example: { name: Bo, scopes: infra:read }\n", "the row of bo@acme.example is not"},
		{"bo@acme.example: [infra:read]\n", "the row of bo@acme.example is not"},
		{"bo@acme.example: { name: Bo, scope: [infra:read] }\n", "the row of bo@acme.example is not"},
		{"- bo@acme.example\n", "people/scopes.yaml is not a mapping"},
	} {
		w := newWorld(t)
		if got := found(t, w, c.table, map[string]any{"email": "ana@acme.example"}); !strings.HasPrefix(got, "failed: ") || !strings.Contains(got, c.want) {
			t.Errorf("with %q the lookup gave %s", c.table, got)
		}
	}
}
