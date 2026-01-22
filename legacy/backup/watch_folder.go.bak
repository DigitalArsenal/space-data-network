package node

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	f_utils "github.com/DigitalArsenal/space-data-network/internal/node/spacedatastandards_utils"
	"github.com/DigitalArsenal/space-data-network/serverconfig"
	"github.com/fsnotify/fsnotify"
)

type FileState struct {
	lastWrite time.Time
}

type Watcher struct {
	watcher             *fsnotify.Watcher
	doneChan            chan bool
	fileStates          map[string]*FileState
	processFileCallback func(filename string)
}

// NewWatcher creates a new Watcher instance
func NewWatcher() *Watcher {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatalf("Failed to create fsnotify watcher: %v", err)
	}
	w.Events = make(chan fsnotify.Event, 50000) // Increase buffer size
	return &Watcher{
		watcher:    w,
		doneChan:   make(chan bool),
		fileStates: make(map[string]*FileState),
	}
}

func (w *Watcher) Watch(dir string, callback func(filename string)) {
	w.processFileCallback = callback
	w.processExistingFiles(dir)

	go func() {
		for {
			select {
			case event, ok := <-w.watcher.Events:
				if !ok {
					return
				}

				filePath := event.Name
				now := time.Now()

				// Process the file immediately if it's a new file or last write was less than a minute ago
				if event.Op&fsnotify.Create == fsnotify.Create {
					state, exists := w.fileStates[filePath]
					if !exists || now.Sub(state.lastWrite) > time.Minute {
						if err := w.processFile(filePath); err != nil {
							log.Printf("Error processing file '%s': %v", filePath, err)
						}
						// Update the last write time for this file
						w.fileStates[filePath] = &FileState{lastWrite: now}
					}
				}

				// Loop through all files and delete those that have been inactive for more than a minute
				for fPath, state := range w.fileStates {
					if now.Sub(state.lastWrite) > time.Minute {
						// File has been inactive for more than a minute, remove from tracking
						delete(w.fileStates, fPath)
					}
				}

			case err, ok := <-w.watcher.Errors:
				if !ok {
					return
				}
				log.Println("error:", err)

			case <-w.doneChan:
				return
			}
		}
	}()

	err := w.watcher.Add(dir)
	if err != nil {
		log.Fatalf("Failed to watch directory '%s': %v", dir, err)
	}
}

func (w *Watcher) processFile(filePath string) error {
	file, err := os.OpenFile(filePath, os.O_RDWR|os.O_EXCL, 0666)
	if err != nil {
		delete(w.fileStates, filePath)
		return fmt.Errorf("failed to open file '%s': %v", filePath, err)
	}
	defer file.Close()

	// Use ReadDataFromSource to read all FlatBuffers from the file
	flatBuffers, err := f_utils.ReadDataFromSource(context.Background(), file)
	if err != nil {
		delete(w.fileStates, filePath)
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
		if err := os.MkdirAll(filepath.Dir(outgoingPath), os.ModePerm); err != nil {
			return fmt.Errorf("failed to create directories for the outgoing path: %v", err)
		}

		// Move the file to the constructed outgoing path
		if err := os.Rename(filePath, outgoingPath); err != nil {
			return fmt.Errorf("failed to move file to the outgoing folder: %v", err)
		}
		w.processFileCallback(outgoingPath)

		// fmt.Println("File moved to the outgoing folder:", outgoingPath)
	} else {
		// Delete the file
		if err := os.Remove(filePath); err != nil {
			return fmt.Errorf("failed to delete file: %v", err)
		}
		fmt.Printf("File not a SpaceDataStandard.org flatbuffer, deleted: %s\n", filePath)
	}
	return nil
}

func (w *Watcher) processExistingFiles(dir string) {
	files, err := os.ReadDir(dir)
	if err != nil {
		log.Printf("Failed to read directory '%s': %v", dir, err)
		return
	}

	for _, file := range files {
		if !file.IsDir() {
			filePath := fmt.Sprintf("%s/%s", dir, file.Name())
			if err := w.processFile(filePath); err != nil {
				log.Printf("Error processing existing file '%s': %v", filePath, err)
				w.fileStates[filePath] = &FileState{lastWrite: time.Now()}
			}
		}
	}
}

// Unwatch stops watching the directories and cleans up resources
func (w *Watcher) Unwatch() {
	w.doneChan <- true
	close(w.doneChan)

	err := w.watcher.Close()
	if err != nil {
		log.Printf("Failed to close watcher: %v", err)
	}
}
