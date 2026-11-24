package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/silirim/vodmax/internal/secret"
	"github.com/silirim/vodmax/internal/store"
)

func TestEpisodeTitle(t *testing.T) {
	for raw, want := range map[string]string{
		"Breaking Bad - Reazioni collaterali (2008) - S01E01 - Questione di chimica": "Questione di chimica",
		"Pilot":                   "Pilot",
		"Twin Peaks S02E01":       "Twin Peaks S02E01",
		"Serie s1e12: Il ritorno": "Il ritorno",
	} {
		if got := episodeTitle(raw); got != want {
			t.Errorf("%q → %q, atteso %q", raw, got, want)
		}
	}
}

// m3uFilm serves a playlist with one film and the file itself, reading it
// by range as panels do.
func m3uFilm(t *testing.T, film []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".m3u"):
			w.Header().Set("Content-Type", "audio/x-mpegurl")
			fmt.Fprintf(w, "#EXTM3U\n#EXTINF:-1 tvg-name=\"Film\" group-title=\"Film\",Film Prova\nhttp://%s/movie/u/p/film.mp4\n", r.Host)
		case strings.HasPrefix(r.URL.Path, "/movie/"):
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Content-Length", strconv.Itoa(len(film)))
			w.WriteHeader(http.StatusOK)
			w.Write(film)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })
	return srv
}

// An M3U film plays through the server: the session comes, and ranges of
// the file are relayed from the playlist's own address.
func TestM3UFilmSession(t *testing.T) {
	ctx := context.Background()
	film := make([]byte, 4<<20)
	for i := range film {
		film[i] = byte(i)
	}
	srv := m3uFilm(t, film)
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "vodmax.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	box, err := secret.LoadOrCreate(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, box, nil)
	defer s.Close()
	if err := s.SetCacheDir(filepath.Join(dir, "cache")); err != nil {
		t.Fatal(err)
	}
	seal := func(int64) []byte { return []byte("x") }
	vpnID, err := st.AddVPN(ctx, "v", "system", seal)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.AddSubscription(ctx, SubscriptionInput{Name: "M", Kind: "m3u", VPNID: vpnID, URL: srv.URL + "/lista.m3u"})
	if err != nil {
		t.Fatal(err)
	}
	if res, err := s.Sync(ctx, id, false); err != nil || res.Total["movie"] != 1 {
		t.Fatalf("sync: %+v %v", res, err)
	}
	page, err := s.Catalog(ctx, store.Query{Kind: "movie", SubscriptionID: id})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("catalog: %+v %v", page, err)
	}
	sess, err := s.NewPlaybackSession(ctx, "movie", page.Items[0].ID, "salotto-pc-01", "direct", false)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	if sess.Mode != "direct" || !strings.HasPrefix(sess.URL, "/stream/vod/") {
		t.Fatalf("session: %+v", sess)
	}
	stm, err := s.OpenDirect(ctx, strings.TrimPrefix(sess.URL, "/stream/vod/"), "bytes=100-199", "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer stm.Close()
	got, err := io.ReadAll(io.LimitReader(stm, 100))
	if err != nil || len(got) != 100 || !bytes.Equal(got, film[100:200]) {
		t.Fatalf("range: %d bytes %v", len(got), err)
	}
}
