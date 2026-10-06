// Manual, opt-in provider acceptance. Never prints keys, URLs or credentials.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"secureshare/api/internal/storage"
	"strconv"
	"time"
)

func main() {
	if !run() {
		os.Exit(1)
	}
}

type s3ErrorResponse struct {
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
}

func unsignedAccessDenied(status int, body []byte) bool {
	switch status {
	case 401, 403, 404:
		return true
	case 400:
		var response s3ErrorResponse
		if xml.Unmarshal(body, &response) != nil {
			return false
		}
		return response.Code == "InvalidArgument" &&
			response.Message == "Authorization"
	default:
		return false
	}
}

func run() (success bool) {
	confirm := flag.Bool("allow-temporary-object", false, "explicitly authorize a random temporary object in configured bucket")
	flag.Parse()
	if !*confirm {
		fmt.Println("Explicit temporary-object authorization required")
		return false
	}
	path, err := strconv.ParseBool(os.Getenv("S3_USE_PATH_STYLE"))
	if err != nil {
		fmt.Println("Set S3_USE_PATH_STYLE explicitly after provider review")
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	obj, err := storage.New(ctx, storage.Options{Endpoint: os.Getenv("S3_ENDPOINT"), DownloadEndpoint: os.Getenv("S3_DOWNLOAD_ENDPOINT"), Region: os.Getenv("S3_REGION"), Bucket: os.Getenv("S3_BUCKET"), AccessKey: os.Getenv("S3_ACCESS_KEY_ID"), SecretKey: os.Getenv("S3_SECRET_ACCESS_KEY"), PathStyle: path})
	if err != nil {
		fmt.Println("FAIL provider configuration")
		return false
	}
	var random [32]byte
	var content [128]byte
	if _, err = rand.Read(random[:]); err != nil {
		return false
	}
	if _, err = rand.Read(content[:]); err != nil {
		return false
	}
	key := "secureshare-validation/" + hex.EncodeToString(random[:])
	// Independent bounded cleanup runs after any handled failure.
	defer func() {
		c, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		if obj.Delete(c, key) != nil {
			fmt.Println("FAIL temporary object cleanup; investigate provider permissions")
			success = false
		}
	}()
	fail := func(stage string) bool { fmt.Println("FAIL " + stage); return false }
	if obj.Put(ctx, key, bytes.NewReader(content[:]), int64(len(content)), "application/octet-stream") != nil {
		return fail("Put")
	}
	signed, err := obj.PresignGet(ctx, key, "validation.bin", 60*time.Second)
	if err != nil {
		return fail("PresignGet")
	}
	client := http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	fetch := func(raw string) (int, []byte, error) {
		req, e := http.NewRequestWithContext(ctx, "GET", raw, nil)
		if e != nil {
			return 0, nil, e
		}
		r, e := client.Do(req)
		if e != nil {
			return 0, nil, e
		}
		defer r.Body.Close()
		b, e := io.ReadAll(io.LimitReader(r.Body, 4096))
		return r.StatusCode, b, e
	}
	status, b, err := fetch(signed)
	if err != nil || status != 200 || !bytes.Equal(b, content[:]) {
		return fail("download byte comparison")
	}
	unsigned, err := url.Parse(signed)
	if err != nil {
		return fail("URL parsing")
	}
	unsigned.RawQuery = ""
	status, b, err = fetch(unsigned.String())
	if err != nil || !unsignedAccessDenied(status, b) {
		return fail("unsigned access denial")
	}
	if obj.Delete(ctx, key) != nil {
		return fail("Delete")
	}
	status, _, err = fetch(signed)
	if err != nil || (status != 403 && status != 404 && status != 410) {
		return fail("object unavailable after Delete")
	}
	if obj.Delete(ctx, key) != nil {
		return fail("Delete nonexistent object (provider differs from expected idempotent semantics)")
	}
	fmt.Println("PASS Put, PresignGet, byte comparison, unsigned denial, Delete, post-delete denial, nonexistent Delete")
	return true
}
