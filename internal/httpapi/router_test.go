package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SpandanDhru/yorm/internal/auth"
	"github.com/SpandanDhru/yorm/internal/game"
	"github.com/SpandanDhru/yorm/internal/session"
	"github.com/SpandanDhru/yorm/internal/session/sessiontest"
	"github.com/SpandanDhru/yorm/internal/store"
	"github.com/SpandanDhru/yorm/internal/ws"
)

// fakeStore keeps sessions and members in memory.
type fakeStore struct {
	mu       sync.Mutex
	sessions map[string]store.Session
	members  map[string][]game.Member
}

func (f *fakeStore) CreateSession(_ context.Context, s store.Session, dm game.Member) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[s.ID] = s
	f.members[s.ID] = []game.Member{dm}
	return nil
}

func (f *fakeStore) Session(_ context.Context, id string) (store.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[id]
	if !ok {
		return store.Session{}, store.ErrNotFound
	}
	return s, nil
}

// Events serves seqs 1..20 of a fixed session "ses_1".
func (f *fakeStore) Events(_ context.Context, id string, after int64, limit int) ([]game.Event, error) {
	if id != "ses_1" {
		return nil, store.ErrNotFound
	}
	evs := []game.Event{}
	for seq := after + 1; seq <= 20 && len(evs) < limit; seq++ {
		evs = append(evs, game.Event{Seq: seq, Name: "DiceRolled", By: "usr_dm", Data: game.DiceRolled{Expr: "1d20"}})
	}
	return evs, nil
}

func (f *fakeStore) AddMember(_ context.Context, id string, m game.Member) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.members[id] = append(f.members[id], m)
	return nil
}

// fakeCommander records commands and answers with err.
type fakeCommander struct {
	mu      sync.Mutex
	cmds    []game.Command
	err     error
	deleted []string
}

func (f *fakeCommander) Delete(_ context.Context, id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, id)
}

func (f *fakeStore) DeleteSession(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.sessions[id]; !ok {
		return store.ErrNotFound
	}
	delete(f.sessions, id)
	return nil
}

func (f *fakeCommander) Do(_ context.Context, _ string, cmd game.Command) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cmds = append(f.cmds, cmd)
	return int64(len(f.cmds)), f.err
}

type testAPI struct {
	t      *testing.T
	h      http.Handler
	signer *auth.Signer
	store  *fakeStore
	cmds   *fakeCommander
	dir    string
}

