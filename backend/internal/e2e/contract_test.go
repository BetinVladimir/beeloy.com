package e2e_test

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"beeloy.com/backend/internal/acl"
	"beeloy.com/backend/internal/server/servertest"
)

// tsInterfaces extracts the field names (and optionality) of every `export interface X { … }` of api-types.ts.
func tsInterfaces(t *testing.T) map[string]map[string]bool {
	raw, err := os.ReadFile("../../../apps/web/src/contracts/api-types.ts")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	out := map[string]map[string]bool{}
	head := regexp.MustCompile(`export interface (\w+)\s*\{`)
	for _, m := range head.FindAllStringSubmatchIndex(src, -1) {
		name := src[m[2]:m[3]]
		i, depth := m[1], 1
		start := i
		for ; i < len(src) && depth > 0; i++ {
			switch src[i] {
			case '{':
				depth++
			case '}':
				depth--
			}
		}
		body := src[start : i-1]
		fields := map[string]bool{} // name -> optional
		d, tokStart := 0, 0
		flush := func(end int) {
			tok := strings.TrimSpace(body[tokStart:end])
			if tok == "" {
				return
			}
			if k := regexp.MustCompile(`^(\w+)(\??):`).FindStringSubmatch(tok); k != nil {
				fields[k[1]] = k[2] == "?"
			}
		}
		for j := 0; j < len(body); j++ {
			switch body[j] {
			case '{', '[', '(', '<':
				d++
			case '}', ']', ')', '>':
				if !(body[j] == '>' && j > 0 && body[j-1] == '=') {
					d--
				}
			case ';', ',', '\n':
				if d == 0 {
					flush(j)
					tokStart = j + 1
				}
			}
		}
		flush(len(body))
		out[name] = fields
	}
	return out
}

func check(t *testing.T, ifaces map[string]map[string]bool, name string, obj any) {
	t.Helper()
	m, ok := obj.(map[string]any)
	if !ok {
		t.Errorf("%s: response element is not an object: %v", name, obj)
		return
	}
	decl, ok := ifaces[name]
	if !ok {
		t.Fatalf("interface %s is missing in api-types.ts", name)
	}
	var missing, extra []string
	for f, optional := range decl {
		if _, has := m[f]; !has && !optional {
			missing = append(missing, f)
		}
	}
	for k := range m {
		if _, has := decl[k]; !has {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 || len(extra) > 0 {
		t.Errorf("%s differs from the API response: declared in TS but absent in JSON: %v; present in JSON but not declared: %v", name, missing, extra)
	}
}

func first(t *testing.T, r *servertest.Resp) any {
	t.Helper()
	l := r.List()
	if r.Code != 200 || len(l) == 0 {
		t.Fatalf("expected a non-empty list: %d %s", r.Code, r.Raw)
	}
	return l[0]
}

// TestAPITypesMatchResponses keeps apps/web/src/contracts/api-types.ts in sync with the real JSON of the API.
func TestAPITypesMatchResponses(t *testing.T) {
	ifaces := tsInterfaces(t)
	if len(ifaces) < 15 {
		t.Fatalf("parsed only %d interfaces", len(ifaces))
	}
	w := newWorld(t)
	// data for every endpoint
	art := w.editor.Post("/api/v1/editor/articles", map[string]any{"category_id": servertest.CatNews, "source_locale": "en", "title_for_slug": "Contract article"}).Data()["id"].(string)
	w.editor.Put("/api/v1/editor/articles/"+art+"/source", map[string]any{"title": "Contract article", "summary": "Summary for the contract.", "body_md": "body"})
	w.editor.Post("/api/v1/editor/articles/"+art+"/submit", nil)
	w.mod.Post("/api/v1/mod/articles/"+art+"/publish", map[string]any{"rev": 1, "visibility": "public"})
	idea := w.alice.Post("/api/v1/ideas", map[string]any{"title": "Contract idea", "body": strings.Repeat("x", 30), "category": "other", "locale": "en"}).Data()
	ideaID := idea["id"].(string)
	w.mod.Post("/api/v1/ideas/"+ideaID+"/status", map[string]any{"to": "under_review"})
	w.bob.Post("/api/v1/comments", map[string]any{"target_type": "idea", "target_id": ideaID, "body": "contract comment"})
	conv := w.alice.Post("/api/v1/conversations", map[string]any{"user_id": w.bob.UserID()}).Data()["id"].(string)
	send(w.alice, conv, "hi bob")
	rep := w.bob.Post("/api/v1/reports", map[string]any{"target_type": "idea", "target_id": ideaID, "reason": "spam"})
	slug := w.alice.Get("/api/v1/me").Data()["slug"].(string)

	// Me and Role
	me := w.admin.Get("/api/v1/me").Data()
	check(t, ifaces, "Me", me)
	check(t, ifaces, "Role", me["roles"].([]any)[0])
	check(t, ifaces, "PublicProfile", w.anon.Get("/api/v1/users/"+slug).Data())
	check(t, ifaces, "Category", first(t, w.anon.Get("/api/v1/categories")))
	check(t, ifaces, "ArticleListItem", first(t, w.anon.Get("/api/v1/articles?locale=en")))
	artSlug := w.editor.Get("/api/v1/editor/articles/" + art).Data()["article"].(map[string]any)["slug"].(string)
	check(t, ifaces, "ArticleView", w.anon.Get("/api/v1/articles/"+artSlug+"?locale=en").Data())
	check(t, ifaces, "Idea", first(t, w.anon.Get("/api/v1/ideas")))
	check(t, ifaces, "Author", first(t, w.anon.Get("/api/v1/ideas")).(map[string]any)["author"])
	d := w.anon.Get("/api/v1/ideas/" + idea["slug"].(string)).Data()
	check(t, ifaces, "HistoryItem", d["history"].([]any)[0])
	check(t, ifaces, "Comment", first(t, w.anon.Get("/api/v1/comments?target_type=idea&target_id="+ideaID)))
	check(t, ifaces, "PersonHit", first(t, w.anon.Get("/api/v1/search/people?q="+slug)))
	cv := first(t, w.bob.Get("/api/v1/conversations?folder=requests"))
	check(t, ifaces, "Conversation", cv)
	check(t, ifaces, "Peer", cv.(map[string]any)["peer"])
	check(t, ifaces, "Message", first(t, w.bob.Get("/api/v1/conversations/"+conv+"/messages")))
	check(t, ifaces, "EditorListItem", first(t, w.editor.Get("/api/v1/editor/articles")))
	ea := w.editor.Get("/api/v1/editor/articles/" + art).Data()
	check(t, ifaces, "EditorArticle", ea)
	check(t, ifaces, "RevisionRow", ea["revisions"].([]any)[0])
	// the article details are inside "article": compare only the fields the UI declares
	rid := rep.Data()["id"].(string)
	check(t, ifaces, "Report", w.mod.Get("/api/v1/mod/reports/"+rid).Data())
	check(t, ifaces, "AdminUser", first(t, w.admin.Get("/api/v1/admin/users")))
	_ = acl.RoleAdmin
}
