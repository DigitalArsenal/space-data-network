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
)

func (n *Node) processFile(filePath string) error {
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

	} else {
		// Delete the file
		if err = os.Remove(filePath); err != nil {
			return fmt.Errorf("failed to delete file: %v", err)
		}
		fmt.Printf("File not a SpaceDataStandard.org flatbuffer, deleted: %s\n", filePath)
	}

	return err
}
