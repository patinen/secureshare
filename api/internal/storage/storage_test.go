package storage

import (
	"context"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestFilename(t *testing.T) {
	cases := []struct{ name, input, want string }{
		{"unix_path", "../../report.pdf", "report.pdf"}, {"windows_path", `C:\private\report.pdf`, "report.pdf"},
		{"header_injection", "evil\r\nX-Header: injected.html", "evilX-Header_ injected.html"}, {"empty", "...", "download"},
		{"quoted", `a"b.html`, "a_b.html"}, {"unicode", "résumé.pdf", "résumé.pdf"}, {"device", "CON.txt", "_CON.txt"},
		{"multipart_escaped", "evil%22report%0D%0A.html", "evil_report.html"},
		{"bidi", "photo\u202egnp.exe", "photognp.exe"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Filename(c.input)
			if got != c.want {
				t.Fatal("unexpected sanitized name")
			}
			kind, params, err := mime.ParseMediaType(Disposition(c.input))
			if err != nil || kind != "attachment" || params["filename"] != got || strings.ContainsAny(Disposition(c.input), "\r\n") {
				t.Fatal("unsafe disposition")
			}
		})
	}
	t.Run("length", func(t *testing.T) {
		if len([]rune(Filename(strings.Repeat("é", 300)))) != 255 {
			t.Fatal("filename unbounded")
		}
	})
}
func TestContentType(t *testing.T) {
	for _, c := range []struct{ name, input, want string }{{"normalize", "Text/HTML; charset=utf-8", "text/html"}, {"invalid", "x\r\nheader: evil", "application/octet-stream"}, {"bounded", strings.Repeat("a", 256), "application/octet-stream"}, {"empty", "", "application/octet-stream"}} {
		t.Run(c.name, func(t *testing.T) {
			if ContentType(c.input) != c.want {
				t.Fatal("wrong type")
			}
		})
	}
}

func TestS3SignedDownload(t *testing.T) {
	var uploaded bool
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			body, _ := io.ReadAll(r.Body)
			if string(body) != "<script>evil</script>" || r.Header.Get("Content-Type") != "application/octet-stream" || r.Header.Get("Content-Disposition") != "attachment" {
				t.Error("unsafe stored object")
			}
			uploaded = true
		}
		w.WriteHeader(200)
	}))
	defer endpoint.Close()
	s, err := New(context.Background(), Options{Endpoint: endpoint.URL, DownloadEndpoint: "http://downloads.local", Region: "us-east-1", Bucket: "private", AccessKey: "local-test", SecretKey: "local-test-secret", PathStyle: true})
	if err != nil {
		t.Fatal("storage setup")
	}
	body := "<script>evil</script>"
	if err = s.Put(context.Background(), "files/opaque", strings.NewReader(body), int64(len(body)), "text/html"); err != nil || !uploaded {
		t.Fatal("upload failed")
	}
	t.Run("safe_presign", func(t *testing.T) {
		signed, err := s.PresignGet(context.Background(), "files/opaque", "evil\r\nheader.html", DownloadTTL)
		if err != nil {
			t.Fatal("presign failed")
		}
		u, err := url.Parse(signed)
		if err != nil {
			t.Fatal("invalid URL")
		}
		q := u.Query()
		if u.Host != "downloads.local" || q.Get("X-Amz-Expires") != "60" || q.Get("response-content-type") != "application/octet-stream" || q.Get("response-cache-control") != "no-store" {
			t.Fatal("unsafe signed download options")
		}
		kind, params, err := mime.ParseMediaType(q.Get("response-content-disposition"))
		if err != nil || kind != "attachment" || params["filename"] != "evilheader.html" {
			t.Fatal("unsafe download filename")
		}
	})
	t.Run("long_ttl_rejected", func(t *testing.T) {
		if _, err := s.PresignGet(context.Background(), "key", "name", time.Minute+time.Second); err == nil {
			t.Fatal("long TTL accepted")
		}
	})
	t.Run("zero_ttl_rejected", func(t *testing.T) {
		if _, err := s.PresignGet(context.Background(), "key", "name", 0); err == nil {
			t.Fatal("zero TTL accepted")
		}
	})
	t.Run("delete", func(t *testing.T) {
		if err := s.Delete(context.Background(), "files/opaque"); err != nil {
			t.Fatal("delete failed")
		}
	})
}
