package node

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	serverconfig "github.com/DigitalArsenal/space-data-network/serverconfig"
)

var (
	currentUnpinQueue []string
	nextUnpinQueue    []string
	queueLock         sync.Mutex
	timerLock         sync.Mutex
)

func (n *Node) onFileProcessed(filePath string, processErr error) {
	if processErr != nil {
		log.Printf("Error processing file onFileProcessed '%s': %v", filePath, processErr)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	addedCid, addErr := n.AddFile(ctx, filePath)
	if addErr != nil {
		log.Printf("Failed to add file '%s' to IPFS: %v", filePath, addErr)
		return
	}

	// Add the CID to the next unpin queue
	nextUnpinQueue = append(nextUnpinQueue, addedCid.String())

	n.createAndPublishPNM(ctx, addedCid.String())
	n.scheduleIPNSUpdate()
}

func (n *Node) createAndPublishPNM(ctx context.Context, cid string) {
	// Implementation for PNM creation and publishing
}

func (n *Node) scheduleIPNSUpdate() {
	timerLock.Lock()
	defer timerLock.Unlock()

	if !n.timerActive {
		if n.publishTimer == nil {
			n.publishTimer = time.AfterFunc(30*time.Second, n.publishIPNS)
		} else {
			n.publishTimer.Reset(30 * time.Second)
		}
		n.timerActive = true
	}
}

func (n *Node) publishIPNS() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Lock the queue and swap current and next
	queueLock.Lock()
	currentUnpinQueue, nextUnpinQueue = nextUnpinQueue, []string{}
	queueLock.Unlock()

	// Unpin files in the current queue
	for _, cid := range currentUnpinQueue {
		err := n.UnpinFile(ctx, cid)
		if err != nil {
			fmt.Println(err)
		} else {
			fmt.Println("Unpinned: " + cid)
		}
	}

	// Reset the timer and timerActive flag
	if n.publishTimer != nil {
		n.publishTimer.Stop()
	}
	n.timerActive = false

	// Publish to IPNS
	CID, err := n.AddFolderToIPNS(ctx, serverconfig.Conf.Folders.RootFolder)
	if err != nil {
		log.Println("Failed to publish to IPNS:", err)
		return
	}

	log.Printf("Published to IPNS: %s \n", CID)
}
