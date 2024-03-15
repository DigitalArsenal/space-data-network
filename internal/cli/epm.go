package cli

import (
	"bufio"
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	config "github.com/DigitalArsenal/space-data-network/configs"
	node "github.com/DigitalArsenal/space-data-network/internal/node"
	spacedatastandards_utils "github.com/DigitalArsenal/space-data-network/internal/node/spacedatastandards_utils"
	"github.com/mdp/qrterminal/v3"
)

func setupNode() *node.Node {

	// Prompt for KeyStore password
	fmt.Println("The password is usually set in the environment variable: \n" +
		"\n'SPACE_DATA_NETWORK_DATASTORE_PASSWORD' \n\n" +
		"and is used to access the application's keystore. \n" +
		"If not set, the application will use a default password" + "\n ")
	fmt.Print("KeyStore Password: ")
	password, err := readPassword()
	if err != nil {
		fmt.Printf("Failed to read password: %v\n", err)
		return nil
	}

	// Set the configuration property for KeyStore password
	config.Conf.Datastore.Password = strings.TrimSpace(password)

	// Create a new node, which will use the updated configuration for its KeyStore
	newNode, err := node.NewNode(context.Background())
	if err != nil {
		fmt.Printf("Failed to create new node: %v\n", err)
		return nil
	}
	return newNode
}

func CreateServerEPM() {

	newNode := setupNode()
	reader := bufio.NewReader(os.Stdin)

	entityType, _ := readInput(reader, "Are you creating a profile for an Organization or a Person? (O/P): ")
	isPerson := strings.ToUpper(entityType) == "P"

	fmt.Println("Creating a server EPM...")

	email, _ := readInput(reader, "Enter email: ")
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
	dnString, _ := readInput(reader, "Enter DN components (e.g., 'CN=John Doe, O=Example Corp, OU=IT Dept, DC=example, DC=com'): ")
	publicKey, _ := newNode.PublicKey()

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
		newNode.GetWallet(),
		newNode.GetSigningAccount(),
		newNode.GetEncryptionAccount(),
	)

	// Handle the generated EPM bytes, such as saving them to a file or sending over a network.
	fmt.Println("EPM created successfully. Length of EPM bytes:", len(epmBytes))
	CID, _ := spacedatastandards_utils.GenerateCID(epmBytes)

	sig, err := newNode.GetWallet().SignData(newNode.GetSigningAccount(), "application/octet-stream", []byte(CID))
	if err != nil {
		fmt.Printf("failed to sign CID: %s\n", err)
	}
	signatureHex := hex.EncodeToString(sig)
	formattedSignature := fmt.Sprintf("0x%s", signatureHex)
	fmt.Println("CID:", CID)
	fmt.Println("Ethereum signature:", formattedSignature)
	fmt.Println(publicKey)

	pnmBytes := spacedatastandards_utils.CreatePNM("", CID, formattedSignature)
	//TODO save PNM
	newNode.KeyStore.SaveEPM(epmBytes)
	newNode.KeyStore.SavePNM(pnmBytes)
}

func ReadServerEPM(showQR ...bool) {
	newNode := setupNode() // Assuming setupNode returns an instance of your node
	vCard := spacedatastandards_utils.ConvertTovCard(newNode.KeyStore.LoadEPM())

	if len(showQR) > 0 && showQR[0] {
		generateAndDisplayQRCode(vCard)
	} else {
		fmt.Println(vCard)
	}
}

func generateAndDisplayQRCode(content string) {
	config := qrterminal.Config{
		Level:          qrterminal.M,
		Writer:         os.Stdout,
		HalfBlocks:     true,
		BlackChar:      qrterminal.BLACK_BLACK,
		WhiteBlackChar: qrterminal.WHITE_BLACK,
		WhiteChar:      qrterminal.WHITE_WHITE,
		BlackWhiteChar: qrterminal.BLACK_WHITE,
		QuietZone:      1,
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
