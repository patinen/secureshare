package http

import (
	"errors"
	"io"
	"net/http"
	"os"
	"secureshare/api/internal/shares"
	"secureshare/api/internal/storage"
	"strconv"
	"time"
)

// Bounded multipart streaming: never ParseMultipartForm or trust Content-Length.
// Spooling allows complete validation before any object-storage write.
func fileUpload(s shares.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, storage.MaxFileSize+64*1024)
		reader, err := r.MultipartReader()
		if err != nil {
			fail(w, 400, "Expected multipart/form-data")
			return
		}
		var file *os.File
		defer func() {
			if file != nil {
				file.Close()
				_ = os.Remove(file.Name())
			}
		}()
		fields := map[string]string{}
		var name, kind string
		var size int64
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				uploadError(w, err)
				return
			}
			field := part.FormName()
			if field == "file" {
				if file != nil || part.FileName() == "" {
					part.Close()
					fail(w, 400, "Exactly one file required")
					return
				}
				name, kind = part.FileName(), part.Header.Get("Content-Type")
				file, err = os.CreateTemp("", "secureshare-upload-*")
				if err != nil {
					part.Close()
					fail(w, 500, "Upload unavailable")
					return
				}
				size, err = io.Copy(file, io.LimitReader(part, storage.MaxFileSize+1))
				if size > storage.MaxFileSize {
					fail(w, 413, "File exceeds 25 MiB")
					return
				}
				if err != nil {
					uploadError(w, err)
					return
				}
				part.Close()
			} else {
				if field != "title" && field != "expiresAt" && field != "maxRedemptions" {
					part.Close()
					fail(w, 400, "Unexpected multipart field")
					return
				}
				if _, exists := fields[field]; exists || part.FileName() != "" {
					part.Close()
					fail(w, 400, "Duplicate or invalid multipart field")
					return
				}
				value, err := io.ReadAll(io.LimitReader(part, 4097))
				part.Close()
				if err != nil {
					uploadError(w, err)
					return
				}
				if len(value) > 4096 {
					fail(w, 400, "Multipart field too large")
					return
				}
				fields[field] = string(value)
			}
		}
		if file == nil || size == 0 {
			fail(w, 400, "One non-empty file required")
			return
		}
		expiry, err := time.Parse(time.RFC3339Nano, fields["expiresAt"])
		if err != nil {
			fail(w, 400, "Invalid expiry")
			return
		}
		var title *string
		if value := fields["title"]; value != "" {
			title = &value
		}
		var limit *int
		if value := fields["maxRedemptions"]; value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil {
				fail(w, 400, "Invalid redemption limit")
				return
			}
			limit = &parsed
		}
		created, err := s.CreateFile(r.Context(), currentUser(r).ID, shares.FileInput{Title: title, ExpiresAt: expiry, MaxRedemptions: limit, FileName: name, ContentType: kind, Size: size, Body: file})
		if errors.Is(err, shares.ErrInvalid) {
			fail(w, 400, "Invalid file share: title up to 150 characters, expiry within 30 days, limit 1–1000, file 1 byte–25 MiB")
			return
		}
		if err != nil {
			fail(w, 500, "Unable to create file share")
			return
		}
		write(w, 201, created)
	}
}
func uploadError(w http.ResponseWriter, err error) {
	var limit *http.MaxBytesError
	if errors.As(err, &limit) {
		fail(w, 413, "Upload exceeds size limit")
		return
	}
	fail(w, 400, "Invalid multipart upload")
}