func newAPI(t *testing.T, health func(context.Context) error) *testAPI {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	signer, err := auth.NewSigner([]byte(strings.Repeat("k", auth.MinKeyLen)))
	if err != nil {
		t.Fatal(err)
	}
	mgr := session.NewManager(sessiontest.NewMemStore(), log, session.DefaultOptions())
	t.Cleanup(func() { _ = mgr.Shutdown(context.Background()) })
	web := t.TempDir()
	must(t, os.WriteFile(filepath.Join(web, "index.html"), []byte("<html>app</html>"), 0o600))
	must(t, os.Mkdir(filepath.Join(web, "assets"), 0o750))
	must(t, os.WriteFile(filepath.Join(web, "assets", "app-abc.js"), []byte("js"), 0o600))

	a := &testAPI{
		t: t, signer: signer, dir: t.TempDir(),
		store: &fakeStore{sessions: map[string]store.Session{}, members: map[string][]game.Member{}},
		cmds:  &fakeCommander{},
	}
	a.h = NewRouter(Deps{
		Log: log, WS: ws.NewServer(signer, mgr, log, ws.DefaultOptions()), Health: health,
		Signer: signer, Store: a.store, Sessions: a.cmds, UploadDir: a.dir, WebDir: web,
	})
	return a
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (a *testAPI) do(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	return rec
}

func (a *testAPI) postJSON(path, body string) *httptest.ResponseRecorder {
	return a.do(httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
}

func (a *testAPI) token(session, user string, role auth.Role) string {
	tok, err := a.signer.Issue(session, user, role, time.Hour)
	must(a.t, err)
	return tok
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	return v
}

func TestHealthz(t *testing.T) {
	for name, tt := range map[string]struct {
		err  error
		code int
	}{
		"db up":   {nil, http.StatusOK},
		"db down": {errors.New("connection refused"), http.StatusServiceUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			a := newAPI(t, func(context.Context) error { return tt.err })
			if rec := a.do(httptest.NewRequest(http.MethodGet, "/healthz", nil)); rec.Code != tt.code {
				t.Fatalf("status = %d, want %d", rec.Code, tt.code)
			}
		})
	}
}

func TestLivezIgnoresTheDatabase(t *testing.T) {
	a := newAPI(t, func(context.Context) error { return errors.New("database asleep") })
	if rec := a.do(httptest.NewRequest(http.MethodGet, "/livez", nil)); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// The session ID in the path must reach ws.Server: a token for another
// session is refused before the upgrade.
func TestWebSocketRouteUsesPathSession(t *testing.T) {
	a := newAPI(t, nil)
	rec := a.do(httptest.NewRequest(http.MethodGet, "/ws/sessions/ses_1?token="+a.token("ses_other", "usr_kai", auth.RolePlayer), nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestCreateAndJoinSession(t *testing.T) {
	a := newAPI(t, nil)

	rec := a.postJSON("/api/sessions", `{"name":"  The Sunless Citadel ","display_name":"Mara"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	created := decode[joinResp](t, rec)
	claims, err := a.signer.Verify(created.Token)
	if err != nil || claims.Session != created.Session || claims.User != created.User || claims.Role != auth.RoleDM {
		t.Fatalf("DM token claims = %+v, %v; response %+v", claims, err, created)
	}
	if created.Name != "The Sunless Citadel" || created.InviteCode == "" {
		t.Fatalf("create response = %+v", created)
	}
	if dm := a.store.members[created.Session][0]; dm.DisplayName != "Mara" || dm.Role != auth.RoleDM {
		t.Fatalf("stored DM = %+v", dm)
	}

	joinURL := "/api/sessions/" + created.Session + "/join"
	for name, tt := range map[string]struct {
		url, body string
		code      int
	}{
		"wrong code":      {joinURL, `{"code":"nope","display_name":"Kai"}`, http.StatusForbidden},
		"unknown session": {"/api/sessions/ses_nope/join", `{"code":"` + created.InviteCode + `","display_name":"Kai"}`, http.StatusForbidden},
		"no name":         {joinURL, `{"code":"` + created.InviteCode + `","display_name":" "}`, http.StatusBadRequest},
		"unknown field":   {joinURL, `{"code":"` + created.InviteCode + `","display_name":"Kai","role":"dm"}`, http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			if rec := a.postJSON(tt.url, tt.body); rec.Code != tt.code {
				t.Fatalf("status = %d (%s), want %d", rec.Code, rec.Body, tt.code)
			}
		})
	}

	rec = a.postJSON(joinURL, `{"code":"`+created.InviteCode+`","display_name":"Kai"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("join: %d %s", rec.Code, rec.Body)
	}
	joined := decode[joinResp](t, rec)
	claims, err = a.signer.Verify(joined.Token)
	if err != nil || claims.Role != auth.RolePlayer || claims.Session != created.Session || joined.InviteCode != "" {
		t.Fatalf("player join = %+v, claims %+v, %v", joined, claims, err)
	}
}

func TestCreateSessionValidation(t *testing.T) {
	a := newAPI(t, nil)
	for _, body := range []string{``, `{"name":""}`, `{"name":"` + strings.Repeat("x", 81) + `"}`, `{"name":"ok","display_name":"` + strings.Repeat("x", 41) + `"}`} {
		if rec := a.postJSON("/api/sessions", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%q: status %d, want 400", body, rec.Code)
		}
	}
}

func pngBytes(t *testing.T) []byte {
	var buf bytes.Buffer
	must(t, png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 4, 4))))
	return buf.Bytes()
}

func uploadReq(t *testing.T, session, token string, file []byte, fields map[string]string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if file != nil {
		fw, err := mw.CreateFormFile("image", "map.png")
		must(t, err)
		_, _ = fw.Write(file)
	}
	for k, v := range fields {
		must(t, mw.WriteField(k, v))
	}
	must(t, mw.Close())
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+session+"/maps", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

// files lists the files (not folders) under dir, as paths relative to it.
func files(t *testing.T, dir string) []string {
	t.Helper()
	var names []string
	must(t, filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, path)
			names = append(names, filepath.ToSlash(rel))
		}
		return err
	}))
	return names
}

