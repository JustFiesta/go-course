package utils

import (
	"archive/zip"
	"bytes"
	"fmt"
	"log"
	"time"
)

// RetryWithBackoff executes fn up to attempts times with exponential backoff.
// Stops and returns nil as soon as fn succeeds.
func RetryWithBackoff(attempts int, fn func() error) error {
	var lastErr error
	for i := 0; i < attempts; i++ {
		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err
			wait := time.Duration(1<<uint(i)) * time.Second
			log.Printf("[retry] attempt %d/%d failed: %v — retrying in %s", i+1, attempts, err, wait)
			time.Sleep(wait)
		}
	}
	return fmt.Errorf("all %d attempts failed: %w", attempts, lastErr)
}

// PollUntil polls condition every interval until timeout is reached.
// condition should return (true, nil) when the desired state is reached.
func PollUntil(timeout, interval time.Duration, condition func() (bool, error)) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		done, err := condition()
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		time.Sleep(interval)
	}
	return fmt.Errorf("timed out after %s — condition not met", timeout)
}

// ZipFiles packs a map of {filename → content} into an in-memory ZIP archive.
// Returns bytes ready to be uploaded as a Lambda deployment package.
func ZipFiles(files map[string][]byte) ([]byte, error) {
	buf := new(bytes.Buffer)
	w := zip.NewWriter(buf)

	for name, content := range files {
		f, err := w.Create(name)
		if err != nil {
			return nil, fmt.Errorf("zip create %s: %w", name, err)
		}
		if _, err = f.Write(content); err != nil {
			return nil, fmt.Errorf("zip write %s: %w", name, err)
		}
	}

	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("zip close: %w", err)
	}
	return buf.Bytes(), nil
}