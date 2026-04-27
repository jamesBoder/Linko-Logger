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
	"bufio"
	"log/slog"
	
	pkgerr "github.com/pkg/errors"
	"boot.dev/linko/internal/store"
	"boot.dev/linko/internal/linkoerr"
	
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

// update to detect multiple errors with multiError and extract stack traces from all errors in the chain
func replaceAttr(groups []string, a slog.Attr) slog.Attr {
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
	debugHandler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{ReplaceAttr: replaceAttr, Level: slog.LevelDebug})

	

	if logfile == "" {
		return slog.New(debugHandler), func() error { return nil }, nil
	}

	f, err := os.OpenFile(logfile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, nil , err
	}
 
	bufferedFile := bufio.NewWriterSize(f, 8192) // 8KB buffer

	infoHandler := slog.NewJSONHandler(bufferedFile, &slog.HandlerOptions{ReplaceAttr: replaceAttr, Level: slog.LevelInfo})

	logger := slog.New(slog.NewMultiHandler(debugHandler, infoHandler))



	//  define a function that calls bufferedFile.Flush() and f.Close(), then return that function.
	closeFunc := func() error {
		if err := bufferedFile.Flush(); err != nil {
			return err
		}
		return f.Close()
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
