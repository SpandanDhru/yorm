package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/SpandanDhru/yorm/internal/auth"
	"github.com/SpandanDhru/yorm/internal/game"
	"github.com/SpandanDhru/yorm/internal/ids"
	"github.com/SpandanDhru/yorm/internal/store"
)

// Store is the persistence the REST API needs; *store.Postgres implements it.
type Store interface {
	CreateSession(ctx context.Context, s store.Session, dm game.Member) error
	Session(ctx context.Context, id string) (store.Session, error)
	AddMember(ctx context.Context, sessionID string, m game.Member) error
	DeleteSession(ctx context.Context, id string) error
	Events(ctx context.Context, sessionID string, after int64, limit int) ([]game.Event, error)
}

// Commander runs a command in a live session, or stops a deleted one;
// *session.Manager implements it.
type Commander interface {
	Do(ctx context.Context, sessionID string, cmd game.Command) (int64, error)
	Delete(ctx context.Context, sessionID string)
}

type api struct {
	Deps
}

const (
	maxJSONBytes  = 64 << 10
	maxImageBytes = 20 << 20
	maxNameLen    = 80
	maxDisplayLen = 40
)

// joinResp is what a client stores to reconnect: the token is its identity.
type joinResp struct {
	Session    string    `json:"session"`
	Name       string    `json:"name"`
	User       string    `json:"user"`
	Role       auth.Role `json:"role"`
	Token      string    `json:"token"`
	InviteCode string    `json:"invite_code,omitempty"` // DM only
}

func (a *api) createSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	name, ok := cleanText(req.Name, maxNameLen)
	if !ok {
		writeError(w, http.StatusBadRequest, "name must be 1 to 80 characters")
		return
	}
	display, ok := cleanText(req.DisplayName, maxDisplayLen)
	if req.DisplayName == "" {
		display, ok = "DM", true
	}
	if !ok {
		writeError(w, http.StatusBadRequest, "display_name must be 1 to 40 characters")
		return
	}

	s := store.Session{ID: ids.New("ses"), Name: name, InviteCode: ids.Random(8)}
	dm := game.Member{UserID: game.UserID(ids.New("usr")), DisplayName: display, Role: auth.RoleDM}
	if err := a.Store.CreateSession(r.Context(), s, dm); err != nil {
		a.internalError(w, "create session", err)
		return
	}
	tok, err := a.Signer.Issue(s.ID, string(dm.UserID), dm.Role, auth.DefaultTTL)
	if err != nil {
		a.internalError(w, "issue token", err)
		return
	}
	writeJSON(w, http.StatusCreated, joinResp{
		Session: s.ID, Name: s.Name, User: string(dm.UserID), Role: dm.Role, Token: tok, InviteCode: s.InviteCode,
	})
}

