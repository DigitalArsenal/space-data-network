package node

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	config "github.com/DigitalArsenal/space-data-network/configs"
	spacedatastandards_utils "github.com/DigitalArsenal/space-data-network/internal/node/spacedatastandards_utils"
	"github.com/DigitalArsenal/space-data-network/internal/spacedatastandards/EPM"
	"github.com/mdp/qrterminal/v3"
	qrcode "github.com/skip2/go-qrcode"
)

func captureStackTrace() string {
	var builder strings.Builder
	pc := make([]uintptr, 10) // Increase the size if necessary
	n := runtime.Callers(2, pc)
	frames := runtime.CallersFrames(pc[:n])

	for {
		frame, more := frames.Next()
		builder.WriteString(fmt.Sprintf("%s:%d %s\n", frame.File, frame.Line, frame.Function))
		if !more {
			break
		}
	}

	return builder.String()
}
func setupNode() *Node {

	// Prompt for KeyStore password
	fmt.Print("KeyStore Password (leave blank if none has been set): ")
	password, err := readPassword()
	if err != nil {
		fmt.Printf("Failed to read password: %v\n", err)
		return nil
	}

	// Set the configuration property for KeyStore password
	config.Conf.Datastore.Password = strings.TrimSpace(password)

	// Create a new node, which will use the updated configuration for its KeyStore
	newNode, err := NewSDNNode(context.Background(), "")
	if err != nil {
		fmt.Printf("Failed to create new node: %v\n", err)
		return nil
	}
	return newNode
}

func CreateDefaultServerEPM(n *Node) {
	// Load existing EPM from the KeyStore
	return
	/*
		vepm, _ := n.KeyStore.LoadEPM()
		if len(vepm) > 0 {
			return
		}

		fmt.Println("Creating a server EPM...")

		// Generate email using peerID
		peerID := n.Host.ID()
		email := fmt.Sprintf("%s@spacedatanetwork.digitalarsenal.io", peerID)

		// Set default values for required fields
		legalName := "Default Organization"
		familyName := ""
		givenName := ""
		additionalName := ""
		honorificPrefix := ""
		honorificSuffix := ""
		jobTitle := ""
		occupation := ""
		alternateNames := []string{"Default Name"}
		dnString := fmt.Sprintf("CN=%s %s, O=Default Organization", givenName, familyName)

		// Assume default values for address components
		country := "Default Country"
		region := "Default Region"
		locality := "Default City"
		postalCode := "00000"
		street := "Default Street 1"
		poBox := ""

		// Call the spacedatastandards_utils.CreateEPM with the collected data
		epmBytes := spacedatastandards_utils.CreateEPM(
			dnString,
			legalName,
			familyName,
			givenName,
			additionalName,
			honorificPrefix,
			honorificSuffix,
			jobTitle,
			occupation,
			alternateNames,
			email,
			"", // Telephone not set
			country,
			region,
			locality,
			postalCode,
			street,
			poBox,
			n.Wallet,
			n.signingAccount,
			n.encryptionAccount,
		)

		fmt.Println("EPM created successfully. Length of EPM bytes:", len(epmBytes))

		var CID string
		var err error
		maxRetries := 3

		for i := 0; i < maxRetries; i++ {
			CID, err = spacedatastandards_utils.GenerateCID(epmBytes)
			if err != nil {
				fmt.Printf("Attempt %d: Failed to generate CID, error: %v\n", i+1, err)
				continue
			}
			if CID != "" {
				fmt.Println("Print CID from autogenerate:", CID)
				break
			}
			fmt.Printf("Attempt %d: Received blank CID, retrying...\n", i+1)
		}

		if CID == "" {
			panic("Failed to generate a valid CID after 3 attempts.")
		}

		//sig, err := n.Wallet.SignData(n.signingAccount, "application/octet-stream", []byte(CID))
		//if err != nil {
		//	fmt.Printf("Failed to sign CID: %v\n", err)
		//	return
		//}
		//signatureHex := hex.EncodeToString(sig)
		//formattedSignature := fmt.Sprintf("0x%s", signatureHex)

		// Create PNM and save EPM and PNM to KeyStore
		//pnmBytes := spacedatastandards_utils.CreatePNM("", CID, formattedSignature)
		//n.KeyStore.SaveEPM(epmBytes)
		//n.KeyStore.SavePNM(pnmBytes)*/
}

