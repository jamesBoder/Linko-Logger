package main

import (
	"context"
	"net/http"
	"log/slog"



	pkgerr "github.com/pkg/errors"
	"golang.org/x/crypto/bcrypt"
)

type contextKey string

const UserContextKey contextKey = "user"

var allowedUsers = map[string]string{
	"frodo":   "$2a$10$B6O/n6teuCzpuh66jrUAdeaJ3WvXcxRkzpN0x7H.di9G9e/NGb9Me",
	"samwise": "$2a$10$EWZpvYhUJtJcEMmm/IBOsOGIcpxUnGIVMRiDlN/nxl1RRwWGkJtty",
	// frodo: "ofTheNineFingers"
	// samwise: "theStrong"
	"saruman": "invalidFormat",
}

func (s *server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok {
			httpError(r.Context(), w, http.StatusUnauthorized, pkgerr.New("missing or invalid Authorization header"))
			return
		}
		stored, exists := allowedUsers[username]
		if !exists {
			httpError(r.Context(), w, http.StatusUnauthorized, pkgerr.New("unauthorized"))
			return
		}
		ok, err := s.validatePassword(password, stored)
		if err != nil {
			s.logger.Error("error validating password", slog.String("user", username), slog.Any("error", err))
			httpError(r.Context(), w, http.StatusInternalServerError, pkgerr.New("unauthorized"))
			return
		}
		if !ok {
			httpError(r.Context(), w, http.StatusUnauthorized, pkgerr.New("unauthorized"))
			return
		}

		// read and type-assert *LogContext from the request context and set Username. If the type assertion fails, log an error and return a 500 Internal Server Error response.	
		logContext, ok := r.Context().Value(logContextKey).(*LogContext)
		if !ok {
			s.logger.Error("error getting log context from request context", slog.String("user", username))
			httpError(r.Context(), w, http.StatusInternalServerError, pkgerr.New("error getting log context"))
			return
		}
		logContext.Username = username


		// store the username in the request context with the UserContextKey and call the next handler in the chain with the modified request
		r = r.WithContext(context.WithValue(r.Context(), UserContextKey, username))
		next.ServeHTTP(w, r)
	})
}



func (s *server) validatePassword(password, stored string) (bool, error) {
	err := bcrypt.CompareHashAndPassword([]byte(stored), []byte(password))
	if err == bcrypt.ErrMismatchedHashAndPassword {
		return false, nil
	}
	if err != nil {
		// wrap the error with stack trace and return it
		return false, pkgerr.WithStack(err)
	}
	return true, nil
}
