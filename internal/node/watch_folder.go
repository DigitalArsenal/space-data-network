package node

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	f_utils "github.com/DigitalArsenal/space-data-network/internal/node/spacedatastandards_utils"
	"github.com/DigitalArsenal/space-data-network/serverconfig"
	"github.com/fsnotify/fsnotify"
)

var debounce = 1 * time.Second

type FileState struct {
	lastModified   time.Time
	watching       bool
	cancelWatchCtx func()
}

type FileProcessedCallback func(filePath string, err error)

type Watcher struct {
	watcher     *fsnotify.Watcher
	timer       *time.Ticker
	doneChan    chan bool
	fileStates  map[string]*FileState
	processChan chan string
	callback    FileProcessedCallback // Callback function
}

func NewWatcher(callback FileProcessedCallback) *Watcher {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatalf("Failed to create fsnotify watcher: %v", err)
	}
	return &Watcher{
		watcher:     w,
		timer:       time.NewTicker(debounce),
		doneChan:    make(chan bool),
		fileStates:  make(map[string]*FileState),
		processChan: make(chan string, 100),
		callback:    callback,
	}
}

func (w *Watcher) Watch(dir string) {
	w.enqueueFiles(dir) // Initial enqueue of existing files

	go func() {
		for {
			select {
			case <-w.timer.C:
				w.enqueueFiles(dir) // Periodic checking
			case filePath := <-w.processChan:
				if err := w.processFile(filePath); err != nil {
					log.Printf("Error processing file Watch '%s': %v", filePath, err)
				}
			case <-w.doneChan:
				return
			}
		}
	}()
}

func (w *Watcher) watchFile(filePath string, state *FileState) {
	if state.watching {
		return // Prevent multiple watchers on the same file
	}

	ctx, cancel := context.WithCancel(context.Background())
	state.cancelWatchCtx = cancel
	state.watching = true

	go func() {
		defer func() {
			cancel()                   // Cancel the context to clean up the goroutine
			state.watching = false     // Reset the watching flag
			w.watcher.Remove(filePath) // Stop watching the file
		}()

		lastWriteTime := time.Now()
		for {
			select {
			case <-ctx.Done(): // Context cancelled, stop watching
				return
			case event := <-w.watcher.Events:
				if event.Name != filePath || event.Op&fsnotify.Write != fsnotify.Write {
					continue // Ignore irrelevant events
				}
				lastWriteTime = time.Now() // Update last write time on write event
			case <-time.After(debounce):
				if time.Since(lastWriteTime) >= debounce {
					// Process the file if there have been no writes for the debounce period
					if err := w.processFile(filePath); err != nil {
						log.Printf("Error processing file Timer'%s': %v", filePath, err)
					}
					return // Processing done, exit goroutine
				}
			}
		}
	}()

	if err := w.watcher.Add(filePath); err != nil {
		log.Printf("Failed to watch file '%s': %v", filePath, err)
		cancel() // Ensure resources are cleaned up on failure to add watcher
		state.watching = false
	}
}

func (w *Watcher) enqueueFiles(dir string) {
	files, err := os.ReadDir(dir)
	if err != nil {
		log.Printf("Failed to read directory '%s': %v", dir, err)
		return
	}

	var fileInfos []os.FileInfo
	for _, file := range files {
		if !file.IsDir() {
			info, err := file.Info()
			if err != nil {
				//log.Printf("Error getting info for file '%s': %v", file.Name(), err)
				continue
			}
			fileInfos = append(fileInfos, info)
		}
	}

	// Sort by modification time, oldest first
	sort.Slice(fileInfos, func(i, j int) bool {
		return fileInfos[i].ModTime().Before(fileInfos[j].ModTime())
	})

	// Enqueue only the next file to be processed based on modification time
	if len(fileInfos) > 0 {
		nextFile := fileInfos[0] // Assuming the oldest file is the next to be processed
		filePath := filepath.Join(dir, nextFile.Name())
		state, exists := w.fileStates[filePath]
		if !exists {
			state = &FileState{lastModified: nextFile.ModTime()}
			w.fileStates[filePath] = state
		}

		w.watchFile(filePath, state)
	}
}

func (w *Watcher) processFile(filePath string) error {
	var file *os.File
	var err error

	// Retry opening the file up to 3 times with a 2-second delay between retries
	for i := 0; i < 5; i++ {
		file, err = os.OpenFile(filePath, os.O_RDWR|os.O_EXCL, 0666)
		if err == nil {
			defer file.Close()
			break // Successfully opened the file, exit the retry loop
		}

		if os.IsNotExist(err) {
			// If the file does not exist, no need to retry
			return fmt.Errorf("file '%s' does not exist", filePath)
		}

		log.Printf("Attempt %d: Error opening file '%s': %v", i+1, filePath, err)
		time.Sleep(2 * time.Second) // Wait for 2 seconds before retrying
	}

	if err != nil {
		// Failed to open the file after retries
		return fmt.Errorf("failed to open file '%s' after retries: %v", filePath, err)
	}

	// Use ReadDataFromSource to read all FlatBuffers from the file
	flatBuffers, err := f_utils.ReadDataFromSource(context.Background(), file)
	if err != nil {
		return fmt.Errorf("failed to read FlatBuffers from file '%s': %v", filePath, err)
	}

	if len(flatBuffers) < 12 {
		return fmt.Errorf("invalid FlatBuffers data in file '%s'", filePath)
	}

	fileStandard := strings.Split(f_utils.FID(flatBuffers), "$")[1]
	found := false

	for _, standard := range serverconfig.Conf.Info.Standards {
		if standard == fileStandard {
			found = true
			break
		}
	}

	if found {
		// Construct the outgoing path using the RootFolder, fileStandard, and the base file name
		outgoingPath := filepath.Join(serverconfig.Conf.Folders.RootFolder, fileStandard, filepath.Base(filePath))

		// Create the directory structure if it doesn't exist
		if err = os.MkdirAll(filepath.Dir(outgoingPath), os.ModePerm); err != nil {
			return fmt.Errorf("failed to create directories for the outgoing path: %v", err)
		}

		// Move the file to the constructed outgoing path
		if err = os.Rename(filePath, outgoingPath); err != nil {
			return fmt.Errorf("failed to move file to the outgoing folder: %v", err)
		}
		if w.callback != nil {
			w.callback(outgoingPath, err) // Invoke the callback
		}
	} else {
		// Delete the file
		if err = os.Remove(filePath); err != nil {
			return fmt.Errorf("failed to delete file: %v", err)
		}
		fmt.Printf("File not a SpaceDataStandard.org flatbuffer, deleted: %s\n", filePath)
	}

	return err
}

// Unwatch stops the timer and cleans up resources
func (w *Watcher) Unwatch() {
	w.doneChan <- true
	close(w.doneChan)
	w.timer.Stop()
}
