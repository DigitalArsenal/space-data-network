package node

import (
	"bufio"
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	sds_utils "github.com/DigitalArsenal/space-data-network/internal/node/sds_utils"
	server_info "github.com/DigitalArsenal/space-data-network/internal/node/server_info"
	"github.com/DigitalArsenal/space-data-network/internal/spacedatastandards/EPM"
	"github.com/mdp/qrterminal/v3"
	"github.com/skip2/go-qrcode"
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

func CreateDefaultServerEPM(ctx context.Context, node *Node) {

	vepm, _ := server_info.LoadEPMFromFile()
	if len(vepm) > 0 {
		return
	}

	fmt.Println("Creating a server A EPM...")

	if node == nil || node.Host == nil {
		// Node or node.Host is nil, can't proceed further
		fmt.Println("Node or node.Host is nil, cannot create default server EPM.")
		return
	}

	//lint:ignore SA5011 check above for nil
	peerID := node.Host.ID()
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

	var signingPublicKeyHex, encryptionPublicKeyHex string

	var err error
	// Get the hexadecimal representation of the public keys
	signingPublicKeyHex, err = node.Wallet.PublicKeyHex(node.signingAccount)
	if err != nil {
		fmt.Printf("Error getting signing public key: %v\n", err)
		return
	}
	signingPublicKeyHex = "0x" + signingPublicKeyHex

	encryptionPublicKeyHex, err = node.Wallet.PublicKeyHex(node.encryptionAccount)
	if err != nil {
		fmt.Printf("Error getting encryption public key: %v\n", err)
		return
	}
	encryptionPublicKeyHex = "0x" + encryptionPublicKeyHex

	// Call the sds_utils.CreateEPM with the collected data
	epmBytes := sds_utils.CreateEPM(
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
		signingPublicKeyHex,
		encryptionPublicKeyHex,
	)

	fmt.Println("EPM created successfully. Length of EPM bytes:", len(epmBytes))

	CID, _ := node.AddFileFromBytes(ctx, epmBytes)
	CIDString := CID.String()
	sig, err := node.Wallet.SignData(node.signingAccount, "application/octet-stream", []byte(CIDString))
	if err != nil {
		fmt.Printf("Failed to sign CID: %v\n", err)
		return
	}
	signatureHex := hex.EncodeToString(sig)
	formattedSignature := fmt.Sprintf("0x%s", signatureHex)

	//Create PNM and save EPM and PNM to KeyStore
	pnmBytes := sds_utils.CreatePNM("", CIDString, formattedSignature, "EPM")
	server_info.SaveEPMToFile(epmBytes)
	server_info.SavePNMToFile(pnmBytes)
	node.SDSTopic.Publish(ctx, pnmBytes)
}

func CreateServerEPM(ctx context.Context, epmBytes []byte, node *Node) []byte {

	reader := bufio.NewReader(os.Stdin)
	var epm *EPM.EPM
	var err error
	var outputEPMBytes []byte
	var email, telephone, legalName, familyName, givenName, additionalName string
	var honorificPrefix, honorificSuffix, jobTitle, occupation string
	var country, region, locality, postalCode, street, poBox string
	var dnString, signingPublicKeyHex, encryptionPublicKeyHex string
	var alternateNames []string
	var isPerson bool

	if len(epmBytes) > 0 {
		epm, err = sds_utils.DeserializeEPM(ctx, epmBytes)
		if err != nil {
			fmt.Printf("Error deserializing EPM: %v\n", err)
			return nil
		}
		// Extract properties using FlatBuffer getters
		email = string(epm.EMAIL())
		telephone = string(epm.TELEPHONE())
		legalName = string(epm.LEGAL_NAME())
		familyName = string(epm.FAMILY_NAME())
		givenName = string(epm.GIVEN_NAME())
		additionalName = string(epm.ADDITIONAL_NAME())
		honorificPrefix = string(epm.HONORIFIC_PREFIX())
		honorificSuffix = string(epm.HONORIFIC_SUFFIX())
		jobTitle = string(epm.JOB_TITLE())
		occupation = string(epm.OCCUPATION())
		dnString = string(epm.DN())

		address := new(EPM.Address)
		epm.ADDRESS(address) // Assuming this method populates the 'address' object
		country = string(address.COUNTRY())
		region = string(address.REGION())
		locality = string(address.LOCALITY())
		postalCode = string(address.POSTAL_CODE())
		street = string(address.STREET())
		poBox = string(address.POST_OFFICE_BOX_NUMBER())

	} else {

		entityType, _ := readInput(reader, "Are you creating a profile for an Organization or a Person? (O/P): ")
		isPerson = strings.ToUpper(entityType) == "P"

		fmt.Println("Creating a server EPM...")

		email, err = enforceValidEmail(reader, "Enter email: ")
		if err != nil {
			fmt.Printf("Error reading email: %v\n", err)
			return nil
		}

		telephone, _ = readInput(reader, "Enter telephone: ")
		country, _ = readInput(reader, "Enter country: ")
		region, _ = readInput(reader, "Enter region/state: ")
		locality, _ = readInput(reader, "Enter locality/city: ")
		postalCode, _ = readInput(reader, "Enter postal code: ")
		street, _ = readInput(reader, "Enter street address: ")
		poBox, _ = readInput(reader, "Enter post office box number (if any): ")

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
		alternateNames = parseInput(altNamesInput)
		dnString, _ = readInput(reader, "Enter DN (e.g., 'CN=John Doe, O=E Corp, OU=IT, DC=ex, DC=com'): ")

	}

	if node != nil {
		var err error
		// Get the hexadecimal representation of the public keys
		signingPublicKeyHex, err = node.Wallet.PublicKeyHex(node.signingAccount)
		if err != nil {
			fmt.Printf("Error getting signing public key: %v\n", err)
			return nil // Or handle the error as appropriate
		}
		signingPublicKeyHex = "0x" + signingPublicKeyHex

		encryptionPublicKeyHex, err = node.Wallet.PublicKeyHex(node.encryptionAccount)
		if err != nil {
			fmt.Printf("Error getting encryption public key: %v\n", err)
			return nil // Or handle the error as appropriate
		}
		encryptionPublicKeyHex = "0x" + encryptionPublicKeyHex
	}

	// Call the sds_utils.CreateEPM with the collected data
	outputEPMBytes = sds_utils.CreateEPM(
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
		signingPublicKeyHex,
		encryptionPublicKeyHex,
	)

	if node == nil {
		return outputEPMBytes
	}

	// Handle the generated EPM bytes, such as saving them to a file or sending over a network.
	// fmt.Println("EPM created successfully. Length of EPM bytes:", len(outputEPMBytes))
	CID, _ := node.AddFileFromBytes(ctx, outputEPMBytes)
	CIDString := CID.String()
	sig, err := node.Wallet.SignData(node.signingAccount, "application/octet-stream", []byte(CIDString))
	if err != nil {
		stackTrace := captureStackTrace()
		wrappedErr := fmt.Errorf("failed to sign CID: %w\nStack trace:\n%s", err, stackTrace)
		fmt.Println(wrappedErr)
	}
	signatureHex := hex.EncodeToString(sig)
	formattedSignature := fmt.Sprintf("0x%s", signatureHex)

	pnmBytes := sds_utils.CreatePNM("/ip4/127.0.0.1/tcp/4001", CIDString, formattedSignature, "EPM")

	if len(signingPublicKeyHex) > 0 && len(encryptionPublicKeyHex) > 0 {
		server_info.SaveEPMToFile(outputEPMBytes)
		server_info.SavePNMToFile(pnmBytes)
		node.SDSTopic.Publish(ctx, pnmBytes)
	}

	return outputEPMBytes
}

func ReadServerEPM(showQR ...bool) {

	vepm := []byte("")
	if len(vepm) == 0 {
		fmt.Println("EPM not found, run with flag '-create-server-epm' to generate")
		return
	}
	vCard := sds_utils.ConvertTovCard(vepm)

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
