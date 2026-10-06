//go:build !unix

package uploads

import "os"

// Fail closed where Unix ownership/mode guarantees are unavailable. Windows
// development runs the API/worker in the supplied Linux Docker environment.
func owned(os.FileInfo) bool { return false }