func (a *api) joinSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code        string `json:"code"`
		DisplayName string `json:"display_name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	display, ok := cleanText(req.DisplayName, maxDisplayLen)
	if !ok {
		writeError(w, http.StatusBadRequest, "display_name must be 1 to 40 characters")
		return
	}
	s, err := a.Store.Session(r.Context(), chi.URLParam(r, "id"))
	// A missing session and a wrong code look the same, so invite links
	// cannot be used to probe for session IDs.
	if errors.Is(err, store.ErrNotFound) ||
		(err == nil && subtle.ConstantTimeCompare([]byte(req.Code), []byte(s.InviteCode)) != 1) {
		writeError(w, http.StatusForbidden, "invalid invite link")
		return
	}
	if err != nil {
		a.internalError(w, "load session", err)
		return
	}
	m := game.Member{UserID: game.UserID(ids.New("usr")), DisplayName: display, Role: auth.RolePlayer}
	if err := a.Store.AddMember(r.Context(), s.ID, m); err != nil {
		a.internalError(w, "add member", err)
		return
	}
	tok, err := a.Signer.Issue(s.ID, string(m.UserID), m.Role, auth.DefaultTTL)
	if err != nil {
		a.internalError(w, "issue token", err)
		return
	}
	writeJSON(w, http.StatusCreated, joinResp{Session: s.ID, Name: s.Name, User: string(m.UserID), Role: m.Role, Token: tok})
}

// imageTypes maps the sniffed content type of an accepted map image to its extension.
var imageTypes = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/webp": ".webp",
	"image/gif":  ".gif",
}

// uploadMap stores a map image and sets it as the session's map with the
// given grid. Form fields: image (file), cols, rows, cell_feet (optional).
func (a *api) uploadMap(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "id")
	claims, ok := a.bearer(w, r, sessionID)
	if !ok {
		return
	}
	if claims.Role != auth.RoleDM {
		writeError(w, http.StatusForbidden, "only the DM can upload maps")
		return
	}

	file, ok := imageFile(w, r)
	if !ok {
		return
	}
	defer func() { _ = file.Close() }()
	grid := map[string]int{}
	for _, f := range []string{"cols", "rows", "cell_feet"} {
		if v := r.FormValue(f); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				writeError(w, http.StatusBadRequest, f+" must be a number")
				return
			}
			grid[f] = n
		}
	}

	name, ok := a.save(w, sessionID, "map", file)
	if !ok {
		return
	}
	args, _ := json.Marshal(map[string]any{
		"image_url": game.UploadsPrefix + name, "cols": grid["cols"], "rows": grid["rows"], "cell_feet": grid["cell_feet"],
	})
	seq, err := a.Sessions.Do(r.Context(), sessionID, game.Command{
		ID: ids.New("cmd"), By: game.UserID(claims.User), Name: "set_map", Args: args,
	})
	if err != nil {
		a.removeUpload(name)
		var rej *game.Reject
		if errors.As(err, &rej) {
			writeJSON(w, http.StatusUnprocessableEntity, rej)
			return
		}
		a.internalError(w, "set map", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"image_url": game.UploadsPrefix + name, "seq": seq})
}

type badImageError string

func (e badImageError) Error() string { return string(e) }

// uploads runs f on the uploads dir as an os.Root, which keeps every path
// inside it.
func (a *api) uploads(f func(*os.Root) error) error {
	root, err := os.OpenRoot(a.UploadDir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return f(root)
}

// removeUpload deletes one uploaded file, by its path under the uploads dir.
func (a *api) removeUpload(name string) {
	if err := a.uploads(func(root *os.Root) error { return root.Remove(name) }); err != nil {
		a.Log.Warn("removing an unused upload", "name", name, "err", err)
	}
}

// imageFile returns the "image" file of a multipart upload, or writes the
// error and returns false.
func imageFile(w http.ResponseWriter, r *http.Request) (multipart.File, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxImageBytes+(1<<20))
	file, _, err := r.FormFile("image")
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "image must be at most 20 MB")
			return nil, false
		}
		writeError(w, http.StatusBadRequest, "image file is required")
		return nil, false
	}
	return file, true
}

// save stores an uploaded image in the session's folder and returns its
// path under the uploads dir, or writes the error and returns false.
func (a *api) save(w http.ResponseWriter, sessionID, prefix string, file io.Reader) (string, bool) {
	name, err := a.saveImage(sessionID, prefix, file)
	var bad badImageError
	switch {
	case errors.As(err, &bad):
		writeError(w, http.StatusBadRequest, bad.Error())
		return "", false
	case err != nil:
		a.internalError(w, "save image", err)
		return "", false
	}
	return name, true
}

// uploadImage stores a picture, for a token, and returns its URL. Any
// member may upload; setting it on a token is a command, checked there.
func (a *api) uploadImage(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "id")
	if _, ok := a.bearer(w, r, sessionID); !ok {
		return
	}
	if _, err := a.Store.Session(r.Context(), sessionID); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such session")
		return
	} else if err != nil {
		a.internalError(w, "load session", err)
		return
	}
	file, ok := imageFile(w, r)
	if !ok {
		return
	}
	defer func() { _ = file.Close() }()
	name, ok := a.save(w, sessionID, "img", file)
	if !ok {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"image_url": game.UploadsPrefix + name})
}

// deleteSession removes a session for good: its log, snapshots, members,
// and uploaded images. Connected clients are told it's gone. DM only.
func (a *api) deleteSession(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "id")
	claims, ok := a.bearer(w, r, sessionID)
	if !ok {
		return
	}
	if claims.Role != auth.RoleDM {
		writeError(w, http.StatusForbidden, "only the DM can delete the session")
		return
	}
	// Rows first, so nothing can start the session again; then stop it.
	switch err := a.Store.DeleteSession(r.Context(), sessionID); {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "no such session")
		return
	case err != nil:
		a.internalError(w, "delete session", err)
		return
	}
	a.Sessions.Delete(r.Context(), sessionID)
	if sessionDir.MatchString(sessionID) {
		if err := a.uploads(func(root *os.Root) error { return root.RemoveAll(sessionID) }); err != nil {
			a.Log.Warn("removing a deleted session's uploads", "session", sessionID, "err", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// saveImage checks the file really is an image, by content rather than the
// name the browser sent, and writes it under a random name in the
// session's folder. It returns the path relative to the uploads dir.
func (a *api) saveImage(sessionID, prefix string, file io.Reader) (string, error) {
	if !sessionDir.MatchString(sessionID) {
		return "", badImageError("bad session")
	}
	head := make([]byte, 512)
	n, err := io.ReadFull(file, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return "", err
	}
	head = head[:n]
	ext, ok := imageTypes[http.DetectContentType(head)]
	if !ok {
		return "", badImageError("image must be PNG, JPEG, WebP, or GIF")
	}

	name := sessionID + "/" + ids.New(prefix) + ext
	err = a.uploads(func(root *os.Root) error {
		if err := root.MkdirAll(sessionID, 0o750); err != nil {
			return err
		}
		// Written under a temporary name and renamed, so a half-written
		// file is never served.
		tmp := sessionID + "/.upload-" + ids.Random(6)
		f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer func() { _ = root.Remove(tmp) }() // no-op after the rename
		_, err = f.Write(head)
		if err == nil {
			_, err = io.Copy(f, file)
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		return root.Rename(tmp, name)
	})
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		return "", badImageError("image must be at most 20 MB")
	}
	return name, err
}

const (
	defaultPage = 100
	maxPage     = 1000
)

// eventHistory pages through a session's events, oldest first, for the DM:
// ?after=<seq>&limit=<n>. next is the after to ask for the following page,
// or null at the end.
func (a *api) eventHistory(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "id")
	claims, ok := a.bearer(w, r, sessionID)
	if !ok {
		return
	}
	if claims.Role != auth.RoleDM {
		writeError(w, http.StatusForbidden, "only the DM can read the history")
		return
	}
	after, limit := int64(0), defaultPage
	var err error
	if v := r.URL.Query().Get("after"); v != "" {
		if after, err = strconv.ParseInt(v, 10, 64); err != nil || after < 0 {
			writeError(w, http.StatusBadRequest, "after must be a seq")
			return
		}
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if limit, err = strconv.Atoi(v); err != nil || limit < 1 || limit > maxPage {
			writeError(w, http.StatusBadRequest, "limit must be 1 to 1000")
			return
		}
	}
	evs, err := a.Store.Events(r.Context(), sessionID, after, limit)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such session")
		return
	}
	if err != nil {
		a.internalError(w, "load events", err)
		return
	}
	resp := struct {
		Events []game.Event `json:"events"`
		Next   *int64       `json:"next"`
	}{Events: evs}
	if len(evs) == limit {
		resp.Next = &evs[len(evs)-1].Seq
	}
	writeJSON(w, http.StatusOK, resp)
}

var (
	uploadName = regexp.MustCompile(`^(map|img)_[a-z2-7]+\.(png|jpg|webp|gif)$`)
	sessionDir = regexp.MustCompile(`^ses_[a-z0-9]+$`) // safe as a folder name
)

// serveUpload serves an uploaded image: /uploads/<session>/<name>, or
// /uploads/<name> for ones from before uploads were kept per session.
// Names are random and never reused, so images can be cached forever.
func (a *api) serveUpload(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !uploadName.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	if dir := chi.URLParam(r, "session"); dir != "" {
		if !sessionDir.MatchString(dir) {
			http.NotFound(w, r)
			return
		}
		name = dir + "/" + name
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFileFS(w, r, os.DirFS(a.UploadDir), name) //nolint:gosec // both parts matched their patterns, and DirFS rejects ".."
}

// bearer checks the Authorization header holds a valid token for sessionID.
func (a *api) bearer(w http.ResponseWriter, r *http.Request, sessionID string) (auth.Claims, bool) {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		writeError(w, http.StatusUnauthorized, "missing bearer token")
		return auth.Claims{}, false
	}
	claims, err := a.Signer.Verify(tok)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token")
		return auth.Claims{}, false
	}
	if claims.Session != sessionID {
		writeError(w, http.StatusForbidden, "token is for a different session")
		return auth.Claims{}, false
	}
	return claims, true
}

func (a *api) internalError(w http.ResponseWriter, what string, err error) {
	a.Log.Error("request failed", "op", what, "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

// cleanText trims s and checks it has 1 to limit characters.
func cleanText(s string, limit int) (string, bool) {
	s = strings.TrimSpace(s)
	n := utf8.RuneCountInString(s)
	return s, n >= 1 && n <= limit
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("bad JSON body: %v", err))
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
