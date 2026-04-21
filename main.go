package main

import (
	"context"
	"flag"
	
	"os"
	"os/signal"
	"syscall"
	"time"
	"io"
	"fmt"
	"bufio"
	"log/slog"

	"boot.dev/linko/internal/store"
)

// create closeFunc type that is a function that returns an error
type closeFunc func() error

func initializeLogger(logfile string) (*slog.Logger, closeFunc, error) {
	slogger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if logfile == "" {
		return slogger, func() error { return nil }, nil
	}

	f, err := os.OpenFile(logfile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, nil , err
	}
 
	bufferedFile := bufio.NewWriterSize(io.MultiWriter(os.Stderr, f), 8192)
	logger := slog.New(slog.NewTextHandler(bufferedFile, nil))

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
		logger.Info(fmt.Sprintf("failed to initialize store: %v\n", err))
		return 1
	}
	s := newServer(*st, httpPort, cancel, logger)
	var serverErr error
	go func() {
		serverErr = s.start()
	}()

	

	

	<-ctx.Done()

	// print server has stopped
	logger.Info("Linko is shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

	// defer closeFunc 
	defer func() {
		if err := closeFunc(); err != nil {
			logger.Info(fmt.Sprintf("failed to close log file: %v", err))
		}
	}()
	
	defer cancel()

	if err := s.shutdown(shutdownCtx); err != nil {
		logger.Info(fmt.Sprintf("failed to shutdown server: %v", err))
		return 1
	}
	if serverErr != nil {
		logger.Info(fmt.Sprintf("server error: %v", serverErr))
		return 1
	}
	return 0
}
