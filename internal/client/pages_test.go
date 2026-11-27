// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestListPageDecodesBothShapes(t *testing.T) {
	var arr listPage[ProjectSecret]
	if err := json.Unmarshal([]byte(`[{"id":1,"key":"A"},{"id":2,"key":"B"}]`), &arr); err != nil {
		t.Fatal(err)
	}
	if arr.Paged || len(arr.Items) != 2 || arr.Items[1].Key != "B" {
		t.Errorf("bare array decoded as %+v", arr)
	}
	var page listPage[ProjectSecret]
	if err := json.Unmarshal([]byte(`{"items":[{"id":3,"key":"C"}],"total":101,"page":2,"page_size":100}`), &page); err != nil {
		t.Fatal(err)
	}
	if !page.Paged || len(page.Items) != 1 || page.Items[0].ID != 3 || page.Total != 101 || page.Page != 2 || page.PageSize != 100 {
		t.Errorf("page decoded as %+v", page)
	}
}

func TestListPageRejectsOtherJSON(t *testing.T) {
	for _, in := range []string{`null`, `{}`, `{"items":null}`, `{"data":[]}`, `{"items":{}}`, `"items"`, `3`, `true`} {
		var p listPage[ProjectSecret]
		if err := json.Unmarshal([]byte(in), &p); err == nil {
			t.Errorf("decoded %s as %+v, want an error", in, p)
		}
	}
}

// secretsServer serves GET /api/projects/1/secrets with answer, which gets
// the page and page_size asked for (0 when absent), and records them.
type secretsServer struct {
	mu    sync.Mutex
	asked [][2]int
}

func (s *secretsServer) client(t *testing.T, answer func(page, size int) string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/projects/1/secrets" {
			http.NotFound(w, r)
			return
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		size, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
		s.mu.Lock()
		s.asked = append(s.asked, [2]int{page, size})
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(answer(page, size)))
	}))
	t.Cleanup(srv.Close)
	c, err := New(Config{Endpoint: srv.URL, APIKey: "tk_test"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (s *secretsServer) requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.asked)
}

