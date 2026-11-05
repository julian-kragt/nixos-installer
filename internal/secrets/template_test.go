package secrets

import (
	"slices"
	"strings"
	"testing"
)

func keyPaths(t *Template) []string {
	var out []string
	for _, k := range t.Keys {
		out = append(out, k.Path.String())
	}
	return out
}

func TestParseTemplate(t *testing.T) {
	tmpl, err := ParseTemplate([]byte(`# guidance
hashedPassword: CHANGEME
db:
  password: change_me
  host: db.local
  admin:
    hashedPassword: CHANGEME
servers:
  - a
  - b
cert: |
  change_me
empty: {}
`))
	if err != nil {
		t.Fatal(err)
	}
	want := []TemplateKey{
		{Path: Path{"hashedPassword"}, Kind: Password},
		{Path: Path{"db", "password"}, Kind: Secret},
		{Path: Path{"db", "host"}, Kind: Literal},
		{Path: Path{"db", "admin", "hashedPassword"}, Kind: Password},
		{Path: Path{"servers"}, Kind: Literal},
		{Path: Path{"cert"}, Kind: Secret},
		{Path: Path{"empty"}, Kind: Literal},
	}
	if !slices.EqualFunc(tmpl.Keys, want, func(a, b TemplateKey) bool { return a.Path.equal(b.Path) && a.Kind == b.Kind }) {
		t.Errorf("keys = %+v\nwant %+v", tmpl.Keys, want)
	}
	if !tmpl.NeedsEditor() {
		t.Error("NeedsEditor = false, want true")
	}
	if got := tmpl.Passwords(); len(got) != 2 || got[1].String() != "db.admin.hashedPassword" {
		t.Errorf("passwords = %v", got)
	}

	for _, bad := range []string{"", "- a\n", "a: 1\n\"x\\\"y\": 2\n", "a:\n  b]: c\n", "0: a\n", "a:\n  true: b\n"} {
		if _, err := ParseTemplate([]byte(bad)); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

func TestQuotedKeyThatLooksLikeANumber(t *testing.T) {
	tmpl, err := ParseTemplate([]byte("\"0\": a\n"))
	if err != nil || len(tmpl.Keys) != 1 || tmpl.Keys[0].Path.String() != "0" {
		t.Errorf("keys = %+v, %v; want the quoted key 0", tmpl, err)
	}
}

func TestUserAndHostTemplatesNeedNoEditor(t *testing.T) {
	for _, data := range []string{"hashedPassword: CHANGEME\n", "placeholder: unused\n"} {
		tmpl, err := ParseTemplate([]byte(data))
		if err != nil || tmpl.NeedsEditor() {
			t.Errorf("%q: NeedsEditor = %v, %v; want false", data, tmpl.NeedsEditor(), err)
		}
	}
}

func TestPathExtract(t *testing.T) {
	if got := (Path{"db", "0", "password"}).Extract(); got != `["db"]["0"]["password"]` {
		t.Errorf("extract = %s", got)
	}
}

func TestBufferAndDocument(t *testing.T) {
	tmpl, err := ParseTemplate([]byte("# keep this out\nhashedPassword: CHANGEME\ndb:\n  admin:\n    hashedPassword: CHANGEME\n  password: change_me # the db password\n"))
	if err != nil {
		t.Fatal(err)
	}
	buf, err := tmpl.Buffer("host-users/atlas-jkragt.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(buf), "hashedPassword") || !strings.Contains(string(buf), "host-users/atlas-jkragt.yaml") {
		t.Errorf("buffer:\n%s", buf)
	}

	edited := strings.Replace(string(buf), "password: change_me # the db password", "password: |\n    line one\n    line two", 1)
	content, err := tmpl.CheckEdited([]byte(edited))
	if err != nil {
		t.Fatalf("CheckEdited: %v\n%s", err, edited)
	}
	doc, err := Document(content,
		Assignment{Path{"hashedPassword"}, "$y$top"},
		Assignment{Path{"db", "admin", "hashedPassword"}, "$y$admin"})
	if err != nil {
		t.Fatal(err)
	}
	want := "db:\n  password: |\n    line one\n    line two\n  admin:\n    hashedPassword: $y$admin\nhashedPassword: $y$top\n"
	if string(doc) != want {
		t.Errorf("document:\n%s\nwant:\n%s", doc, want)
	}
}

func TestCheckEdited(t *testing.T) {
	tmpl, err := ParseTemplate([]byte("hashedPassword: CHANGEME\ndb:\n  password: change_me\n"))
	if err != nil {
		t.Fatal(err)
	}
	for edited, want := range map[string]string{
		"db:\n  password: change_me\n":           "db.password still holds a placeholder",
		"db:\n  password: \"\"\n":                "db.password is empty",
		"db:\n  pass: x\n":                       "db.password is missing; db.pass is not in the template",
		"db:\n  password: x\nextra: y\n":         "extra is not in the template",
		"not yaml: [":                            "did not find",
		"db:\n  password: s3cret\n":              "",
		"db:\n  password: s3cret\n# a comment\n": "",
	} {
		_, err := tmpl.CheckEdited([]byte(edited))
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("%q: err = %v, want %q", edited, err, want)
		}
		if err != nil && strings.Contains(err.Error(), "s3cret") {
			t.Errorf("%q: the error shows the value", edited)
		}
	}
}

func TestDocumentRefusesSopsKey(t *testing.T) {
	m, err := parseMapping([]byte("sops: x\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Document(m); err == nil {
		t.Error("want an error for a top-level sops key")
	}
}

func TestClassify(t *testing.T) {
	for v, want := range map[string]Class{
		"":                    ClassEmpty,
		"change_me":           ClassPlaceholder,
		"CHANGEME":            ClassPlaceholder,
		"x Change_Me y":       ClassPlaceholder,
		"$y$j9T$abc":          ClassCrypt,
		"$6$salt$hash":        ClassCrypt,
		"$2b$10$abc":          ClassCrypt,
		"$1$md5":              ClassValue,
		"hunter2":             ClassValue,
		"-----BEGIN KEY-----": ClassValue,
	} {
		if got := Classify(v); got != want {
			t.Errorf("Classify(%q) = %s, want %s", v, got, want)
		}
	}
}

func TestCheck(t *testing.T) {
	user := []TemplateKey{{Path: Path{"hashedPassword"}, Kind: Password}}
	host := []TemplateKey{{Path: Path{"db", "password"}, Kind: Secret}}
	tests := []struct {
		name   string
		keys   []TemplateKey
		fields []Field
		want   string // "" = ok
	}{
		{"user ok", user, []Field{{Path{"hashedPassword"}, ClassCrypt}}, ""},
		{"nested ok", host, []Field{{Path{"db", "password"}, ClassValue}}, ""},
		{"password not hashed", user, []Field{{Path{"hashedPassword"}, ClassValue}}, "not a crypt hash"},
		{"placeholder left", host, []Field{{Path{"db", "password"}, ClassPlaceholder}}, "placeholder"},
		{"empty", host, []Field{{Path{"db", "password"}, ClassEmpty}}, "empty"},
		{"missing", user, nil, "missing"},
		{"moved", host, []Field{{Path{"password"}, ClassValue}}, "db.password is missing; password is not in the template"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Check(tt.keys, tt.fields)
			if tt.want == "" && err != nil || tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Errorf("err = %v, want %q", err, tt.want)
			}
		})
	}
}