func CreateServerEPM() {

	newNode := setupNode()
	reader := bufio.NewReader(os.Stdin)

	vepm := []byte("")

	if len(vepm) > 0 {
		epm := EPM.GetSizePrefixedRootAsEPM(vepm, 0)

		fmt.Println(string(epm.EMAIL()))
	}
	entityType, _ := readInput(reader, "Are you creating a profile for an Organization or a Person? (O/P): ")
	isPerson := strings.ToUpper(entityType) == "P"

	fmt.Println("Creating a server EPM...")

	email, err := enforceValidEmail(reader, "Enter email: ")
	if err != nil {
		fmt.Printf("Error reading email: %v\n", err)
		return
	}

	telephone, _ := readInput(reader, "Enter telephone: ")

	var legalName, familyName, givenName, additionalName, honorificPrefix, honorificSuffix, jobTitle, occupation string

	country, _ := readInput(reader, "Enter country: ")
	region, _ := readInput(reader, "Enter region/state: ")
	locality, _ := readInput(reader, "Enter locality/city: ")
	postalCode, _ := readInput(reader, "Enter postal code: ")
	street, _ := readInput(reader, "Enter street address: ")
	poBox, _ := readInput(reader, "Enter post office box number (if any): ")

	if isPerson {
		// Person-specific fields
		familyName, _ = readInput(reader, "Enter family name: ")
		givenName, _ = readInput(reader, "Enter given name: ")
		additionalName, _ = readInput(reader, "Enter additional name: ")
		honorificPrefix, _ = readInput(reader, "Enter honorific prefix: ")
		honorificSuffix, _ = readInput(reader, "Enter honorific suffix: ")
		jobTitle, _ = readInput(reader, "Enter job title: ")
	} else {
		legalName, _ = readInput(reader, "Enter organization name: ")
	}

	altNamesInput, _ := readInput(reader, "Enter alternate names (comma-separated): ")

	// Parse comma-separated alternate names and multiformat addresses
	alternateNames := parseInput(altNamesInput)
	dnString, _ := readInput(reader, "Enter DN (e.g., 'CN=John Doe, O=E Corp, OU=IT, DC=ex, DC=com'): ")

	// Call the spacedatastandards_utils.CreateEPM with the collected data
	epmBytes := spacedatastandards_utils.CreateEPM(
		dnString,
		legalName,
		familyName,
		givenName,
		additionalName,
		honorificPrefix,
		honorificSuffix,
		jobTitle,
		occupation,
		alternateNames,
		email,
		telephone,
		country,
		region,
		locality,
		postalCode,
		street,
		poBox,
		newNode.Wallet,
		newNode.signingAccount,
		newNode.encryptionAccount,
	)

	// Handle the generated EPM bytes, such as saving them to a file or sending over a network.
	fmt.Println("EPM created successfully. Length of EPM bytes:", len(epmBytes))
	/*CID, _ := spacedatastandards_utils.GenerateCID(epmBytes)

	sig, err := newNode.Wallet.SignData(newNode.signingAccount, "application/octet-stream", []byte(CID))
	if err != nil {
		stackTrace := captureStackTrace()
		wrappedErr := fmt.Errorf("failed to sign CID: %w\nStack trace:\n%s", err, stackTrace)
		fmt.Println(wrappedErr)
	}
	signatureHex := hex.EncodeToString(sig)
	formattedSignature := fmt.Sprintf("0x%s", signatureHex)

	pnmBytes := spacedatastandards_utils.CreatePNM("/ip4/127.0.0.1/tcp/4001", CID, formattedSignature)
	//TODO save PNM
	newNode.KeyStore.SaveEPM(epmBytes)
	newNode.KeyStore.SavePNM(pnmBytes)*/
}

