package node

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"time"
)

type FileItem struct {
	Path     string
	LastSeen time.Time
}

type OrderedQueue struct {
	Items []FileItem
}

func (q *OrderedQueue) Add(item FileItem) {
	for i, existingItem := range q.Items {
		if existingItem.Path == item.Path {
			q.Items[i].LastSeen = item.LastSeen
			return
		}
	}
	q.Items = append(q.Items, item)
}

type FolderWatcher struct {
	node           *Node
	dir            string
	cancel         context.CancelFunc
	queue          OrderedQueue
	ticker         *time.Ticker
	processedFiles map[string]time.Time
	beingProcessed map[string]struct{}
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
	fw.checkFolder()

	for {
		select {
		case <-fw.ticker.C:
			fw.checkFolder()
		case <-ctx.Done():
			fw.ticker.Stop()
			return
		}
		fw.processQueue()
	}
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
			continue
		}

		filePath := filepath.Join(fw.dir, entry.Name())
		if _, processed := fw.processedFiles[filePath]; !processed {
			fw.queue.Add(FileItem{Path: filePath, LastSeen: info.ModTime()})
		}
	}
}

func (fw *FolderWatcher) processQueue() {
	for len(fw.queue.Items) > 0 {
		item := fw.queue.Items[0]
		_, beingProcessed := fw.beingProcessed[item.Path]
		_, processed := fw.processedFiles[item.Path]

		if beingProcessed || processed {
			fw.queue.Items = fw.queue.Items[1:]
			continue
		}

		if time.Since(item.LastSeen) < 5*time.Second {
			time.Sleep(1 * time.Second)
			continue
		}

		fw.queue.Items = fw.queue.Items[1:]
		fw.beingProcessed[item.Path] = struct{}{}
		fw.sendFile(item)
	}
}

func (fw *FolderWatcher) sendFile(item FileItem) {
	fw.node.readyFilesChan <- item.Path
	fw.processedFiles[item.Path] = time.Now()
	delete(fw.beingProcessed, item.Path)
}

func (fw *FolderWatcher) Stop() {
	if fw.cancel != nil {
		fw.cancel()
	}
}
