package node

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type FileItem struct {
	Path     string
	LastSeen time.Time
}

type OrderedQueue struct {
	Items []FileItem
	mu    sync.Mutex
}

// Add updates the item's LastSeen time if it exists, or appends it if it doesn't.
func (q *OrderedQueue) Add(item FileItem) {
	q.mu.Lock()
	defer q.mu.Unlock()

	for i, existingItem := range q.Items {
		if existingItem.Path == item.Path {
			q.Items[i].LastSeen = item.LastSeen // Update LastSeen time
			return
		}
	}

	q.Items = append(q.Items, item) // Add new item
}

type FolderWatcher struct {
	node           *Node
	dir            string
	cancel         context.CancelFunc
	queue          OrderedQueue
	ticker         *time.Ticker
	processedFiles map[string]time.Time // Track processed files to avoid re-processing
	procFilesMu    sync.Mutex           // Mutex for processedFiles map
	beingProcessed map[string]struct{}  // Map to track files being processed
}

func NewFolderWatcher(node *Node, dir string) *FolderWatcher {
	return &FolderWatcher{
		node:           node,
		dir:            dir,
		queue:          OrderedQueue{},
		ticker:         time.NewTicker(1 * time.Second),
		processedFiles: make(map[string]time.Time),
		beingProcessed: make(map[string]struct{}),
	}
}

func (fw *FolderWatcher) Start(ctx context.Context) {
	_, fw.cancel = context.WithCancel(ctx)
	fw.checkFolder() // Initial check to queue existing files

	go func() {
		for {
			select {
			case <-fw.ticker.C:
				fw.checkFolder()
			case <-ctx.Done():
				fw.ticker.Stop()
				return
			}
		}
	}()
}

func (fw *FolderWatcher) checkFolder() {
	entries, err := os.ReadDir(fw.dir)
	if err != nil {
		log.Printf("Error reading directory: %v", err)
		return
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			//log.Printf("Error getting info for file: %v", err)
			continue
		}

		filePath := filepath.Join(fw.dir, entry.Name())
		fw.procFilesMu.Lock()
		if _, processed := fw.processedFiles[filePath]; !processed {
			fw.queue.Add(FileItem{Path: filePath, LastSeen: info.ModTime()})
		}
		fw.procFilesMu.Unlock()
	}

	fw.processQueue()
}

func (fw *FolderWatcher) processQueue() {
	for {
		fw.queue.mu.Lock()
		if len(fw.queue.Items) == 0 {
			fw.queue.mu.Unlock()
			return // Exit if there are no items to process
		}

		// Get the oldest file from the queue
		item := fw.queue.Items[0]

		// Check if the file is already being processed or has been processed recently
		_, beingProcessed := fw.beingProcessed[item.Path]
		fw.procFilesMu.Lock()
		_, processed := fw.processedFiles[item.Path]
		fw.procFilesMu.Unlock()

		if beingProcessed || processed {
			// If already processed or being processed, remove from queue and skip
			fw.queue.Items = fw.queue.Items[1:]
			fw.queue.mu.Unlock()
			continue
		}

		if time.Since(item.LastSeen) < 5*time.Second {
			fw.queue.mu.Unlock()
			time.Sleep(1 * time.Second) // Wait for stability
			continue                    // Re-check the stability of the file after the wait
		}

		// File is ready for processing; remove it from the queue and mark as being processed
		fw.queue.Items = fw.queue.Items[1:]
		fw.beingProcessed[item.Path] = struct{}{}
		fw.queue.mu.Unlock()

		// Process the file
		go fw.processFile(item)
	}
}

func (fw *FolderWatcher) processFile(item FileItem) {
	// Send the file for processing
	fw.node.readyFilesChan <- item.Path

	// Mark the file as processed
	fw.procFilesMu.Lock()
	fw.processedFiles[item.Path] = time.Now()
	delete(fw.beingProcessed, item.Path)
	fw.procFilesMu.Unlock()
}

func (fw *FolderWatcher) Stop() {
	if fw.cancel != nil {
		fw.cancel()
	}
}