func ReadServerEPM(showQR ...bool) {

	vepm := []byte("")
	if len(vepm) == 0 {
		fmt.Println("EPM not found, run with flag '-create-server-epm' to generate")
		return
	}
	vCard := spacedatastandards_utils.ConvertTovCard(vepm)

	reader := bufio.NewReader(os.Stdin)
	fmt.Println("Enter the path to save the vCard (.vcf), or leave blank to skip: ")
	vCardPath, _ := reader.ReadString('\n')
	vCardPath = strings.TrimSpace(vCardPath)
	if vCardPath != "" {
		vCardPath = ensureValidPath(vCardPath, "server.vcf")
		saveToFile(vCardPath, vCard)
		fmt.Printf("vCard saved to %s\n", vCardPath)
	}

	if len(showQR) > 0 && showQR[0] {
		fmt.Println("Enter the path to save the QR code (.png), or leave blank to skip: ")
		qrPath, _ := reader.ReadString('\n')
		qrPath = strings.TrimSpace(qrPath)
		if qrPath != "" {
			qrPath = ensureValidPath(qrPath, "server_qr.png")

			// Generate and save the QR code to a file
			err := qrcode.WriteFile(vCard, qrcode.Medium, 256, qrPath)
			if err != nil {
				fmt.Printf("Failed to generate QR code: %v\n", err)
			} else {
				fmt.Printf("QR code saved to %s\n", qrPath)
			}
		}
		generateAndDisplayQRCode(vCard)
	}

	// Only print the vCard if no path was provided for saving
	fmt.Println(vCard)
	/*pnmBytes, _ := newNode.KeyStore.LoadPNM()

	pnm := PNM.GetSizePrefixedRootAsPNM(pnmBytes, 0)

	fmt.Println("EPM CID: ", string(pnm.CID()))
	fmt.Println("Ethereum Digital Signature: ", string(pnm.SIGNATURE()))*/
}

func generateAndDisplayQRCode(content string) {
	config := qrterminal.Config{
		Level:          qrterminal.L,
		Writer:         os.Stdout,
		HalfBlocks:     true,
		BlackChar:      qrterminal.BLACK_BLACK,
		WhiteBlackChar: qrterminal.WHITE_BLACK,
		WhiteChar:      qrterminal.WHITE_WHITE,
		BlackWhiteChar: qrterminal.BLACK_WHITE,
		QuietZone:      0,
	}

	fmt.Println("QR code:")
	qrterminal.GenerateWithConfig(content, config)
}

func readPassword() (string, error) {
	bytePassword, err := bufio.NewReader(os.Stdin).ReadBytes('\n')
	if err != nil {
		return "", err
	}
	password := string(bytePassword)
	return strings.TrimSpace(password), nil
}

func readInput(reader *bufio.Reader, prompt string) (string, error) {
	fmt.Print(prompt)
	input, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(input), nil
}

func parseInput(input string) []string {
	input = strings.TrimSpace(input)
	if input == "" {
		return []string{}
	}
	return strings.Split(input, ",")
}

// readInputWithDefault prompts the user with a default value
func readInputWithDefault(reader *bufio.Reader, prompt, defaultValue string) (string, error) {
	fmt.Printf("%s [%s]: ", prompt, defaultValue)
	input, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	input = strings.TrimSpace(input)
	if input == "" {
		return defaultValue, nil
	}
	return input, nil
}

// isValidEmail checks if the given string is a valid email
func isValidEmail(email string) bool {
	emailRegex := regexp.MustCompile(`^[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,4}$`)
	return emailRegex.MatchString(email)
}

// enforceValidEmail keeps prompting the user until a valid email is entered
func enforceValidEmail(reader *bufio.Reader, prompt string) (string, error) {
	for {
		email, err := readInput(reader, prompt)
		if err != nil {
			return "", err
		}
		if isValidEmail(email) {
			return email, nil
		}
		fmt.Println("Invalid email format. Please enter a valid email.")
	}
}

func ensureValidPath(inputPath, defaultName string) string {
	var currentDir string
	if !strings.HasPrefix(inputPath, "/") {
		// Handle relative path
		currentDir, _ := os.Getwd()
		inputPath = filepath.Join(currentDir, inputPath)
	}

	dirPath := filepath.Dir(inputPath)
	if _, err := os.Stat(dirPath); os.IsNotExist(err) {
		fmt.Printf("Directory %s does not exist, using current directory.\n", dirPath)
		inputPath = filepath.Join(currentDir, defaultName)
	}

	return inputPath
}

func saveToFile(path, content string) {
	err := os.WriteFile(path, []byte(content), 0644)
	if err != nil {
		fmt.Printf("Failed to save file %s: %v\n", path, err)
	}
}
