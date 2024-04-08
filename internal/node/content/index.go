package content

import (
	"embed"
	"log"
	"os"
	"path/filepath"
	"text/template"
)

// Embed the index.html file
//
//go:embed assets/index.html
var indexHTML embed.FS

type NodeData struct {
	PublicKey         string
	PeerID            string
	SigningAddress    string
	EncryptionAddress string
	Base36CIDv1       string
}

func WriteNodeInfoToTemplate(hexPublicKey, peerID, signingAddress, encryptionAddress, peerCidStr, rootDir string) {
	// Create an instance of NodeData with your data
	data := NodeData{
		PublicKey:         hexPublicKey,
		PeerID:            peerID,
		SigningAddress:    signingAddress,
		EncryptionAddress: encryptionAddress,
		Base36CIDv1:       peerCidStr,
	}

	// Read the embedded 'index.html' template
	tmplData, err := indexHTML.ReadFile("assets/index.html")
	if err != nil {
		log.Fatalf("Failed to read embedded 'index.html': %v", err)
	}

	// Parse the template from the read content
	tmpl, err := template.New("index").Parse(string(tmplData))
	if err != nil {
		log.Fatalf("Failed to parse 'index.html' template: %v", err)
	}

	// Create the 'index.html' file in the 'root' directory
	indexPath := filepath.Join(rootDir, "index.html")
	indexFile, err := os.Create(indexPath)
	if err != nil {
		log.Fatalf("Failed to create 'index.html': %v", err)
	}
	defer indexFile.Close()

	// Execute the template with the data and write to indexFile
	if err := tmpl.Execute(indexFile, data); err != nil {
		log.Fatalf("Failed to execute template: %v", err)
	}
}
