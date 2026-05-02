package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"log/slog"
	"time"
	"io"
	"strings"
	"crypto/rand"
	
	"boot.dev/linko/internal/store"
)

type server struct {
	httpServer *http.Server
	store      store.Store
	cancel     context.CancelFunc
	logger *slog.Logger
}

// spyReadCloser is a wrapper around an io.ReadCloser that counts the number of bytes read. This can be used to log the size of request bodies.
type spyReadCloser struct {
	io.ReadCloser
	bytesRead int
}

// override the Read method to count the number of bytes read
func (r *spyReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.bytesRead += n
	return n, err
}

type spyResponseWriter struct {
	http.ResponseWriter
	bytesWritten int
	statusCode   int
}

func (w *spyResponseWriter) Write(p []byte) (int, error) {
	if w.statusCode == 0 {
		w.statusCode = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytesWritten += n
	return n, err
}

func (w *spyResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

const logContextKey contextKey = "log_context"

type LogContext struct {
	Username string
	Error  error
}

// create redactIP function that takes an IP address as a string and replaces the final octet of any IPv4 address with x. Use net.SplitHostPort and net.ParseIP to parse the address. Non-IPv4 addresses should be returned unchanged.
func redactIP(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return addr
	}
	if ip.To4() != nil {
		octets := strings.Split(host, ".")
		if len(octets) == 4 {
			octets[3] = "x"
			return strings.Join(octets, ".")
		}
	}
	return addr
}


// update httpError to to replace the raw error text for 401, 403, and 500 responses with a generic status message from http.StatusText.
func httpError(ctx context.Context, w http.ResponseWriter, status int, err error) {
	logContext, ok := ctx.Value(logContextKey).(*LogContext)
	if ok {
		logContext.Error = err
	}

	if status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusInternalServerError {
		http.Error(w, http.StatusText(status), status)
	} else {
		//  extract the plain text message from an error value and include it in the response body for all other status codes. This will allow us to return more detailed error messages for client errors (e.g. 400 Bad Request) while still hiding internal error details for server errors (e.g. 500 Internal Server Error).
		http.Error(w, err.Error(), status)
	}
}

// Update your requestLogger middleware to include the error from the log context in an "error" attribute if it exists.
func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			spyReader := &spyReadCloser{ReadCloser: r.Body}
			r.Body = spyReader
			start := time.Now()
			spyWriter := &spyResponseWriter{ResponseWriter: w}
			

			// create a *LogContext. and store it on the request with r.WithContext and context.WithValue
			 logContext := &LogContext{}
			 r = r.WithContext(context.WithValue(r.Context(), logContextKey, logContext))
			 

			 // call the next handler in the chain with the spyResponseWriter and the modified request
			next.ServeHTTP(spyWriter, r)

			attrs := []any {
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("client_ip", redactIP(r.RemoteAddr)),
				slog.Duration("duration", time.Since(start)),
				slog.Int("request_body_bytes", spyReader.bytesRead),
				slog.Int("response_status", spyWriter.statusCode),
				slog.Int("response_body_bytes", spyWriter.bytesWritten),
			}

			// read the request ID from the response header and include it as a "request_id" attribute in the log entry
			requestID := spyWriter.Header().Get("X-Request-ID")

			// if the request ID is not empty, add it to the log attributes
			if requestID != "" {
				attrs = append(attrs, slog.String("request_id", requestID))
			}
			
			// if the log context has a username, add it to the log attributes
			if logContext.Username != "" {
				attrs = append(attrs, slog.String("user", logContext.Username))
			}
			
			// if the log context has an error, add it to the log attributes
			if logContext.Error != nil {
				attrs = append(attrs, slog.Any("error", logContext.Error))
			}

			 // log the request method, path, client IP, duration, request body size, response status code, and response body size using the provided logger. Use slog.String and slog.Int to log the values with appropriate keys.
			logger.Info("Served request", attrs...)
		})	
	}
}

// create a request ID middleware that reads X-Request-ID from the inbound request. If one isn't present, generate one with rand.Text. Before calling next.ServeHTTP, set the request ID on the response header with w.Header().Set.
func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = rand.Text()
		}
		w.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(w, r)
	})
}


func newServer(store store.Store, port int, cancel context.CancelFunc, logger *slog.Logger) *server {
	mux := http.NewServeMux()

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: requestIDMiddleware(requestLogger(logger)(mux)),
	}

	s := &server{
		httpServer: srv,
		store:      store,
		cancel:     cancel,
		logger: logger,

	}

	mux.HandleFunc("GET /", s.handlerIndex)
	mux.Handle("POST /api/login", s.authMiddleware(http.HandlerFunc(s.handlerLogin)))
	mux.Handle("POST /api/shorten", s.authMiddleware(http.HandlerFunc(s.handlerShortenLink)))
	mux.Handle("GET /api/stats", s.authMiddleware(http.HandlerFunc(s.handlerStats)))
	mux.Handle("GET /api/urls", s.authMiddleware(http.HandlerFunc(s.handlerListURLs)))
	mux.HandleFunc("GET /{shortCode}", s.handlerRedirect)
	mux.HandleFunc("POST /admin/shutdown", s.handlerShutdown)

	return s
}

func (s *server) start() error {
	ln, err := net.Listen("tcp", s.httpServer.Addr)
	if err != nil {
		return err
	}
	// print server is running. 
	s.logger.Info("Linko is running", 
    "port", ln.Addr().(*net.TCPAddr).Port,
	)

	if err := s.httpServer.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	
	return nil
}

func (s *server) shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

func (s *server) handlerShutdown(w http.ResponseWriter, r *http.Request) {
	if os.Getenv("ENV") == "production" {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusOK)
	go s.cancel()
}