func TestUploadMap(t *testing.T) {
	a := newAPI(t, nil)
	dmTok := a.token("ses_1", "usr_dm", auth.RoleDM)
	grid := map[string]string{"cols": "20", "rows": "15"}

	for name, tt := range map[string]struct {
		req  *http.Request
		code int
	}{
		"no token":      {uploadReq(t, "ses_1", "", pngBytes(t), grid), http.StatusUnauthorized},
		"player":        {uploadReq(t, "ses_1", a.token("ses_1", "usr_kai", auth.RolePlayer), pngBytes(t), grid), http.StatusForbidden},
		"other session": {uploadReq(t, "ses_2", dmTok, pngBytes(t), grid), http.StatusForbidden},
		"no file":       {uploadReq(t, "ses_1", dmTok, nil, grid), http.StatusBadRequest},
		"not an image":  {uploadReq(t, "ses_1", dmTok, []byte("<svg onload=alert(1)>"), grid), http.StatusBadRequest},
		"bad number":    {uploadReq(t, "ses_1", dmTok, pngBytes(t), map[string]string{"cols": "wide"}), http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			if rec := a.do(tt.req); rec.Code != tt.code {
				t.Fatalf("status = %d (%s), want %d", rec.Code, rec.Body, tt.code)
			}
		})
	}
	if len(a.cmds.cmds) != 0 || len(files(t, a.dir)) != 0 {
		t.Fatalf("rejected uploads left commands %v or files %v", a.cmds.cmds, files(t, a.dir))
	}

	img := pngBytes(t)
	rec := a.do(uploadReq(t, "ses_1", dmTok, img, map[string]string{"cols": "20", "rows": "15", "cell_feet": "5"}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	resp := decode[struct {
		ImageURL string `json:"image_url"`
	}](t, rec)
	cmd := a.cmds.cmds[0]
	var args map[string]any
	must(t, json.Unmarshal(cmd.Args, &args))
	if cmd.Name != "set_map" || cmd.By != "usr_dm" || args["image_url"] != resp.ImageURL || args["cols"] != 20.0 || args["rows"] != 15.0 {
		t.Fatalf("command = %+v %v", cmd, args)
	}

	get := a.do(httptest.NewRequest(http.MethodGet, resp.ImageURL, nil))
	if get.Code != http.StatusOK || !bytes.Equal(get.Body.Bytes(), img) || !strings.Contains(get.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("GET %s: %d, %d bytes, headers %v", resp.ImageURL, get.Code, get.Body.Len(), get.Header())
	}
}

// A rejected set_map (say, a grid too large) leaves no orphaned image.
func TestUploadMapRejectedRemovesFile(t *testing.T) {
	a := newAPI(t, nil)
	a.cmds.err = &game.Reject{Code: game.CodeInvalidTarget, Message: "cols and rows must be between 1 and 200"}
	rec := a.do(uploadReq(t, "ses_1", a.token("ses_1", "usr_dm", auth.RoleDM), pngBytes(t), map[string]string{"cols": "500", "rows": "5"}))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d (%s), want 422", rec.Code, rec.Body)
	}
	if rej := decode[game.Reject](t, rec); rej.Code != game.CodeInvalidTarget {
		t.Fatalf("body = %+v", rej)
	}
	if f := files(t, a.dir); len(f) != 0 {
		t.Fatalf("files left behind: %v", f)
	}
}

func TestStaticRoutes(t *testing.T) {
	a := newAPI(t, nil)
	must(t, os.WriteFile(filepath.Join(a.dir, "secret.txt"), []byte("x"), 0o600))
	for _, tt := range []struct {
		path, body string
		code       int
	}{
		{"/", "<html>app</html>", http.StatusOK},
		{"/s/ses_1", "<html>app</html>", http.StatusOK}, // client-side route
		{"/assets/app-abc.js", "js", http.StatusOK},
		{"/api/nope", `{"error":"not found"}`, http.StatusNotFound},
		{"/uploads/secret.txt", "", http.StatusNotFound},
		{"/uploads/..%2fsecret.txt", "", http.StatusNotFound},
		{"/uploads/map_abc.png", "", http.StatusNotFound},
	} {
		rec := a.do(httptest.NewRequest(http.MethodGet, tt.path, nil))
		if rec.Code != tt.code || (tt.body != "" && strings.TrimSpace(rec.Body.String()) != tt.body) {
			t.Errorf("GET %s = %d %q, want %d %q", tt.path, rec.Code, rec.Body, tt.code, tt.body)
		}
	}
}