func secretsJSON(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprintf(`{"id":%d,"project_id":1,"key":"K%d","environments":[{"id":7}]}`, id, id)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// pageOf serves ids, newest first, as Temps pages them.
func pageOf(ids []int64, page, size int) string {
	start := min((page-1)*size, len(ids))
	end := min(start+size, len(ids))
	return fmt.Sprintf(`{"items":%s,"total":%d,"page":%d,"page_size":%d}`, secretsJSON(ids[start:end]), len(ids), page, size)
}

// newestFirst is IDs n down to 1.
func newestFirst(n int) []int64 {
	ids := make([]int64, n)
	for i := range ids {
		ids[i] = int64(n - i)
	}
	return ids
}

func TestGetProjectSecretReadsABareArray(t *testing.T) {
	s := &secretsServer{}
	c := s.client(t, func(int, int) string { return secretsJSON(newestFirst(250)) })
	got, err := c.GetProjectSecret(context.Background(), 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != 3 || got.EnvironmentIDs()[0] != 7 {
		t.Errorf("got %+v", got)
	}
	// A bare array is the whole list: one request, however long it is.
	if n := s.requests(); n != 1 {
		t.Errorf("%d requests, want 1", n)
	}
	if _, err := c.GetProjectSecret(context.Background(), 1, 999); !IsNotFound(err) {
		t.Errorf("missing secret: err = %v, want a 404", err)
	}
}

// A secret that only appears on a later page is found, not taken for
// deleted, and the pages are read at the largest size Temps serves.
func TestGetProjectSecretReadsEveryPage(t *testing.T) {
	ids := newestFirst(250) // pages of 100: 250..151, 150..51, 50..1
	s := &secretsServer{}
	c := s.client(t, func(page, size int) string { return pageOf(ids, page, size) })
	got, err := c.GetProjectSecret(context.Background(), 1, 7)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != 7 || got.Key != "K7" {
		t.Errorf("got %+v", got)
	}
	want := [][2]int{{1, 100}, {2, 100}, {3, 100}}
	if fmt.Sprint(s.asked) != fmt.Sprint(want) {
		t.Errorf("asked for (page, page_size) %v, want %v", s.asked, want)
	}

	s.asked = nil
	if _, err := c.GetProjectSecret(context.Background(), 1, 999); !IsNotFound(err) {
		t.Errorf("missing secret: err = %v, want a 404", err)
	}
	if n := s.requests(); n != 3 {
		t.Errorf("a missing secret took %d requests, want 3 (one per page)", n)
	}
}

// A server that pages more finely than asked is followed by what it serves.
func TestGetProjectSecretFollowsTheServedPageSize(t *testing.T) {
	ids := newestFirst(45)
	s := &secretsServer{}
	c := s.client(t, func(page, _ int) string { return pageOf(ids, page, 20) })
	got, err := c.GetProjectSecret(context.Background(), 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != 2 || s.requests() != 3 {
		t.Errorf("got %+v after %d requests, want secret 2 after 3", got, s.requests())
	}
}

// A secret deleted while the list is read moves the older ones forward, so
// one can slip onto a page already read; the changed total makes the
// provider read the list again rather than report the secret gone.
func TestGetProjectSecretRereadsAListThatChanged(t *testing.T) {
	ids := newestFirst(150)
	var mu sync.Mutex
	reads := 0
	s := &secretsServer{}
	c := s.client(t, func(page, size int) string {
		mu.Lock()
		defer mu.Unlock()
		reads++
		if reads == 2 {
			// After page 1 (150..51) is read, secret 150 is deleted: 50
			// moves onto page 1 and page 2 starts at 49.
			ids = ids[1:]
		}
		return pageOf(ids, page, size)
	})
	got, err := c.GetProjectSecret(context.Background(), 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	// Page 1, page 2 (secret 50 is not on it any more), then page 1 again.
	if got.ID != 50 || reads != 3 {
		t.Errorf("got %+v after %d requests, want secret 50 after 3", got, reads)
	}
}

func TestGetProjectSecretStopsAtTheMostPagesItReads(t *testing.T) {
	full := pageOf(newestFirst(100), 1, 100)
	for _, tc := range []struct {
		name   string
		answer func(page, size int) string
	}{
		// Every page is full and the total keeps up.
		{"endless", func(page, size int) string {
			return strings.Replace(full, `"total":100,"page":1`, fmt.Sprintf(`"total":%d,"page":%d`, (page+1)*size, page), 1)
		}},
		// Every page is full and no total is given.
		{"no total", func(page, size int) string {
			return fmt.Sprintf(`{"items":%s,"page":%d,"page_size":%d}`, secretsJSON(newestFirst(100)), page, size)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &secretsServer{}
			c := s.client(t, tc.answer)
			_, err := c.GetProjectSecret(context.Background(), 1, 999)
			if err == nil || IsNotFound(err) {
				t.Fatalf("err = %v, want an error that is not a 404", err)
			}
			if n := s.requests(); n > maxListPages*listWalks {
				t.Errorf("%d requests, more than %d", n, maxListPages*listWalks)
			}
		})
	}
}

func TestGetProjectSecretRejectsAServerThatIgnoresThePage(t *testing.T) {
	s := &secretsServer{}
	c := s.client(t, func(_, size int) string { return pageOf(newestFirst(250), 1, size) })
	_, err := c.GetProjectSecret(context.Background(), 1, 7)
	if err == nil || !strings.Contains(err.Error(), "asked for page 2") {
		t.Fatalf("err = %v, want one naming the page asked for", err)
	}
	if n := s.requests(); n != 2 {
		t.Errorf("%d requests, want 2", n)
	}
}

// An answer that is neither shape fails rather than reading as an empty
// list, which would remove the secret from state.
func TestGetProjectSecretRejectsOtherJSON(t *testing.T) {
	for _, body := range []string{`{"data":[]}`, `null`, `{"items":null,"total":0}`, `"secrets"`} {
		s := &secretsServer{}
		c := s.client(t, func(int, int) string { return body })
		if _, err := c.GetProjectSecret(context.Background(), 1, 3); err == nil || IsNotFound(err) {
			t.Errorf("%s: err = %v, want a decoding error", body, err)
		}
	}
}
