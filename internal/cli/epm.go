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
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/mdp/qrterminal/v3"
	hdwallet "github.com/miguelmota/go-ethereum-hdwallet"
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

func printWalletAccountsAndMatch(wallet *hdwallet.Wallet, targetAccount accounts.Account) {
	// Assuming the wallet can return a list of accounts it manages
	accounts := wallet.Accounts()
	fmt.Println(len(accounts))
	signingAddress := targetAccount.Address.Hex() // Get the hex string of the target account address

	matched := false
	for _, account := range accounts {
		address := account.Address.Hex() // Get the hex string of the current account address
		fmt.Println("Wallet Ethereum Address:", address)

		// Check if the current account address matches the target account address
		if address == signingAddress {
			fmt.Println("Match found for address:", address)
			matched = true
			break // Optional: Break the loop if a match is found
		}
	}

	if !matched {
		fmt.Println("No matching address found in the wallet for the target account.")
	}
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
		newNode.GetWallet(),
		newNode.GetSigningAccount(),
		newNode.GetEncryptionAccount(),
	)

	// Handle the generated EPM bytes, such as saving them to a file or sending over a network.
	fmt.Println("EPM created successfully. Length of EPM bytes:", len(epmBytes))
	CID, _ := flatbuffer_utils.GenerateCID(epmBytes)

	printWalletAccountsAndMatch(newNode.GetWallet(), newNode.GetSigningAccount())
	printWalletAccountsAndMatch(newNode.GetWallet(), newNode.GetEncryptionAccount())

	sig, err := newNode.GetWallet().SignData(newNode.GetSigningAccount(), "application/octet-stream", []byte(CID))
	if err != nil {
		fmt.Printf("failed to sign CID: %s\n", err)
	}
	signatureHex := hex.EncodeToString(sig)
	formattedSignature := fmt.Sprintf("0x%s", signatureHex)
	fmt.Println("CID:", CID)
	fmt.Println("Ethereum signature:", formattedSignature)
	fmt.Println(publicKey)

	//TODO save PNM
	newNode.KeyStore.SaveEPM(epmBytes)
}

func ReadServerEPM(showQR ...bool) {
	newNode := setupNode() // Assuming setupNode returns an instance of your node
	vCard := flatbuffer_utils.ConvertTovCard(newNode.KeyStore.LoadEPM())

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
