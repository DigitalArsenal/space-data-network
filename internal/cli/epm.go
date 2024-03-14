package cli

import (
	"bufio"
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	config "github.com/DigitalArsenal/space-data-network/configs"
	node "github.com/DigitalArsenal/space-data-network/internal/node"
	flatbuffer_utils "github.com/DigitalArsenal/space-data-network/internal/node/flatbuffer_utils"
	"github.com/mdp/qrterminal/v3"
)

func setupNode() *node.Node {

	clearTerminal()

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

	// Call the flatbuffer_utils.CreateEPM with the collected data
	epmBytes := flatbuffer_utils.CreateEPM(
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
	)

	// Print out the EPM data for confirmation
	// Modified printEPM function to conditionally display person-specific fields
	printEPM(isPerson, dnString, legalName, email, telephone, alternateNames, familyName, givenName, additionalName, honorificPrefix, honorificSuffix, jobTitle, occupation)

	// Handle the generated EPM bytes, such as saving them to a file or sending over a network.
	fmt.Println("EPM created successfully. Length of EPM bytes:", len(epmBytes))
	CID, _ := flatbuffer_utils.GenerateCID(epmBytes)

	account, _ := newNode.GetAccount(config.Conf.Datastore.EthereumDerivationPath) //0x3835e5C7A36A2cE6A1a9b7cbd2c2276bd5538BdD
	sig, err := newNode.GetWallet().SignData(account, "application/octet-stream", []byte(CID))
	if err != nil {
		fmt.Println("failed to sign CID: %w", err)
	}
	signatureHex := hex.EncodeToString(sig)
	formattedSignature := fmt.Sprintf("0x%s", signatureHex)
	fmt.Println("CID:", CID)
	fmt.Println("Ethereum signature:", formattedSignature)

	//TODO save PNM
	newNode.KeyStore.SaveEPM(epmBytes)
}
func ReadServerEPM() {

	newNode := setupNode()
	vCard := flatbuffer_utils.ConvertTovCard(newNode.KeyStore.LoadEPM())
	fmt.Println(vCard)
	generateAndDisplayQRCode(vCard)

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

func printEPM(isPerson bool, dnString, legalName, email, telephone string, alternateNames []string, familyName, givenName, additionalName, honorificPrefix, honorificSuffix, jobTitle, occupation string) {
	clearTerminal()

	fmt.Printf("DN: %s\n", dnString)
	fmt.Printf("Organization Name: %s\n", legalName)
	fmt.Printf("Email: %s\n", email)
	fmt.Printf("Telephone: %s\n", telephone)
	fmt.Printf("Alternate Names: %s\n", strings.Join(alternateNames, ", "))

	if isPerson {
		fmt.Printf("Family Name: %s\n", familyName)
		fmt.Printf("Given Name: %s\n", givenName)
		fmt.Printf("Additional Name: %s\n", additionalName)
		fmt.Printf("Honorific Prefix: %s\n", honorificPrefix)
		fmt.Printf("Honorific Suffix: %s\n", honorificSuffix)
		fmt.Printf("Job Title: %s\n", jobTitle)
		fmt.Printf("Occupation: %s\n", occupation)
	}
}

func clearTerminal() {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("cmd", "/c", "cls")
	default:
		cmd = exec.Command("clear")
	}
	cmd.Stdout = os.Stdout
	cmd.Run()
}
