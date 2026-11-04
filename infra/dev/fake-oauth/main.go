// Command fake-oauth is a stand-in for Google/Facebook in the local stack and the e2e tests. NEVER deploy it.
//
// The test chooses who signs in next with POST /__identity {"sub","email","name"} (or {"deny":true}); /authorize then
// redirects back at once with a one-time code, /token checks PKCE (when a challenge was sent) and the client secret, and
// /userinfo answers in both the Google (sub) and the Facebook (id) shape.
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
)

type identity struct {
	Sub   string `json:"sub"`
	Email string `json:"email"`
	Name  string `json:"name"`
	Deny  bool   `json:"deny"`
}

type grant struct {
	id        identity
	challenge string
	used      bool
}

var (
	mu     sync.Mutex
	next   = identity{Sub: "fake-1", Email: "fake@example.com", Name: "Fake Person"}
	codes  = map[string]*grant{}
	tokens = map[string]identity{}
	secret = os.Getenv("FAKE_CLIENT_SECRET")
)

func rnd() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /__identity", func(w http.ResponseWriter, r *http.Request) {
		var i identity
		if json.NewDecoder(r.Body).Decode(&i) != nil {
			http.Error(w, "bad json", 400)
			return
		}
		mu.Lock()
		next = i
		mu.Unlock()
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		ru, err := url.Parse(q.Get("redirect_uri"))
		if err != nil || ru.Scheme == "" {
			http.Error(w, "bad redirect_uri", 400)
			return
		}
		mu.Lock()
		id := next
		mu.Unlock()
		out := ru.Query()
		out.Set("state", q.Get("state"))
		if id.Deny {
			out.Set("error", "access_denied")
		} else {
			code := rnd()
			mu.Lock()
			codes[code] = &grant{id: id, challenge: q.Get("code_challenge")}
			mu.Unlock()
			out.Set("code", code)
		}
		ru.RawQuery = out.Encode()
		http.Redirect(w, r, ru.String(), http.StatusFound)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		defer mu.Unlock()
		g := codes[r.Form.Get("code")]
		if g == nil || g.used || (secret != "" && r.Form.Get("client_secret") != secret) {
			http.Error(w, `{"error":"invalid_grant"}`, 400)
			return
		}
		g.used = true
		if g.challenge != "" {
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
				http.Error(w, `{"error":"invalid_grant"}`, 400)
				return
			}
		}
		tok := rnd()
		tokens[tok] = g.id
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": tok, "token_type": "Bearer"})
	})
	mux.HandleFunc("GET /userinfo", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		id, ok := tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		mu.Unlock()
		if !ok {
			http.Error(w, "{}", 401)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"sub": id.Sub, "id": id.Sub, "email": id.Email, "email_verified": true, "name": id.Name})
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	log.Println("fake-oauth listening on :4010")
	log.Fatal(http.ListenAndServe(":4010", mux))
}
