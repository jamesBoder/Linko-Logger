package main

import (
	"context"
	"flag"
	
	"os"
	"os/signal"
	"syscall"
	"time"
	"errors"
	"fmt"
	"strings"
	"slices"
	"net/url"
	"log/slog"
	
	pkgerr "github.com/pkg/errors"
	"gopkg.in/natefinch/lumberjack.v2"
	"github.com/mattn/go-isatty"
	"github.com/lmittmann/tint"
	"boot.dev/linko/internal/store"
	"boot.dev/linko/internal/linkoerr"
	"boot.dev/linko/internal/build"

	
)



// create closeFunc type that is a function that returns an error
type closeFunc func() error

// define a stackTracer interface that has an error method and a StackTrace method that returns a pkgerr.StackTrace
type stackTracer interface {
	error
	StackTrace() pkgerr.StackTrace
}

// 
type multiError interface {
	error
	Unwrap() []error
}

//func errorAttrs(err error) []slog.Attr which builds the attrs slice with:

   // A message attribute with the error's message
   // Any linkoerr attributes that can be extracted from the error
   //  The stack_trace attribute (only if the error is a stackTracer)

func errorAttrs(err error) []slog.Attr {
	attrs := []slog.Attr{
		slog.String("message", err.Error()),
	}
	
	attrs = append(attrs, linkoerr.Attrs(err)...)

	if stErr, ok := err.(stackTracer); ok {
		attrs = append(attrs, slog.Any("stack_trace", fmt.Sprintf("%+v", stErr.StackTrace())))
	}
	return attrs
}

// update to detect multiple errors with multiError and extract stack traces from all errors in the chain. Add a security filter to your replaceAttr function. If a log attribute's key matches a list of sensitive key names (e.g. password, key, apikey, secret, pin, creditcardno), replace its value with [REDACTED]. Use slices.Contains to check the key against your list.
func replaceAttr(groups []string, a slog.Attr) slog.Attr {
	var sensitiveKeys = []string{"password", "key", "apikey", "secret", "pin", "creditcardno", "user"}
	// if the attribute key is in the list of sensitive keys, replace its value with [REDACTED]
	if slices.Contains(sensitiveKeys, strings.ToLower(a.Key)) {
		return slog.Any(a.Key, "[REDACTED]")
	}

	// Also check string values for URLs that contain embedded passwords (using url.Parse and URL.User), and redact the password portion if present.
	if a.Value.Kind() == slog.KindString {
		strVal := a.Value.String() 
		if u, err := url.Parse(strVal); err == nil && u.User != nil {
			if _, hasPassword := u.User.Password(); hasPassword {
				u.User = url.UserPassword(u.User.Username(), "[REDACTED]")
				return slog.Any(a.Key, u.String())
			}
		}
	}
		

    if a.Key == "error" {
        err, ok := a.Value.Any().(error)
        if !ok {
            return a
        }
        if multiErr, ok := errors.AsType[multiError](err); ok {
            var allAttrs []slog.Attr
            for i, err := range multiErr.Unwrap() {
                allAttrs = append(allAttrs, slog.Any(
                    fmt.Sprintf("error_%d", i+1),
                    slog.GroupValue(errorAttrs(err)...),
                ))
            }
            return slog.Any("errors", slog.GroupValue(allAttrs...))
        }
        return slog.Any("error", slog.GroupValue(errorAttrs(err)...))
    }
    return a
}
				

func initializeLogger(logfile string) (*slog.Logger, closeFunc, error) {

	// Set the NoColor option on tint.NewHandler to true if you're not in a tty environment. Use both isatty.IsCygwinTerminal and isatty.IsTerminal, and enable color if either returns true.
	debugHandler := tint.NewHandler(os.Stderr, &tint.Options{
		Level: slog.LevelDebug,
		ReplaceAttr: replaceAttr,
		NoColor: !(isatty.IsTerminal(os.Stderr.Fd()) || isatty.IsCygwinTerminal(os.Stderr.Fd())),
	})


	if logfile == "" {
		return slog.New(debugHandler), func() error { return nil }, nil
	}

	// use lumberjack.logger to create a rolling log file
	f := &lumberjack.Logger{
		Filename:   logfile,
		MaxSize:    1, // megabytes
		MaxAge:     28,   //days
		MaxBackups: 10,
		LocalTime: false,
		Compress:   true, // disabled by default
	}

	infoHandler := slog.NewJSONHandler(f, &slog.HandlerOptions{ReplaceAttr: replaceAttr, Level: slog.LevelInfo})

	logger := slog.New(slog.NewMultiHandler(debugHandler, infoHandler))



	//  update closeFunc to use logger.close() on the lumberjack logger
	closeFunc := func() error {
		if err := f.Close(); err != nil {
			return pkgerr.WithStack(err)
		}
		return nil
	}

	return logger, closeFunc, nil
}




func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	httpPort := flag.Int("port", 8899, "port to listen on")
	dataDir := flag.String("data", "./data", "directory to store data")
	flag.Parse()

	status := run(ctx, cancel, *httpPort, *dataDir)
	cancel()
	
	os.Exit(status)
}

func run(ctx context.Context, cancel context.CancelFunc, httpPort int, dataDir string) int {

	// create a single logger that uses initializeLogger to write to both stderr and a file. the log file path should come from the LINKO_LOG_FILE environment variable. if the environment variable is not set, the logger should just write to stderr.
	logFile := os.Getenv("LINKO_LOG_FILE")
	logger, closeFunc, err := initializeLogger(logFile)
	if err != nil {
		fmt.Printf("failed to initialize logger: %v\n", err)
		return 1
	}

	env := os.Getenv("ENV")
	hostname, _ := os.Hostname()

	logger = logger.With(
		slog.String("git_sha", build.GitSHA),
		slog.String("build_time", build.BuildTime),
		slog.String("env", env),
		slog.String("hostname", hostname),
	)
	

	st, err := store.New(dataDir, logger)
	if err != nil {
		// update this to be logged at ERROR level instead of INFO
		logger.Error(fmt.Sprintf("failed to initialize store: %v\n", err))
		return 1
	}

	// log server startup
	logger.Debug("Linko is starting up")

	s := newServer(*st, httpPort, cancel, logger)
	var serverErr error
	go func() {
		serverErr = s.start()
	}()

	

	

	<-ctx.Done()

	// print server has stopped
	// Update the startup and shutdown messages to be logged at the DEBUG level.
	logger.Debug("Linko is shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

	// defer closeFunc 
	defer func() {
		if err := closeFunc(); err != nil {
			logger.Error(fmt.Sprintf("failed to close log file: %v", err))
		}
	}()
	
	defer cancel()

	if err := s.shutdown(shutdownCtx); err != nil {
		logger.Error(fmt.Sprintf("failed to shutdown server: %v", err))
		return 1
	}
	if serverErr != nil {
		logger.Error(fmt.Sprintf("server error: %v", serverErr))
		return 1
	}
	return 0
}
