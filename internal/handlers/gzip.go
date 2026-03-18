package handlers

import (
	"compress/gzip"
	"io"
	"log/slog"
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
				next.ServeHTTP(w, r)
				return
			}

			gz := gzipPool.Get().(*gzip.Writer)
			gz.Reset(w)
			defer func() {
				if err := gz.Close(); err != nil {
					slog.Debug("gzip close error", slog.Any("err", err))
				}
				gzipPool.Put(gz)
			}()

			w.Header().Set("Content-Encoding", "gzip") // Indicate that the response is gzip-compressed.
			w.Header().Set("Vary", "Accept-Encoding")  // Indicate that the response varies based on Accept-Encoding.
			w.Header().Del("Content-Length")           // Remove Content-Length since the compressed size is unknown.

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
