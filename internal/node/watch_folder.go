package node

import (
	"context"
	"fmt"
	"log"
	"os"

	spacedatastandards_utils "github.com/DigitalArsenal/space-data-network/internal/node/spacedatastandards_utils"
	"github.com/fsnotify/fsnotify"
)

type Watcher struct {
	watcher  *fsnotify.Watcher
	doneChan chan bool
}

// NewWatcher creates a new Watcher instance
func NewWatcher() *Watcher {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatalf("Failed to create fsnotify watcher: %v", err)
	}
	return &Watcher{
		watcher:  w,
		doneChan: make(chan bool),
	}
}

func (w *Watcher) Watch(dir string) {

	w.processExistingFiles(dir)

	go func() {
		for {
			select {
			case event, ok := <-w.watcher.Events:
				if !ok {
					return
				}
				if event.Op&fsnotify.Write == fsnotify.Write {
					filePath := event.Name
					if err := w.processFile(filePath); err != nil {
						log.Printf("Error processing file '%s': %v", filePath, err)
					}
					fmt.Println()
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
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file '%s': %v", filePath, err)
	}
	defer file.Close()
	fmt.Println("TEST", file)
	ctx := context.Background() // You can use context.WithTimeout to set a timeout if needed
	data, fileID, err := spacedatastandards_utils.ReadDataFromSource(ctx, file)
	if err != nil {
		return fmt.Errorf("failed to read data from file '%s': %v", filePath, err)
	}

	fmt.Println(data)
	fmt.Println(fileID)
	// Your logic to handle the data and file ID
	// e.g., publishDataToIPNS(data, fileID)

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