func TestEventHistory(t *testing.T) {
	a := newAPI(t, nil)
	get := func(path, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return a.do(req)
	}
	type page struct {
		Events []struct{ Seq int64 }
		Next   *int64
	}
	dmTok := a.token("ses_1", "usr_dm", auth.RoleDM)

	rec := get("/api/sessions/ses_1/events?after=5&limit=10", dmTok)
	p := decode[page](t, rec)
	if rec.Code != http.StatusOK || len(p.Events) != 10 || p.Events[0].Seq != 6 || p.Next == nil || *p.Next != 15 {
		t.Fatalf("page 1 = %d %+v", rec.Code, p)
	}
	p = decode[page](t, get("/api/sessions/ses_1/events?after=15&limit=10", dmTok))
	if len(p.Events) != 5 || p.Next != nil {
		t.Fatalf("last page = %+v", p)
	}
	if p := decode[page](t, get("/api/sessions/ses_1/events", dmTok)); len(p.Events) != 20 {
		t.Fatalf("default page has %d events", len(p.Events))
	}

	for name, tt := range map[string]struct {
		path, token string
		code        int
	}{
		"no token":      {"/api/sessions/ses_1/events", "", http.StatusUnauthorized},
		"player":        {"/api/sessions/ses_1/events", a.token("ses_1", "usr_kai", auth.RolePlayer), http.StatusForbidden},
		"bad after":     {"/api/sessions/ses_1/events?after=x", dmTok, http.StatusBadRequest},
		"limit too big": {"/api/sessions/ses_1/events?limit=5000", dmTok, http.StatusBadRequest},
		"missing":       {"/api/sessions/ses_2/events", a.token("ses_2", "usr_dm", auth.RoleDM), http.StatusNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			if rec := get(tt.path, tt.token); rec.Code != tt.code {
				t.Fatalf("status = %d, want %d", rec.Code, tt.code)
			}
		})
	}
}

func imageReq(t *testing.T, session, token string, file []byte) *http.Request {
	t.Helper()
	req := uploadReq(t, session, token, file, nil)
	req.URL.Path = "/api/sessions/" + session + "/images"
	return req
}

func TestUploadTokenImage(t *testing.T) {
	a := newAPI(t, nil)
	created := decode[joinResp](t, a.postJSON("/api/sessions", `{"name":"Pics"}`))
	sid := created.Session
	player := a.token(sid, "usr_kai", auth.RolePlayer)

	if rec := a.do(imageReq(t, sid, "", pngBytes(t))); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", rec.Code)
	}
	if rec := a.do(imageReq(t, sid, player, []byte("not an image"))); rec.Code != http.StatusBadRequest {
		t.Fatalf("not an image: %d", rec.Code)
	}
	if rec := a.do(imageReq(t, "ses_gone", a.token("ses_gone", "usr_kai", auth.RolePlayer), pngBytes(t))); rec.Code != http.StatusNotFound {
		t.Fatalf("missing session: %d", rec.Code)
	}

	rec := a.do(imageReq(t, sid, player, pngBytes(t)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	url := decode[struct {
		ImageURL string `json:"image_url"`
	}](t, rec).ImageURL
	if !strings.HasPrefix(url, "/uploads/"+sid+"/img_") {
		t.Fatalf("url = %s", url)
	}
	if get := a.do(httptest.NewRequest(http.MethodGet, url, nil)); get.Code != http.StatusOK || get.Body.Len() == 0 {
		t.Fatalf("GET %s: %d", url, get.Code)
	}
	for _, bad := range []string{"/uploads/..%2f" + sid + "/img_a.png", "/uploads/" + sid + "/secret.txt", "/uploads/SES_1/img_a.png"} {
		if got := a.do(httptest.NewRequest(http.MethodGet, bad, nil)); got.Code != http.StatusNotFound {
			t.Errorf("GET %s: %d, want 404", bad, got.Code)
		}
	}
}

func TestDeleteSession(t *testing.T) {
	a := newAPI(t, nil)
	created := decode[joinResp](t, a.postJSON("/api/sessions", `{"name":"Doomed"}`))
	sid := created.Session
	if rec := a.do(imageReq(t, sid, created.Token, pngBytes(t))); rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d", rec.Code)
	}
	del := func(token string) int {
		req := httptest.NewRequest(http.MethodDelete, "/api/sessions/"+sid, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return a.do(req).Code
	}
	if code := del(a.token(sid, "usr_kai", auth.RolePlayer)); code != http.StatusForbidden {
		t.Fatalf("player delete: %d", code)
	}
	if code := del(created.Token); code != http.StatusNoContent {
		t.Fatalf("DM delete: %d", code)
	}
	if len(a.cmds.deleted) != 1 || a.cmds.deleted[0] != sid {
		t.Fatalf("manager told to delete %v", a.cmds.deleted)
	}
	if f := files(t, a.dir); len(f) != 0 {
		t.Fatalf("uploads left behind: %v", f)
	}
	if code := del(created.Token); code != http.StatusNotFound {
		t.Fatalf("second delete: %d", code)
	}
}
