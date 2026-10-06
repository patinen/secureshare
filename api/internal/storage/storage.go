// Package storage contains the private-object boundary. URLs and credentials must
// never be logged; callers serialize only the URL returned after redemption.
package storage

import (
	"context"
	"io"
	"mime"
	"strings"
	"time"
	"unicode"
)

const MaxFileSize int64 = 25 * 1024 * 1024
const DownloadTTL = 60 * time.Second

type Objects interface {
	Put(context.Context, string, io.ReadSeeker, int64, string) error
	Delete(context.Context, string) error
	PresignGet(context.Context, string, string, time.Duration) (string, error)
}

func Filename(raw string) string {
	// Normalize browser multipart escapes once before sanitizing.
	raw = strings.NewReplacer("%22", "\"", "%0D", "\r", "%0d", "\r", "%0A", "\n", "%0a", "\n").Replace(raw)
	raw = strings.ReplaceAll(raw, "\\", "/")
	if i := strings.LastIndex(raw, "/"); i >= 0 {
		raw = raw[i+1:]
	}
	clean := make([]rune, 0, 255)
	for _, r := range raw {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		if strings.ContainsRune(`:"<>|?*`, r) {
			r = '_'
		}
		clean = append(clean, r)
		if len(clean) == 255 {
			break
		}
	}
	name := strings.Trim(string(clean), " .")
	if name == "" {
		return "download"
	}
	// Windows devices are unsafe save-as names, even with a file extension.
	stem := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" || (len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9') {
		name = "_" + name
		if len([]rune(name)) > 255 {
			name = string([]rune(name)[:255])
		}
	}
	return name
}
func ContentType(raw string) string {
	if len(raw) > 255 {
		return "application/octet-stream"
	}
	value, _, err := mime.ParseMediaType(raw)
	if err != nil || !strings.Contains(value, "/") || len(value) > 255 {
		return "application/octet-stream"
	}
	return strings.ToLower(value)
}
func Disposition(filename string) string {
	return mime.FormatMediaType("attachment", map[string]string{"filename": Filename(filename)})
}
