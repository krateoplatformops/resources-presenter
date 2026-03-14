package handlers

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"sync"
)

var gzipPool = sync.Pool{
	New: func() any {
		w, _ := gzip.NewWriterLevel(io.Discard, gzip.DefaultCompression)
		return w
	},
}

// Gzip returns middleware that compresses responses with gzip when the
// client sends Accept-Encoding: gzip. It sets Content-Encoding and
// removes Content-Length (since the compressed size is unknown upfront).
func Gzip() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
				next.ServeHTTP(w, r) // Client doesn't support gzip, serve uncompressed.
				return
			}

			gz := gzipPool.Get().(*gzip.Writer)
			gz.Reset(w)
			defer func() {
				gz.Close()
				gzipPool.Put(gz)
			}()

			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Del("Content-Length") // Remove Content-Length since the compressed size is unknown.

			next.ServeHTTP(&gzipResponseWriter{ResponseWriter: w, Writer: gz}, r)
		})
	}
}

// gzipResponseWriter wraps http.ResponseWriter to route Write calls
// through a gzip.Writer.
type gzipResponseWriter struct {
	http.ResponseWriter
	Writer *gzip.Writer
}

func (g *gzipResponseWriter) Write(data []byte) (int, error) {
	return g.Writer.Write(data)
}
